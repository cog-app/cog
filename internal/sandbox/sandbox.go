// Package sandbox runs the programs that read what users upload and feeds
// supply — converters, decoders, OCR, yt-dlp — where a bug in them reaches
// as little as possible.
//
// These programs parse hostile input, and their parsers have a long history
// of bugs; some also follow links inside a document to other files on the
// machine and draw them into the preview the uploader gets back. Run as the
// server, either reads every user's files and the master secret. With
// bubblewrap (bwrap) installed and permitted to create namespaces, each runs
// with the system's programs and libraries read-only, only the files it was
// given, an empty /tmp, no other processes, a clean environment and, unless
// it fetches, no network. Without it, programs run as before, and Status says
// why.
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Spec is what a sandboxed program may reach beyond the system's own files.
type Spec struct {
	// Read are files and directories it may read.
	Read []string
	// Write are directories it may read and write. Each must exist.
	Write []string
	// Network lets it make connections, for a program that fetches.
	Network bool
	// Env are variables set for it, as "KEY=value", on top of the minimal
	// environment the sandbox gives every program.
	Env []string
}

// systemDirs hold programs, libraries and their configuration: what any of
// the programs needs, and nothing of the server's data.
var systemDirs = []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/opt"}

// probe finds bwrap and checks it can create a sandbox: a kernel or a
// container (Docker's default seccomp profile) may forbid the namespaces it
// needs, and finding that out on every conversion would be slow and noisy.
//
// COG_SANDBOX=off turns sandboxing off, for a host where it breaks a
// program, and for the test harness, whose pages should not depend on
// whether the machine running them has bwrap.
var probe = sync.OnceValues(func() (string, error) {
	if os.Getenv("COG_SANDBOX") == "off" {
		return "", fmt.Errorf("turned off by COG_SANDBOX=off")
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return "", fmt.Errorf("bwrap is not installed")
	}
	tr, err := exec.LookPath("true")
	if err != nil {
		return "", fmt.Errorf("check bwrap: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bwrap, argsFor(Spec{}, tr)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("bwrap cannot create a sandbox here: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return bwrap, nil
})

// Status reports nil when programs run sandboxed, or why they do not.
func Status() error {
	_, err := probe()
	return err
}

// CommandContext returns the command that runs name with args, confined to
// spec when sandboxing works. It is exec.CommandContext otherwise. The
// caller must not set Env, which the sandbox replaces.
func CommandContext(ctx context.Context, spec Spec, name string, args ...string) *exec.Cmd {
	unsandboxed := func() *exec.Cmd {
		cmd := exec.CommandContext(ctx, name, args...)
		if len(spec.Env) > 0 {
			cmd.Env = append(os.Environ(), spec.Env...)
		}
		return cmd
	}
	bwrap, err := probe()
	if err != nil {
		return unsandboxed()
	}
	prog, err := exec.LookPath(name)
	if err != nil {
		// Let Start report the missing program as it would unsandboxed.
		return unsandboxed()
	}
	if prog, err = filepath.Abs(prog); err != nil {
		return unsandboxed()
	}
	return exec.CommandContext(ctx, bwrap, append(argsFor(spec, prog), args...)...)
}

// argsFor builds bwrap's arguments up to and including prog; the program's
// own arguments follow.
func argsFor(spec Spec, prog string) []string {
	a := []string{
		"--unshare-all", "--die-with-parent", "--new-session",
		"--clearenv",
		"--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin",
		"--setenv", "HOME", "/tmp",
		"--setenv", "TMPDIR", "/tmp",
		"--setenv", "LANG", "C.UTF-8",
	}
	for _, kv := range spec.Env {
		k, v, _ := strings.Cut(kv, "=")
		a = append(a, "--setenv", k, v)
	}
	if spec.Network {
		a = append(a, "--share-net")
	}
	for _, d := range systemDirs {
		a = append(a, "--ro-bind-try", d, d)
	}
	a = append(a, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp")
	// A program installed elsewhere, such as in a virtualenv, brings the
	// tree it was installed into.
	if !underSystem(prog) {
		root := filepath.Dir(filepath.Dir(prog))
		a = append(a, "--ro-bind", root, root)
	}
	// Binds come after the tmpfs, so one under /tmp lands on top of it.
	for _, p := range spec.Read {
		p = abs(p)
		a = append(a, "--ro-bind", p, p)
	}
	for _, p := range spec.Write {
		p = abs(p)
		a = append(a, "--bind", p, p)
	}
	// The working directory exists, empty, so a relative path in the
	// program's arguments names the file bound at its absolute path.
	cwd := abs(".")
	return append(a, "--dir", cwd, "--chdir", cwd, "--", prog)
}

func underSystem(p string) bool {
	for _, d := range systemDirs {
		if strings.HasPrefix(p, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// abs makes a relative path, such as one under a relative data dir,
// absolute: binds are mounted at absolute paths. Abs fails only when the
// working directory is gone, and then bwrap refuses the relative path.
func abs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
