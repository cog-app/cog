package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/internal/certs"
	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/service"
)

// FinishRequest is what Apply asks of `cog setup finish`, which runs as the
// account the server runs as so that what it makes belongs to that
// account. It is passed on standard input: it holds a password.
type FinishRequest struct {
	AdminUser     string `json:"admin_user,omitempty"`
	AdminPassword string `json:"admin_password,omitempty"`
}

// Result is what Apply did, for the wizard's last page.
type Result struct {
	URL      string   `json:"url"`      // where to open Cog
	Warnings []string `json:"warnings"` // what needs attention
}

// ops are what Apply does to the system, apart from files: tests replace
// them, since a test cannot install services.
var ops = struct {
	IsAdmin       func() bool
	Query         func(service.Spec) (service.Status, error)
	Stop          func(service.Spec) error
	Install       func(service.Spec) error
	Uninstall     func(service.Spec) error
	CreateAccount func(name, home string) error
	GiveDir       func(dir, account string) error
	OpenFirewall  func([]service.Port) error
	Finish        func(ctx context.Context, p Plan, req FinishRequest, out io.Writer) error
}{
	IsAdmin:       service.IsAdmin,
	Query:         service.Query,
	Stop:          service.Stop,
	Install:       service.Install,
	Uninstall:     service.Uninstall,
	CreateAccount: service.CreateAccount,
	GiveDir:       service.GiveDir,
	OpenFirewall:  service.OpenFirewall,
	Finish:        finish,
}

// Apply carries out p, writing what it does to out. A system service
// needs administrator rights (service.IsAdmin); the wizard runs Apply in
// an elevated process for it (Elevate).
func Apply(ctx context.Context, p Plan, out io.Writer) (Result, error) {
	var res Result
	if err := p.Validate(); err != nil {
		return res, err
	}
	if p.Scope == service.System && !ops.IsAdmin() {
		return res, errElevation
	}
	spec := p.Service()
	l := p.Layout()
	switch p.Mode {
	case ModeUninstall:
		step(out, "Removing the %s service", p.Scope)
		if err := ops.Uninstall(spec); err != nil {
			return res, err
		}
		step(out, "Cog's files and data are still in %s: delete the folder to remove them too.", l.Dir)
		return res, nil
	case ModeResetAdmin:
		step(out, "Setting the password of %s", p.AdminUser)
		return res, ops.Finish(ctx, p, FinishRequest{AdminUser: p.AdminUser, AdminPassword: p.AdminPassword}, out)
	}

	st, err := ops.Query(spec)
	if err != nil {
		return res, err
	}
	if st.Running {
		step(out, "Stopping the running server")
		if err := ops.Stop(spec); err != nil {
			return res, err
		}
	}

	step(out, "Making %s", l.Dir)
	for _, d := range []string{l.Dir, filepath.Dir(l.Bin)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return res, fmt.Errorf("make %s: %w", d, err)
		}
	}
	if err := os.MkdirAll(l.Data, 0o700); err != nil {
		return res, fmt.Errorf("make %s: %w", l.Data, err)
	}
	if p.Scope == service.System {
		step(out, "Making the account %s for the server", p.Account())
		if err := ops.CreateAccount(p.Account(), l.Dir); err != nil {
			return res, err
		}
	}

	if same, err := sameFile(p.Source, l.Bin); err != nil {
		return res, err
	} else if !same {
		step(out, "Copying Cog to %s", l.Bin)
		if err := copyFile(p.Source, l.Bin); err != nil {
			return res, err
		}
	}

	base := config.Defaults()
	if _, err := os.Stat(l.Config); err == nil {
		if base, err = config.Load(l.Config); err != nil {
			return res, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return res, fmt.Errorf("configuration: %w", err)
	}
	cfg := p.Config(base)
	step(out, "Writing %s", l.Config)
	if err := config.Save(l.Config, cfg); err != nil {
		return res, err
	}

	// Here rather than in finish: for a system service on Linux, the
	// server's account may not listen on 80 or 443 outside its unit.
	if cfg.TLS.Mode == "acme" {
		if err := obtainCertificate(ctx, cfg, out); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("No certificate yet: %v. The server keeps trying every hour; once the problem is fixed it serves HTTPS without being set up again.", err))
			step(out, "Could not get a certificate: %v", err)
		}
	}

	if p.Scope == service.System {
		step(out, "Giving %s to %s", l.Dir, p.Account())
		if err := ops.GiveDir(l.Dir, p.Account()); err != nil {
			return res, err
		}
	}

	if p.AdminUser != "" {
		step(out, "Setting up the admin account %s", p.AdminUser)
	}
	if err := ops.Finish(ctx, p, FinishRequest{AdminUser: p.AdminUser, AdminPassword: p.AdminPassword}, out); err != nil {
		return res, err
	}

	if p.Firewall {
		if ports := p.reachablePorts(); len(ports) > 0 {
			step(out, "Opening the firewall")
			if err := ops.OpenFirewall(ports); err != nil {
				return res, err
			}
		}
	}

	step(out, "Installing the %s service", p.Scope)
	if err := ops.Install(spec); errors.Is(err, service.ErrNotWhileSignedOut) {
		res.Warnings = append(res.Warnings, "Cog stops when you sign out ("+err.Error()+"). To keep it running, ask an administrator to run: loginctl enable-linger "+currentUser())
	} else if err != nil {
		return res, err
	}
	local := "http://127.0.0.1:" + strconv.Itoa(p.Port) + "/"
	step(out, "Waiting for Cog to answer")
	if err := waitReady(ctx, local, time.Minute); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("Cog did not answer at %s: %v. Check its log (%s).", local, err, logHint(p)))
	}
	res.URL = cfg.BaseURL
	if p.Access == AccessInternet && len(res.Warnings) > 0 {
		res.URL = local
	}
	step(out, "Done")
	return res, nil
}

