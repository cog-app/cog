//go:build !windows

package update

import (
	"fmt"
	"os"
	"syscall"
)

// renameOverRunning: a running program's file can be replaced by a rename.
const renameOverRunning = true

// Supervised reports whether a service manager restarts this process when
// it exits: systemd (Restart=always, which sets INVOCATION_ID) or launchd
// (KeepAlive, which sets XPC_SERVICE_NAME).
func Supervised() bool {
	if os.Getenv("INVOCATION_ID") != "" {
		return true
	}
	name := os.Getenv("XPC_SERVICE_NAME")
	return name != "" && name != "0"
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
