// Dev tool for Cog. Usage: go tool dev
//
// Resets tmp/dev state, creates a test user, and runs cog with
// request logging enabled. Ctrl-C to stop.
package main

import (
	"fmt"
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
	devDir := filepath.Join("tmp", "dev")

	// Wipe and recreate dev state
	os.RemoveAll(devDir)
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	dbPath := filepath.Join(devDir, "system.db")

	// Build cog
	log.Println("Building cog...")
	build := exec.Command("go", "build", "-o", filepath.Join(devDir, "cog"), "./cmd/cog")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		log.Fatalf("build failed: %v", err)
	}

	bin := filepath.Join(devDir, "cog")

	// Create test user
	log.Printf("Creating test user %s:%s", testUser, testPass)
	create := exec.Command(bin, "--db", dbPath, "--create-user", testUser+":"+testPass)
	create.Stdout = os.Stdout
	create.Stderr = os.Stderr
	if err := create.Run(); err != nil {
		log.Fatalf("create-user failed: %v", err)
	}

	// Run server with request logging
	hostname, _ := os.Hostname()
	baseURL := fmt.Sprintf("http://%s%s", hostname, addr)
	log.Printf("Starting cog on %s (base-url %s, log-requests on)", addr, baseURL)
	srv := exec.Command(bin,
		"--db", dbPath,
		"--addr", addr,
		"--base-url", baseURL,
		"--log-requests",
	)
	srv.Stdout = os.Stdout
	srv.Stderr = os.Stderr

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
		// Exit with the child's exit code if possible
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		log.Fatal(err)
	}
}
