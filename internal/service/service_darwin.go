package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultAccount is the account a system service runs as; macOS keeps
// daemons' accounts underscored.
const DefaultAccount = "_cog"

// label is the launchd job's label.
func label(s Spec) string { return "us.duckfam." + s.Name }

func plistPath(s Spec) (string, error) {
	if s.Scope == System {
		return filepath.Join("/Library/LaunchDaemons", label(s)+".plist"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", label(s)+".plist"), nil
}

// domain is the launchd domain s runs in.
func domain(s Spec) string {
	if s.Scope == System {
		return "system"
	}
	return "gui/" + strconv.Itoa(os.Getuid())
}

// Plist is the launchd job for s.
func Plist(s Spec) string {
	var b bytes.Buffer
	esc := func(v string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(v)) // a bytes.Buffer does not fail
		return e.String()
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", esc(label(s)))
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range append([]string{s.Exe}, s.Args...) {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", esc(a))
	}
	b.WriteString("\t</array>\n")
	fmt.Fprintf(&b, "\t<key>WorkingDirectory</key>\n\t<string>%s</string>\n", esc(s.Dir))
	// KeepAlive starts it again whenever it exits, which is how an
	// update's restart takes effect.
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n\t<key>KeepAlive</key>\n\t<true/>\n")
	if s.Scope == System {
		fmt.Fprintf(&b, "\t<key>UserName</key>\n\t<string>%s</string>\n", esc(s.Account))
	}
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n", esc(filepath.Join(s.Dir, "cog.log")))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// Install writes s's job, loads it and starts it.
func Install(s Spec) error {
	path, err := plistPath(s)
	if err != nil {
		return err
	}
	st, err := Query(s)
	if err != nil {
		return err
	}
	if st.Installed {
		if err := run("launchctl", "bootout", domain(s)+"/"+label(s)); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("launchd directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(Plist(s)), 0o644); err != nil {
		return fmt.Errorf("write launchd job: %w", err)
	}
	return run("launchctl", "bootstrap", domain(s), path)
}

// Uninstall stops and removes s.
func Uninstall(s Spec) error {
	path, err := plistPath(s)
	if err != nil {
		return err
	}
	st, err := Query(s)
	if err != nil {
		return err
	}
	if st.Installed {
		if err := run("launchctl", "bootout", domain(s)+"/"+label(s)); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove launchd job: %w", err)
	}
	return nil
}

// Stop stops s until it is next loaded: with KeepAlive, killing it would
// only start it again.
func Stop(s Spec) error {
	return run("launchctl", "bootout", domain(s)+"/"+label(s))
}

// Restart restarts s, loading it again if Stop unloaded it.
func Restart(s Spec) error {
	st, err := Query(s)
	if err != nil {
		return err
	}
	if !st.Installed {
		path, err := plistPath(s)
		if err != nil {
			return err
		}
		return run("launchctl", "bootstrap", domain(s), path)
	}
	return run("launchctl", "kickstart", "-k", domain(s)+"/"+label(s))
}

// Query reports whether s is loaded and running.
func Query(s Spec) (Status, error) {
	out, err := exec.Command("launchctl", "print", domain(s)+"/"+label(s)).CombinedOutput()
	if err != nil {
		// print fails for a job that is not loaded.
		return Status{}, nil
	}
	return Status{Installed: true, Running: strings.Contains(string(out), "state = running")}, nil
}

// CreateAccount makes the hidden account a system service runs as, unless
// it exists.
func CreateAccount(name, home string) error {
	if _, err := user.Lookup(name); err == nil {
		return nil
	} else if !errors.As(err, new(user.UnknownUserError)) {
		return fmt.Errorf("look up %s: %w", name, err)
	}
	id, err := freeID()
	if err != nil {
		return err
	}
	group := "/Groups/" + name
	acct := "/Users/" + name
	for _, args := range [][]string{
		{".", "-create", group},
		{".", "-create", group, "PrimaryGroupID", id},
		{".", "-create", group, "RealName", "Cog server"},
		{".", "-create", acct},
		{".", "-create", acct, "UniqueID", id},
		{".", "-create", acct, "PrimaryGroupID", id},
		{".", "-create", acct, "UserShell", "/usr/bin/false"},
		{".", "-create", acct, "NFSHomeDirectory", home},
		{".", "-create", acct, "RealName", "Cog server"},
		{".", "-create", acct, "IsHidden", "1"},
	} {
		if err := run("dscl", args...); err != nil {
			return err
		}
	}
	return nil
}

// freeID is a user and group ID below 500 that neither uses, the range
// macOS keeps for services.
func freeID() (string, error) {
	used := map[string]bool{}
	for _, kind := range [][]string{{"/Users", "UniqueID"}, {"/Groups", "PrimaryGroupID"}} {
		out, err := exec.Command("dscl", ".", "-list", kind[0], kind[1]).Output()
		if err != nil {
			return "", fmt.Errorf("list %s: %w", kind[0], err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) == 2 {
				used[f[1]] = true
			}
		}
	}
	for id := 450; id > 300; id-- {
		if s := strconv.Itoa(id); !used[s] {
			return s, nil
		}
	}
	return "", errors.New("no free account ID between 300 and 450")
}

// OpenFirewall does nothing: the application firewall asks the first time
// the server listens, and is off by default.
func OpenFirewall([]Port) error { return nil }
