// Dev tool for Cog. Usage: go tool dev [-reset]
//
// Builds cog, ensures a test user exists, and runs with request logging.
// Logs are teed to tmp/dev/cog.log. Use -reset to wipe state first.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"math"
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
		create := exec.Command(bin, "--db", dbPath, "--data-dir", filepath.Join(devDir, "data"), "--create-user", testUser+":"+testPass)
		create.Stdout = os.Stdout
		create.Stderr = os.Stderr
		if err := create.Run(); err != nil {
			log.Fatalf("create-user failed: %v", err)
		}
	} else {
		log.Printf("Using existing DB (user: %s)", testUser)
	}

	// Seed sample files so the UI isn't empty.
	seedFiles(filepath.Join(devDir, "data", testUser, "files"))

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
	srv := exec.Command(bin,
		"--db", dbPath,
		"--addr", addr,
		"--base-url", baseURL,
		"--data-dir", filepath.Join(devDir, "data"),
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

// seedFiles creates sample files and directories if they don't already exist.
func seedFiles(filesDir string) {
	type seedFile struct {
		path    string
		content string
	}
	seeds := []seedFile{
		{"Documents/welcome.md", "# Welcome to Cog\n\nThis is a sample document.\n"},
		{"Documents/notes.txt", "Some quick notes for testing.\n"},
		{"Photos/readme.txt", "Drop images here.\n"},
		{"Photos/pattern.png", string(generatePatternPNG())},
		{"Projects/hello.go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello from Cog!\")\n}\n"},
	}

	for _, s := range seeds {
		p := filepath.Join(filesDir, s.path)
		if _, err := os.Stat(p); err == nil {
			continue // already exists
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			log.Printf("seed: mkdir %s: %v", filepath.Dir(p), err)
			continue
		}
		if err := os.WriteFile(p, []byte(s.content), 0o644); err != nil {
			log.Printf("seed: write %s: %v", p, err)
			continue
		}
		log.Printf("Seeded %s", s.path)
	}
}

// generatePatternPNG creates a 256x256 PNG with a colorful plasma-like pattern.
func generatePatternPNG() []byte {
	const size = 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			fx, fy := float64(x)/float64(size), float64(y)/float64(size)
			r := uint8(128 + 127*math.Sin(fx*2*math.Pi*3))
			g := uint8(128 + 127*math.Sin(fy*2*math.Pi*3+2))
			b := uint8(128 + 127*math.Sin((fx+fy)*2*math.Pi*2+4))
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}
