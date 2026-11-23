package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// DefaultAccount is the account a system service runs as.
const DefaultAccount = "cog"

// unitPath is where the unit file for s goes.
func unitPath(s Spec) (string, error) {
	if s.Scope == System {
		return filepath.Join("/etc/systemd/system", s.Name+".service"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config directory: %w", err)
	}
	return filepath.Join(dir, "systemd", "user", s.Name+".service"), nil
}

// systemctl runs systemctl for s's scope.
func systemctl(s Spec, args ...string) error {
	if s.Scope == User {
		args = append([]string{"--user"}, args...)
	}
	return run("systemctl", args...)
}

// Unit is the systemd unit for s.
func Unit(s Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=%s\nAfter=network-online.target\nWants=network-online.target\n\n", s.Description)
	b.WriteString("[Service]\n")
	fmt.Fprintf(&b, "ExecStart=%s\n", systemdCommand(s.Exe, s.Args))
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", systemdCommand(s.Dir, nil))
	fmt.Fprintf(&b, "Environment=%s=1\n", SupervisedEnv)
	// Always, so that the exit an update ends with starts the new binary.
	b.WriteString("Restart=always\nRestartSec=2\n")
	if s.Scope == System {
		fmt.Fprintf(&b, "User=%s\nGroup=%s\n", s.Account, s.Account)
		// 80 and 443 without root, for a server not behind a router.
		b.WriteString("AmbientCapabilities=CAP_NET_BIND_SERVICE\n")
		b.WriteString("ProtectSystem=full\nPrivateTmp=true\n")
	}
	b.WriteString("\n[Install]\n")
	if s.Scope == System {
		b.WriteString("WantedBy=multi-user.target\n")
	} else {
		b.WriteString("WantedBy=default.target\n")
	}
	return b.String()
}

// systemdCommand joins exe and args as systemd parses ExecStart.
func systemdCommand(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	for _, a := range append([]string{exe}, args...) {
		if a == "" || strings.ContainsAny(a, " \t\"'\\$%") {
			a = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`).Replace(a) + `"`
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// Install writes and enables s's unit, and starts it.
func Install(s Spec) error {
	path, err := unitPath(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("unit directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(Unit(s)), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	if err := systemctl(s, "daemon-reload"); err != nil {
		return err
	}
	if err := systemctl(s, "enable", s.Name); err != nil {
		return err
	}
	if err := systemctl(s, "restart", s.Name); err != nil {
		return err
	}
	if s.Scope == User {
		// Without lingering, a user's services stop when they sign out
		// and start only when they sign in. Some systems keep it for
		// administrators: the service still runs while signed in.
		if err := run("loginctl", "enable-linger"); err != nil {
			return fmt.Errorf("%w: %w", ErrNotWhileSignedOut, err)
		}
	}
	return nil
}

// Uninstall stops and removes s.
func Uninstall(s Spec) error {
	path, err := unitPath(s)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := systemctl(s, "disable", "--now", s.Name); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove unit: %w", err)
	}
	return systemctl(s, "daemon-reload")
}

// Stop stops s.
func Stop(s Spec) error { return systemctl(s, "stop", s.Name) }

// Restart restarts s.
func Restart(s Spec) error { return systemctl(s, "restart", s.Name) }

// Query reports whether s is installed and running.
func Query(s Spec) (Status, error) {
	path, err := unitPath(s)
	if err != nil {
		return Status{}, err
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return Status{}, nil
	} else if err != nil {
		return Status{}, fmt.Errorf("unit: %w", err)
	}
	args := []string{"is-active", "--quiet", s.Name}
	if s.Scope == User {
		args = append([]string{"--user"}, args...)
	}
	// is-active answers in its exit status alone.
	running := exec.Command("systemctl", args...).Run() == nil
	return Status{Installed: true, Running: running}, nil
}

// CreateAccount makes the system account a system service runs as, with
// home as its home directory, unless it exists.
func CreateAccount(name, home string) error {
	if _, err := user.Lookup(name); err == nil {
		return nil
	} else if !errors.As(err, new(user.UnknownUserError)) {
		return fmt.Errorf("look up %s: %w", name, err)
	}
	return run("useradd", "--system", "--user-group", "--home-dir", home, "--no-create-home",
		"--shell", "/usr/sbin/nologin", "--comment", "Cog server", name)
}

// OpenFirewall opens ports in ufw or firewalld, whichever is running. With
// neither there is nothing to open.
func OpenFirewall(ports []Port) error {
	switch {
	case have("ufw") && ufwActive():
		for _, p := range ports {
			if err := run("ufw", "allow", fmt.Sprintf("%d/%s", p.Number, p.Proto), "comment", "Cog "+p.Name); err != nil {
				return err
			}
		}
	case have("firewall-cmd") && exec.Command("firewall-cmd", "--state").Run() == nil:
		for _, p := range ports {
			if err := run("firewall-cmd", "--permanent", fmt.Sprintf("--add-port=%d/%s", p.Number, p.Proto)); err != nil {
				return err
			}
		}
		return run("firewall-cmd", "--reload")
	}
	return nil
}

func ufwActive() bool {
	out, err := exec.Command("ufw", "status").Output()
	return err == nil && strings.Contains(string(out), "Status: active")
}

// have reports whether a program is on PATH.
func have(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
