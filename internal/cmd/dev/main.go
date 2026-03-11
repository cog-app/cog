// Dev tool for Cog. Usage: go tool dev [-reset] [-watch] [-shell]
//
// Builds cog with seed support, ensures a test user exists, and runs with
// request logging and COG_SEED=true. Logs are teed to tmp/dev/cog.log.
// Use -reset to wipe state first. Use -watch for live reload via templ proxy.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

const (
	testUser  = "admin"
	testPass  = "admin"
	proxyAddr = ":8580"
	watchAddr = ":8581"
)

func main() {
	reset := flag.Bool("reset", false, "wipe tmp/dev state before starting")
	logRequests := flag.Bool("log", false, "instructs the service to log requests")
	stale := flag.Bool("stale", false, "skip the build step to use the previous, stale build")
	watch := flag.Bool("watch", false, "live reload via templ proxy (templ on :8580, cog on :8581)")
	shell := flag.Bool("shell", false, "run cog shell --user admin instead of the server")
	rebuild := flag.Int("rebuild", 0, "internal: rebuild binary and signal parent")
	flag.Parse()

	devDir := filepath.Join("tmp", "dev")

	if *rebuild != 0 {
		runRebuild(filepath.Join(devDir, "cog"), *rebuild)
		return
	}

	if *reset {
		log.Println("Resetting dev state...")
		os.RemoveAll(devDir)
	}

	if err := os.MkdirAll(devDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	dbPath := filepath.Join(devDir, "system.db")
	bin := filepath.Join(devDir, "cog")

	if *shell {
		runShell(bin, dbPath, devDir, *stale)
	} else if *watch {
		runWatch(bin, dbPath, devDir, *logRequests, *stale)
	} else {
		runOnce(bin, dbPath, devDir, *logRequests, *stale)
	}
}

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
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		log.Fatal(err)
	}
}

// runOnce is the original one-shot generate → build → run cycle.
func runOnce(bin, dbPath, devDir string, logRequests, stale bool) {
	if !stale {
		generate()
		build(bin)
	}

	seedUser(bin, dbPath, devDir)

	teeOut, teeErr, cleanup := openLog(devDir)
	defer cleanup()

	addr := proxyAddr // non-watch uses :8580 directly
	srv := startServer(bin, dbPath, devDir, addr, logRequests, teeOut, teeErr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		srv.Process.Signal(syscall.SIGTERM)
	}()

	if err := srv.Wait(); err != nil {
		log.Printf("cog exited: %v", err)
		if exit, ok := err.(*exec.ExitError); ok {
			if stderr := exit.Stderr; len(stderr) > 0 {
				log.Printf("cog stderr: %s", stderr)
			}
			os.Exit(exit.ExitCode())
		}
		log.Fatal(err)
	}
}

// runWatch starts cog on :8581 and templ proxy on :8580 with live reload.
func runWatch(bin, dbPath, devDir string, logRequests, stale bool) {
	if !stale {
		build(bin)
	}

	seedUser(bin, dbPath, devDir)

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

func generate() {
	log.Println("Generate code...")
	cmd := exec.Command("go", "generate", "./...")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("templ generate failed: %v", err)
	}
}

func build(bin string) {
	log.Println("Building cog (with seed support)...")
	cmd := exec.Command("go", "build", "-tags", "seed", "-o", bin, "./cmd/cog")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("build failed: %v", err)
	}
}

func seedUser(bin, dbPath, devDir string) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		log.Printf("Creating test user %s:%s (admin)", testUser, testPass)
		cmd := exec.Command(bin, "user", "create", "--admin", testUser, testPass)
		cmd.Env = append(os.Environ(),
			"COG_DB="+dbPath,
			"COG_DATA_DIR="+filepath.Join(devDir, "data"),
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("user create failed: %v", err)
		}
	} else {
		log.Printf("Using existing DB (user: %s)", testUser)
	}
}

func openLog(devDir string) (io.Writer, io.Writer, func()) {
	logPath := filepath.Join(devDir, "cog.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		log.Fatalf("open log file: %v", err)
	}
	log.Printf("Logs: %s", logPath)
	teeOut := io.MultiWriter(os.Stdout, logFile)
	teeErr := io.MultiWriter(os.Stderr, logFile)
	return teeOut, teeErr, func() { logFile.Close() }
}

func startServer(bin, dbPath, devDir, addr string, logRequests bool, teeOut, teeErr io.Writer) *exec.Cmd {
	hostname, _ := os.Hostname()
	baseURL := fmt.Sprintf("http://%s%s", hostname, addr)
	log.Printf("Starting cog on %s (base-url %s)", addr, baseURL)

	srv := exec.Command(bin, "serve")
	srv.Env = append(os.Environ(),
		"COG_DB="+dbPath,
		"COG_ADDR="+addr,
		"COG_DATA_DIR="+filepath.Join(devDir, "data"),
		"COG_BASE_URL="+baseURL,
		"COG_SEED=true",
	)
	if logRequests {
		srv.Env = append(srv.Env, "COG_LOG_REQUESTS=true")
	}
	srv.Stdout = teeOut
	srv.Stderr = teeErr

	if err := srv.Start(); err != nil {
		log.Fatalf("start failed: %v", err)
	}
	return srv
}
