//go:build linux

package gallery

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// useSubprocess reports whether the scan worker should run as a subprocess.
// On Linux this is the default; set COG_NO_WORKER=1 to run in-process.
func useSubprocess() bool {
	return os.Getenv("COG_NO_WORKER") == ""
}

// lowerOwnPriority gives every thread of this process the lowest CPU
// priority and the lowest best-effort I/O priority, for a worker that shares
// a machine. Linux keeps both per thread, so setting the process's main
// thread from outside left the threads it had already started at normal
// priority; threads started later inherit from the thread that starts them.
// A failure only costs the server some priority over the worker, so it is
// logged, not returned.
func lowerOwnPriority() {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		slog.Warn("lower worker priority", "err", err)
		return
	}
	const ioprioWhoProcess, ioprioClassBE, ioprioClassShift = 1, 2, 13
	for _, t := range tasks {
		tid, err := strconv.Atoi(t.Name())
		if err != nil {
			continue
		}
		if err := syscall.Setpriority(syscall.PRIO_PROCESS, tid, 19); err != nil {
			slog.Warn("lower worker cpu priority", "tid", tid, "err", err)
		}
		if _, _, errno := syscall.Syscall(syscall.SYS_IOPRIO_SET, ioprioWhoProcess, uintptr(tid), ioprioClassBE<<ioprioClassShift|7); errno != 0 {
			slog.Warn("lower worker io priority", "tid", tid, "err", errno)
		}
	}
}

// killProcess sends SIGKILL to a process.
func killProcess(pid int) {
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		slog.Error("kill worker", "pid", pid, "err", err)
	}
}

// rssWatchdog monitors the worker's RSS and triggers memory release or kill.
func (p *Plugin) rssWatchdog(ctx context.Context, pid int, client scannerClient, done <-chan struct{}) {
	maxMB := uint64(2048)
	if sysDB, err := p.SystemDB(); err != nil {
		p.log.Warn("system db; using default worker RSS limit", "err", err)
	} else {
		maxMB = uint64(SettingWorkerMaxRSS.MustGet(sysDB))
	}
	maxBytes := maxMB * 1024 * 1024

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			rss := readRSS(pid)
			if rss == 0 || rss <= maxBytes {
				continue
			}
			p.log.Warn("scan worker RSS exceeds limit",
				"rss", formatBytes(rss), "limit", formatBytes(maxBytes))
			if err := client.ReleaseMemory(); err != nil {
				p.log.Warn("ask scan worker to release memory", "err", err)
			}

			// Give it time to free memory.
			time.Sleep(15 * time.Second)

			rss = readRSS(pid)
			if rss > maxBytes {
				p.log.Error("scan worker memory still high after release, killing",
					"rss", formatBytes(rss))
				killProcess(pid)
				return
			}
			p.log.Info("scan worker memory recovered", "rss", formatBytes(rss))
		}
	}
}

// readRSS returns the resident set size of a process in bytes.
func readRSS(pid int) uint64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		slog.Error("parse worker statm", "pid", pid, "statm", string(data), "err", err)
		return 0
	}
	return pages * uint64(os.Getpagesize())
}
