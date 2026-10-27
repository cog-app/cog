// Dev tool for Cog. Usage: go tool dev [-reset] [-watch] [-shell]
//
// Builds cog with seed support, ensures a test user exists, and runs with
// request logging and COG_SEED=true. Logs are teed to tmp/dev/cog.log.
// Use -reset to wipe state first. Use -watch for live reload via templ proxy.
package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	"git.duckfam.us/jj-software/cog/internal/maildev"
	"git.duckfam.us/jj-software/cog/internal/settings"
	_ "github.com/mattn/go-sqlite3"
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

	switch {
	case *shell:
		runShell(bin, dbPath, devDir, *stale)
	case *watch:
		runWatch(bin, dbPath, devDir, *logRequests, *stale)
	default:
		runOnce(bin, dbPath, devDir, *logRequests, *stale)
	}
}

// runOnce is the original one-shot generate → build → run cycle.
func runOnce(bin, dbPath, devDir string, logRequests, stale bool) {
	if !stale {
		generate()
		build(bin)
	}

	seedUser(bin, dbPath, devDir)

	mail := startMaildev(dbPath)
	defer mail.Close()

	teeOut, teeErr, cleanup := openLog(devDir)
	defer cleanup()

	addr := proxyAddr // non-watch uses :8580 directly
	srv := startServer(bin, dbPath, devDir, addr, logRequests, teeOut, teeErr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		srv.Process.Kill()
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

const (
	mailSMTPAddr = ":2525"
	mailHTTPAddr = ":8025"
)

// startMaildev launches the dev email server and pre-configures SMTP settings
// in the Cog database so EmailSender() returns a working sender.
func startMaildev(dbPath string) *maildev.Server {
	srv := maildev.NewServer(mailSMTPAddr, mailHTTPAddr)
	if err := srv.Start(); err != nil {
		log.Fatalf("maildev: %v", err)
	}
	log.Printf("Mail UI: http://localhost%s", mailHTTPAddr)
	log.Printf("Mail SMTP: localhost%s", mailSMTPAddr)

	// Pre-configure SMTP settings in the database.
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("maildev: open db: %v", err)
	}
	store := settings.NewStore(db)
	err = errors.Join(
		store.Set(settings.KeySMTPHost, "localhost"),
		store.SetInt(settings.KeySMTPPort, 2525),
		store.Set(settings.KeySMTPFrom, "cog@localhost"),
		// Clear auth — maildev doesn't require it.
		store.Set(settings.KeySMTPUser, ""),
		store.Set(settings.KeySMTPPass, ""),
	)
	if err != nil {
		log.Fatalf("maildev: configure SMTP: %v", err)
	}

	// Set admin email so email features (password reset, OTP) work out of the box.
	if _, err := db.Exec(`UPDATE users SET email = ? WHERE username = ?`, "admin@localhost", testUser); err != nil {
		log.Printf("maildev: set admin email: %v", err)
	}

	if err := db.Close(); err != nil {
		log.Fatalf("maildev: close db: %v", err)
	}

	return srv
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
	baseURL := fmt.Sprintf("http://localhost%s", addr)
	log.Printf("Starting cog on %s (base-url %s)", addr, baseURL)

	srv := exec.Command(bin, "serve")
	srv.Env = append(os.Environ(),
		"COG_DB="+dbPath,
		"COG_ADDR="+addr,
		"COG_DATA_DIR="+filepath.Join(devDir, "data"),
		"COG_BASE_URL="+baseURL,
		"COG_SEED=true",
		"COG_PRIvaTE_IPS=true",
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
