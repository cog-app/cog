package webtest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// Browser slots limit how many test binaries drive Chrome at once. go test
// ./... runs a binary per package in parallel, each with its own Chrome; on a
// busy machine pages then take longer than any reasonable action timeout.
// A binary holds one slot while its shared browser is running. Slots are
// lock files in the temp directory, so they work across processes, and the
// operating system frees a slot when its holder exits, however it exits.

// slots returns how many binaries may run browsers at once: WEBTEST_PARALLEL,
// or a quarter of the CPUs.
func slots() int {
	if n, err := strconv.Atoi(os.Getenv("WEBTEST_PARALLEL")); err == nil && n > 0 {
		return n
	}
	return max(1, runtime.NumCPU()/4)
}

// slotWait is how often a waiting binary tries the slots again.
const slotWait = 100 * time.Millisecond

// acquireSlot blocks until a slot is free and returns its release.
func acquireSlot() (func(), error) {
	dir := filepath.Join(os.TempDir(), "cog-webtest-slots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("browser slots: %w", err)
	}
	n := slots()
	for {
		for i := range n {
			release, ok, err := tryLock(filepath.Join(dir, fmt.Sprintf("slot-%d.lock", i)))
			if err != nil {
				return nil, fmt.Errorf("browser slot %d: %w", i, err)
			}
			if ok {
				return release, nil
			}
		}
		time.Sleep(slotWait)
	}
}
