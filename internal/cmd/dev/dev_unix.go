//go:build unix

package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

// runRebuild is invoked by templ's -cmd. It builds the binary, signals the
// parent dev process to restart, and notifies the templ proxy for browser
// reload (silently ignoring errors if the proxy isn't up yet).
func runRebuild(bin string, ppid int) {
	build(bin)

	if ppid != 0 {
		p, err := os.FindProcess(ppid)
		if err == nil {
			p.Signal(syscall.SIGUSR1)
		}
	}

	// Notify templ proxy for browser reload; ignore errors (proxy may
	// not be listening yet on the initial cycle).
	resp, err := http.Post("http://127.0.0.1:8580/_templ/reload/events", "", nil)
	if err == nil {
		resp.Body.Close()
	}
}

// runShell builds cog and runs `cog shell --user admin`.
// We use syscall.Exec to replace this process so the shell gets direct
// terminal access (readline needs the real tty, not a pipe).
func runShell(bin, dbPath, devDir string, stale bool) {
	if !stale {
		generate()
		build(bin)
	}

	seedUser(bin, dbPath, devDir)

	absBin, err := filepath.Abs(bin)
	if err != nil {
		log.Fatalf("abs path: %v", err)
	}

	env := append(os.Environ(),
		"COG_DB="+dbPath,
		"COG_DATA_DIR="+filepath.Join(devDir, "data"),
	)

	if err := syscall.Exec(absBin, []string{absBin, "shell", "--user", testUser}, env); err != nil {
		log.Fatalf("exec: %v", err)
	}
}

// runWatch starts cog on :8581 and templ proxy on :8580 with live reload.
func runWatch(bin, dbPath, devDir string, logRequests, stale bool) {
	if !stale {
		build(bin)
	}

	seedUser(bin, dbPath, devDir)

	mail := startMaildev(dbPath)
	defer mail.Close()

	teeOut, teeErr, cleanup := openLog(devDir)
	defer cleanup()

	// templ's -cmd calls back into this tool to rebuild and signal us.
	rebuildCmd := fmt.Sprintf("go tool dev -rebuild %d", os.Getpid())

	// Start templ watcher + proxy first — its initial generate cycle
	// fires -cmd (which builds and sends SIGUSR1). We wait for that
	// signal before starting cog so the proxy is already listening.
	log.Printf("Starting templ watcher (proxy on %s → cog on %s)", proxyAddr, watchAddr)
	templ := exec.Command("go", "tool", "templ", "generate",
		"--watch",
		"--proxy=http://localhost"+watchAddr,
		"--proxyport=8580",
		"-open-browser=false",
		"-cmd", rebuildCmd,
	)
	templ.Stdout = os.Stdout
	templ.Stderr = os.Stderr
	if err := templ.Start(); err != nil {
		log.Fatalf("templ start failed: %v", err)
	}

	// Channel for SIGUSR1 (rebuild complete) and SIGINT/SIGTERM (shutdown)
	restart := make(chan os.Signal, 1)
	quit := make(chan os.Signal, 1)
	signal.Notify(restart, syscall.SIGUSR1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Wait for templ's initial build before starting cog
	log.Println("Waiting for initial templ generate + build...")
	select {
	case <-restart:
		log.Println("Initial build complete, starting cog...")
	case <-quit:
		templ.Process.Signal(syscall.SIGTERM)
		templ.Wait()
		return
	}

	srv := startServer(bin, dbPath, devDir, watchAddr, logRequests, teeOut, teeErr)
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Wait() }()

	for {
		select {
		case <-restart:
			log.Println("Rebuild complete, restarting cog...")
			srv.Process.Signal(syscall.SIGTERM)
			<-srvDone // wait for old server to exit
			srv = startServer(bin, dbPath, devDir, watchAddr, logRequests, teeOut, teeErr)
			srvDone = make(chan error, 1)
			go func() { srvDone <- srv.Wait() }()

		case err := <-srvDone:
			log.Printf("cog crashed: %v (waiting for rebuild...)", err)
			// Wait for either a rebuild signal or shutdown
			select {
			case <-restart:
				log.Println("Rebuild complete, restarting cog...")
				srv = startServer(bin, dbPath, devDir, watchAddr, logRequests, teeOut, teeErr)
				srvDone = make(chan error, 1)
				go func() { srvDone <- srv.Wait() }()
			case <-quit:
				templ.Process.Signal(syscall.SIGTERM)
				templ.Wait()
				return
			}

		case <-quit:
			log.Println("Shutting down...")
			srv.Process.Signal(syscall.SIGTERM)
			<-srvDone
			templ.Process.Signal(syscall.SIGTERM)
			templ.Wait()
			return
		}
	}
}
