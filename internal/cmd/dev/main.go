// Dev tool for Cog. Usage: go tool dev [-reset]
//
// Builds cog with seed support, ensures a test user exists, and runs with
// request logging and COG_SEED=true. Logs are teed to tmp/dev/cog.log.
// Use -reset to wipe state first.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

const (
	testUser = "admin"
	testPass = "admin"
	addr     = ":8580"
)

func main() {
	reset := flag.Bool("reset", false, "wipe tmp/dev state before starting")
	logRequests := flag.Bool("log", false, "instructs the service to log requests")
	stale := flag.Bool("stale", false, "skip the build step to use the previous, stale build")
	flag.Parse()

	devDir := filepath.Join("tmp", "dev")

	if *reset {
		log.Println("Resetting dev state...")
		os.RemoveAll(devDir)
	}

	if err := os.MkdirAll(devDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	dbPath := filepath.Join(devDir, "system.db")

	if !*stale {
		// Generate templ code
		log.Println("Generate code...")
		tgen := exec.Command("go", "generate", "./...")
		tgen.Stdout = os.Stdout
		tgen.Stderr = os.Stderr
		if err := tgen.Run(); err != nil {
			log.Fatalf("templ generate failed: %v", err)
		}

		// Build cog with seed tag
		log.Println("Building cog (with seed support)...")
		build := exec.Command("go", "build", "-tags", "seed", "-o", filepath.Join(devDir, "cog"), "./cmd/cog")
		build.Stdout = os.Stdout
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			log.Fatalf("build failed: %v", err)
		}
	}

	bin := filepath.Join(devDir, "cog")

	// Idempotent seed: only create if DB doesn't exist yet
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		log.Printf("Creating test user %s:%s (admin)", testUser, testPass)
		create := exec.Command(bin, "user", "create", "--admin", testUser, testPass)
		create.Env = append(os.Environ(),
			"COG_DB="+dbPath,
			"COG_DATA_DIR="+filepath.Join(devDir, "data"),
		)
		create.Stdout = os.Stdout
		create.Stderr = os.Stderr
		if err := create.Run(); err != nil {
			log.Fatalf("user create failed: %v", err)
		}
	} else {
		log.Printf("Using existing DB (user: %s)", testUser)
	}

	// Open log file for tee
	logPath := filepath.Join(devDir, "cog.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		log.Fatalf("open log file: %v", err)
	}
	defer logFile.Close()

	teeOut := io.MultiWriter(os.Stdout, logFile)
	teeErr := io.MultiWriter(os.Stderr, logFile)

	// Run server with request logging and seeding
	hostname, _ := os.Hostname()
	baseURL := fmt.Sprintf("http://%s%s", hostname, addr)
	log.Printf("Starting cog on %s (base-url %s, log-requests on, seed on)", addr, baseURL)
	log.Printf("Logs: %s", logPath)
	srv := exec.Command(bin, "serve")
	srv.Env = append(os.Environ(),
		"COG_DB="+dbPath,
		"COG_ADDR="+addr,
		"COG_DATA_DIR="+filepath.Join(devDir, "data"),
		"COG_BASE_URL="+baseURL,
		"COG_SEED=true",
	)
	if *logRequests {
		srv.Env = append(srv.Env, "COG_LOG_REQUESTS=true")
	}
	srv.Stdout = teeOut
	srv.Stderr = teeErr

	if err := srv.Start(); err != nil {
		log.Fatalf("start failed: %v", err)
	}

	// Forward signals so Ctrl-C cleanly stops the child
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