// reachablePorts are the ports others connect to: none for a server only
// this computer reaches.
func (p Plan) reachablePorts() []service.Port {
	var ports []service.Port
	for _, pt := range p.Ports() {
		if pt.Number == p.Port && (p.Access == AccessLocal || p.Access == AccessInternet) {
			continue // plain HTTP stays on this computer
		}
		ports = append(ports, pt)
	}
	return ports
}

func logHint(p Plan) string {
	switch {
	case p.Scope == service.User && filepath.Separator == '\\':
		return "run cog serve from a terminal to see it"
	case filepath.Separator == '\\':
		return "Event Viewer, Windows Logs > Application"
	case p.Scope == service.System:
		return "journalctl -u cog, or cog.log in " + p.Dir + " on macOS"
	default:
		return "journalctl --user -u cog, or cog.log in " + p.Dir + " on macOS"
	}
}

func step(out io.Writer, format string, args ...any) {
	fmt.Fprintf(out, format+"\n", args...)
}

// finish runs `cog setup finish` from the install, as the server's account.
func finish(ctx context.Context, p Plan, req FinishRequest, out io.Writer) error {
	l := p.Layout()
	in, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode finish request: %w", err)
	}
	cmd := exec.CommandContext(ctx, l.Bin, "setup", "finish", "--config", l.Config)
	cmd.Dir = l.Dir
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stdout, cmd.Stderr = out, out
	if p.Scope == service.System {
		if err := service.RunAs(cmd, p.Account()); err != nil {
			return err
		}
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("finish setup: %w", err)
	}
	return nil
}

// obtainCertificate gets the certificate cfg asks for, answering the
// challenge on the ports the server will use, unless it has a current one.
func obtainCertificate(ctx context.Context, cfg config.Config, out io.Writer) error {
	log := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelWarn}))
	m, err := certs.New(cfg.TLS, cfg.DataDir, log)
	if err != nil {
		return err
	}
	if !m.NeedsRenewal(time.Now()) {
		step(out, "The certificate for %s is current", cfg.TLS.Domains[0])
		return nil
	}
	step(out, "Asking Let's Encrypt for a certificate for %s (%s)", cfg.TLS.Domains[0], cfg.TLS.Challenge)
	stop, err := m.ServeChallenges()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	err = m.Obtain(ctx)
	if serr := stop(); serr != nil {
		err = errors.Join(err, fmt.Errorf("stop challenge listener: %w", serr))
	}
	if err != nil {
		return err
	}
	step(out, "Got a certificate, valid until %s", m.Expiry().Format("2 January 2006"))
	return nil
}

// waitReady waits for url to answer anything but a server error.
func waitReady(ctx context.Context, url string, limit time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	// trusted URL: the server setup just started, on this computer.
	client := &http.Client{Timeout: 5 * time.Second}
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode < 500 {
				return nil
			}
			err = fmt.Errorf("%s", res.Status)
		}
		last = err
		select {
		case <-ctx.Done():
			return last
		case <-time.After(time.Second):
		}
	}
}

// sameFile reports whether a and b are the same file: setup run from the
// installed binary does not copy it onto itself.
func sameFile(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, fmt.Errorf("setup binary: %w", err)
	}
	sb, err := os.Stat(b)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("installed binary: %w", err)
	}
	return os.SameFile(sa, sb), nil
}

// copyFile puts a copy of src at dst, replacing it in one rename.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		return errors.Join(fmt.Errorf("copy to %s: %w", tmp, err), out.Close(), os.Remove(tmp))
	}
	if err := out.Close(); err != nil {
		return errors.Join(fmt.Errorf("copy to %s: %w", tmp, err), os.Remove(tmp))
	}
	if err := os.Rename(tmp, dst); err != nil {
		return errors.Join(fmt.Errorf("install %s: %w", dst, err), os.Remove(tmp))
	}
	return nil
}

// currentUser is the name of the account setup runs as, for advice.
func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "$USER"
}
