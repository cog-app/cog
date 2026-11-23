//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/windows/svc"
)

// renameOverRunning: Windows will not replace a running program's file.
const renameOverRunning = false

// Supervised reports whether the service manager restarts this process
// when it exits: setup configures a Windows service to restart on failure.
func Supervised() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// Restart starts exe with the same arguments and exits, or exits for the
// service manager to start it again. It returns only on failure.
func Restart(exe string) error {
	if Supervised() {
		os.Exit(ExitRestart)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart %s: %w", exe, err)
	}
	os.Exit(0)
	return nil
}
