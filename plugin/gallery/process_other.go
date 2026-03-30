//go:build !linux

package gallery

import "context"

// useSubprocess reports whether the scan worker should run as a subprocess.
// On non-Linux platforms, always run in-process.
func useSubprocess() bool { return false }

// setNice is a no-op on non-Linux platforms.
func setNice(pid, niceness int) {}

// killProcess kills a process by PID.
func killProcess(pid int) {}

// rssWatchdog is a no-op on non-Linux (no /proc filesystem).
func (p *Plugin) rssWatchdog(ctx context.Context, pid int, client scannerClient, done <-chan struct{}) {
	select {
	case <-ctx.Done():
	case <-done:
	}
}

// readRSS returns 0 on non-Linux (no /proc filesystem).
func readRSS(pid int) uint64 { return 0 }
