//go:build !unix

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// runRebuild is invoked by templ's -cmd. On non-Unix platforms, it builds
// but cannot signal the parent via SIGUSR1.
func runRebuild(bin string, ppid int) {
	build(bin)
	log.Println("Rebuild complete (watch mode signaling not supported on this platform)")
}

// runShell builds cog and runs `cog shell --user admin` as a subprocess.
// On Unix, syscall.Exec replaces the process for direct terminal access;
// here we fall back to exec.Command.
func runShell(bin, dbPath, devDir string, stale bool) {
	if !stale {
		generate()
		build(bin)
	}

	seedUser(bin, dbPath, devDir)

	cmd := exec.Command(bin, "shell", "--user", testUser)
	cmd.Env = append(os.Environ(),
		"COG_DB="+dbPath,
		"COG_DATA_DIR="+filepath.Join(devDir, "data"),
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("shell: %v", err)
	}
}

// runWatch is not supported on non-Unix platforms because it requires
// SIGUSR1 for rebuild signaling between templ and the dev tool.
func runWatch(bin, dbPath, devDir string, logRequests, stale bool) {
	log.Fatal("watch mode requires Unix (SIGUSR1 for rebuild signaling)")
}
