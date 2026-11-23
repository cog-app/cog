//go:build !windows

package update

import (
	"fmt"
	"os"
	"syscall"

	"git.duckfam.us/jj-software/cog/internal/service"
)

// renameOverRunning: a running program's file can be replaced by a rename.
const renameOverRunning = true

// Supervised reports whether a service manager restarts this process when
// it exits: the systemd unit or launchd job setup installed, which say so
// in the environment. Other variables those managers set (INVOCATION_ID,
// XPC_SERVICE_NAME) are inherited by shells in terminals they run too.
func Supervised() bool {
	return os.Getenv(service.SupervisedEnv) == "1"
}

// Restart replaces this process with exe run with the same arguments, or
// exits for the service manager to start it again. It returns only on
// failure.
func Restart(exe string) error {
	if Supervised() {
		os.Exit(ExitRestart)
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		return fmt.Errorf("restart %s: %w", exe, err)
	}
	return nil
}
