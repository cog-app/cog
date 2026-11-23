package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Tailscale fronts Cog with the Tailscale already running on this
// computer: `tailscale serve` terminates HTTPS at the computer's tailnet
// name (https://host.tailnet.ts.net) with a certificate Tailscale manages,
// and proxies to Cog's plain listener on 127.0.0.1. `tailscale funnel`
// does the same and also lets the internet in, which is what share links
// for people without Tailscale need. Nothing is forwarded on the router
// and Cog binds nothing beyond loopback.
//
// Setup drives the tailscale command rather than embedding a node
// (tsnet): it reuses the sign-in and the device the person already has,
// and adds nothing to the binary.

// Tailscale modes, as config.Install.Tailscale records them.
const (
	TailscaleServe  = "serve"
	TailscaleFunnel = "funnel"
)

// TailscaleStatus is what the tailscale command says about this computer.
type TailscaleStatus struct {
	Installed bool
	Running   bool   // signed in and connected
	Name      string // the computer's tailnet name, host.tailnet.ts.net
	// HTTPS is whether the tailnet issues certificates (DNS > HTTPS
	// Certificates in the admin console), which serve needs.
	HTTPS bool
	Err   error
}

// tailscaleCommand runs the tailscale command with args. A variable for
// tests.
var tailscaleCommand = func(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := tailscaleBinary()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if strings.Contains(msg, "Access denied") || strings.Contains(msg, "permission denied") {
			msg += ". Let your account change Tailscale's settings: sudo tailscale set --operator=" + currentUser()
		}
		if msg == "" {
			return nil, fmt.Errorf("tailscale %s: %w", strings.Join(args, " "), err)
		}
		return nil, fmt.Errorf("tailscale %s: %s", strings.Join(args, " "), msg)
	}
	return out.Bytes(), nil
}

var errNoTailscale = errors.New("tailscale is not installed")

func tailscaleBinary() (string, error) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	var known []string
	switch runtime.GOOS {
	case "darwin":
		known = []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale"}
	case "windows":
		known = []string{os.Getenv("ProgramFiles") + `\Tailscale\tailscale.exe`}
	}
	for _, p := range known {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errNoTailscale
}

// CheckTailscale asks the tailscale command about this computer.
func CheckTailscale(ctx context.Context) TailscaleStatus {
	out, err := tailscaleCommand(ctx, "status", "--json")
	if errors.Is(err, errNoTailscale) {
		return TailscaleStatus{}
	}
	st := TailscaleStatus{Installed: true}
	if err != nil {
		// tailscale status exits non-zero when stopped or signed out.
		st.Err = err
		return st
	}
	var s struct {
		BackendState string
		Self         struct{ DNSName string }
		CertDomains  []string
	}
	if err := json.Unmarshal(out, &s); err != nil {
		st.Err = fmt.Errorf("tailscale status: %w", err)
		return st
	}
	st.Running = s.BackendState == "Running"
	st.Name = strings.TrimSuffix(s.Self.DNSName, ".")
	st.HTTPS = st.Name != "" && slices.Contains(s.CertDomains, st.Name)
	return st
}

// Problem is what stops Cog being served through Tailscale, "" if
// nothing does.
func (s TailscaleStatus) Problem() string {
	switch {
	case !s.Installed:
		return "Tailscale is not installed on this computer. Install it from tailscale.com and sign in, then choose this again."
	case s.Err != nil:
		return "Tailscale is not connected (" + s.Err.Error() + "). Open Tailscale and sign in, then choose this again."
	case !s.Running || s.Name == "":
		return "Tailscale is not connected. Open Tailscale and sign in, then choose this again."
	case !s.HTTPS:
		return "Your tailnet does not issue HTTPS certificates yet. Turn on HTTPS Certificates on the DNS page of Tailscale's admin console, then choose this again."
	}
	return ""
}

// applyTailscale points the computer's tailnet HTTPS port at Cog, or stops
// doing so when the plan no longer uses Tailscale.
func applyTailscale(ctx context.Context, p Plan, out io.Writer) error {
	want := ""
	if p.Access == AccessTailscale {
		want = TailscaleServe
		if p.Funnel {
			want = TailscaleFunnel
		}
	}
	if p.Mode == ModeUninstall {
		want = ""
	}
	// Turn off what setup set up before unless it is what stays: serve
	// and funnel are separate settings for the same port.
	if p.PrevTailscale != "" && p.PrevTailscale != want {
		step(out, "Turning off tailscale %s", p.PrevTailscale)
		if _, err := tailscaleCommand(ctx, p.PrevTailscale, "--https=443", "off"); err != nil {
			return err
		}
	}
	if want == "" {
		return nil
	}
	target := "http://127.0.0.1:" + strconv.Itoa(p.Port)
	step(out, "Pointing https://%s at Cog (tailscale %s)", p.TailscaleName, want)
	if _, err := tailscaleCommand(ctx, want, "--bg", "--https=443", target); err != nil {
		return err
	}
	return nil
}
