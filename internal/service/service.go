// Package service installs Cog as a service of the operating system: a
// systemd unit on Linux, a launchd job on macOS, a Windows service or, for
// one user without administrator rights, a scheduled task at sign-in.
//
// A system service runs as an account of its own that owns the install
// directory, binary included, so that the server can update itself. A
// user service runs as the user who set it up, from a directory in their
// home, and starts when they sign in: what a personal computer whose
// router forwards ports to it wants.
package service

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Scope is who a service runs as.
type Scope string

const (
	// System runs at boot as an account of its own. Installing it needs
	// administrator rights.
	System Scope = "system"
	// User runs as the user who installed it, from when they sign in.
	User Scope = "user"
)

// SupervisedEnv is set to "1" in the environment of a Cog run by a service
// setup installed, whose manager starts it again when it exits. An update
// exits for it to; a Cog without it restarts itself.
const SupervisedEnv = "COG_SUPERVISED"

// Spec describes a service.
type Spec struct {
	Name        string // "cog"
	DisplayName string // "Cog"
	Description string
	Scope       Scope
	Exe         string   // the binary
	Args        []string // its arguments: serve --config ...
	Dir         string   // working directory, which the account owns
	// Account is the account a system service runs as, which
	// DefaultAccount names; ignored for a user service.
	Account string
}

// ErrUnsupported is a service operation this platform has no way to do.
var ErrUnsupported = errors.New("services are not supported on this platform")

// ErrNotWhileSignedOut is a user service that is installed and running
// but will stop when its user signs out.
var ErrNotWhileSignedOut = errors.New("the service runs only while its user is signed in")

// Status is whether a service is installed and running.
type Status struct {
	Installed bool
	Running   bool
}

// run runs a program setup needs, with its output in the error.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Port is a port to open in the firewall.
type Port struct {
	Number int
	Proto  string // "tcp"
	Name   string // what it is for
}

// giveDirArgs are icacls's arguments for Windows' GiveDir. The install
// directory is under C:\ProgramData, whose permissions let every local
// user read what is in it, and add files to it: the server's database,
// TLS keys, DNS credentials and everyone's files. So dir stops inheriting
// them (/inheritance:r) and grants, in their place, full control to
// SYSTEM (S-1-5-18) and Administrators (S-1-5-32-544), named by SID so
// that a Windows in another language finds them, and change to the
// service's account. The grants are inheritable, and Windows hands them
// down to what is already in dir, in place of what it inherited.
func giveDirArgs(dir, account string) []string {
	return []string{dir, "/inheritance:r",
		"/grant:r", "*S-1-5-18:(OI)(CI)F",
		"/grant:r", "*S-1-5-32-544:(OI)(CI)F",
		"/grant:r", account + ":(OI)(CI)M",
		"/Q"}
}
