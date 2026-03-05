// Dev tool for Cog. Usage: go tool dev [-reset]
//
// Builds cog, ensures a test user exists, and runs with request logging.
// Logs are teed to tmp/dev/cog.log. Use -reset to wipe state first.
package main

import (
	"bytes"
	"crypto/md5"
	"database/sql"
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

	_ "github.com/mattn/go-sqlite3"
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

	// Seed calendar events.
	seedCalendar(filepath.Join(devDir, "data", testUser, "app_data", "calendar"))

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

// seedCalendar creates sample calendars and events if the calendar DB doesn't exist.
func seedCalendar(calDir string) {
	dbPath := filepath.Join(calDir, "plugin.db")
	if _, err := os.Stat(dbPath); err == nil {
		return // already seeded
	}
	if err := os.MkdirAll(calDir, 0o755); err != nil {
		log.Printf("seed calendar: mkdir: %v", err)
		return
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		log.Printf("seed calendar: open db: %v", err)
		return
	}
	defer db.Close()

	// Run migrations.
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS calendars (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uid TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL DEFAULT 'Personal',
			color TEXT NOT NULL DEFAULT '#0082c9',
			description TEXT NOT NULL DEFAULT '',
			sort_order INTEGER NOT NULL DEFAULT 0,
			ctag INTEGER NOT NULL DEFAULT 1,
			sync_token INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch())
		);
		CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
			uid TEXT NOT NULL,
			etag TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			location TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			dtstart INTEGER NOT NULL DEFAULT 0,
			dtend INTEGER NOT NULL DEFAULT 0,
			is_all_day INTEGER NOT NULL DEFAULT 0,
			ics_data TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
			UNIQUE(calendar_id, uid)
		);
		CREATE INDEX IF NOT EXISTS idx_events_calendar ON events(calendar_id);
		CREATE INDEX IF NOT EXISTS idx_events_dtstart ON events(dtstart);
		CREATE TABLE IF NOT EXISTS deleted_events (
			calendar_id INTEGER NOT NULL,
			uid TEXT NOT NULL,
			deleted_at_token INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_deleted_events_cal ON deleted_events(calendar_id, deleted_at_token);
	`); err != nil {
		log.Printf("seed calendar: migrate: %v", err)
		return
	}

	// Create calendars.
	db.Exec(`INSERT INTO calendars (uid, name, color) VALUES ('personal', 'Personal', '#0082c9')`)
	db.Exec(`INSERT INTO calendars (uid, name, color) VALUES ('work', 'Work', '#e64a19')`)

	type seedEvent struct {
		calUID  string
		uid     string
		summary string
		loc     string
		desc    string
		ics     string
		dtstart int64
		dtend   int64
		allDay  bool
	}

	events := []seedEvent{
		{"personal", "team-standup", "Team Standup", "Zoom", "Daily sync with the engineering team",
			ics("team-standup", "Team Standup", "Daily sync with the engineering team", "Zoom", "20260305T093000Z", "20260305T094500Z"),
			1741164600, 1741165500, false},
		{"personal", "lunch-alice", "Lunch with Alice", "Cafe Roma", "",
			ics("lunch-alice", "Lunch with Alice", "", "Cafe Roma", "20260305T120000Z", "20260305T130000Z"),
			1741173600, 1741177200, false},
		{"personal", "dentist-appt", "Dentist Appointment", "123 Main St", "",
			ics("dentist-appt", "Dentist Appointment", "", "123 Main St", "20260306T140000Z", "20260306T150000Z"),
			1741267200, 1741270800, false},
		{"personal", "mom-birthday", "Mom's Birthday", "", "",
			icsAllDay("mom-birthday", "Mom's Birthday", "20260310", "20260311"),
			1741564800, 1741651200, true},
		{"work", "sprint-review", "Sprint Review", "Conference Room B", "Demo new features to stakeholders",
			ics("sprint-review", "Sprint Review", "Demo new features to stakeholders", "Conference Room B", "20260306T160000Z", "20260306T170000Z"),
			1741274400, 1741278000, false},
		{"work", "1on1-manager", "1:1 with Manager", "Office 204", "",
			ics("1on1-manager", "1:1 with Manager", "", "Office 204", "20260307T100000Z", "20260307T103000Z"),
			1741339200, 1741341000, false},
		{"work", "company-allhands", "Company All-Hands", "Main Auditorium", "Quarterly update from leadership",
			ics("company-allhands", "Company All-Hands", "Quarterly update from leadership", "Main Auditorium", "20260312T150000Z", "20260312T163000Z"),
			1741791600, 1741797000, false},
		{"work", "deploy-freeze", "Deploy Freeze", "", "No deployments during release window",
			icsAllDay("deploy-freeze", "Deploy Freeze", "20260313", "20260316"),
			1741824000, 1742083200, true},
	}

	for _, ev := range events {
		etag := fmt.Sprintf("%x", md5.Sum([]byte(ev.ics)))
		var calID int64
		db.QueryRow(`SELECT id FROM calendars WHERE uid = ?`, ev.calUID).Scan(&calID)
		_, err := db.Exec(`INSERT INTO events (calendar_id, uid, etag, summary, location, description, dtstart, dtend, is_all_day, ics_data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			calID, ev.uid, etag, ev.summary, ev.loc, ev.desc, ev.dtstart, ev.dtend, ev.allDay, ev.ics)
		if err != nil {
			log.Printf("seed calendar event %s: %v", ev.uid, err)
		} else {
			log.Printf("Seeded calendar event: %s", ev.summary)
		}
	}

	// Bump ctags.
	db.Exec(`UPDATE calendars SET ctag = (SELECT COUNT(*) FROM events WHERE calendar_id = calendars.id)`)
}

func ics(uid, summary, description, location, dtstart, dtend string) string {
	s := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Cog//Seed//EN\r\nBEGIN:VEVENT\r\n"
	s += "UID:" + uid + "\r\n"
	s += "SUMMARY:" + summary + "\r\n"
	if description != "" {
		s += "DESCRIPTION:" + description + "\r\n"
	}
	if location != "" {
		s += "LOCATION:" + location + "\r\n"
	}
	s += "DTSTART:" + dtstart + "\r\n"
	s += "DTEND:" + dtend + "\r\n"
	s += "END:VEVENT\r\nEND:VCALENDAR\r\n"
	return s
}

func icsAllDay(uid, summary, dtstart, dtend string) string {
	s := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Cog//Seed//EN\r\nBEGIN:VEVENT\r\n"
	s += "UID:" + uid + "\r\n"
	s += "SUMMARY:" + summary + "\r\n"
	s += "DTSTART;VALUE=DATE:" + dtstart + "\r\n"
	s += "DTEND;VALUE=DATE:" + dtend + "\r\n"
	s += "END:VEVENT\r\nEND:VCALENDAR\r\n"
	return s
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
