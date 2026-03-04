// Dev tool for Cog. Usage: go tool dev [-reset]
//
// Builds cog, ensures a test user exists, and runs with request logging.
// Logs are teed to tmp/dev/cog.log. Use -reset to wipe state first.
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

	// Generate templ code
	log.Println("Generating templ...")
	tgen := exec.Command("go", "tool", "templ", "generate")
	tgen.Stdout = os.Stdout
	tgen.Stderr = os.Stderr
	if err := tgen.Run(); err != nil {
		log.Fatalf("templ generate failed: %v", err)
	}

	// Build cog
	log.Println("Building cog...")
	build := exec.Command("go", "build", "-o", filepath.Join(devDir, "cog"), "./cmd/cog")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		log.Fatalf("build failed: %v", err)
	}

	bin := filepath.Join(devDir, "cog")

	// Idempotent seed: only create if DB doesn't exist yet
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		log.Printf("Creating test user %s:%s (admin)", testUser, testPass)
		create := exec.Command(bin, "--db", dbPath, "--create-user", testUser+":"+testPass)
		create.Stdout = os.Stdout
		create.Stderr = os.Stderr
		if err := create.Run(); err != nil {
			log.Fatalf("create-user failed: %v", err)
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

	// Run server with request logging
	hostname, _ := os.Hostname()
	baseURL := fmt.Sprintf("http://%s%s", hostname, addr)
	log.Printf("Starting cog on %s (base-url %s, log-requests on)", addr, baseURL)
	log.Printf("Logs: %s", logPath)
	dataDir := filepath.Join(devDir, "data")
	srv := exec.Command(bin,
		"--db", dbPath,
		"--addr", addr,
		"--base-url", baseURL,
		"--data-dir", dataDir,
		"--log-requests",
	)
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
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		log.Fatal(err)
	}
}
