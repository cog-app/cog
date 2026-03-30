package calendar

import (
	"crypto/md5"
	"fmt"
	"time"
)

// Seed creates sample calendars and events for a user.
func (p *Plugin) Seed(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return fmt.Errorf("seed calendar: %w", err)
	}

	// Check if already seeded.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM calendars`).Scan(&count)
	if count > 0 {
		return nil
	}

	db.Exec(`INSERT INTO calendars (uid, name, color) VALUES ('personal', 'Personal', '#0082c9')`)
	db.Exec(`INSERT INTO calendars (uid, name, color) VALUES ('work', 'Work', '#e64a19')`)

	// Find start of current week (Monday).
	now := p.ctx.Now().UTC()
	weekday := now.Weekday()
	if weekday == time.Sunday {
		weekday = 7
	}
	monday := now.AddDate(0, 0, -int(weekday-time.Monday))
	weekStart := time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, time.UTC)

	// Helper: offset from week start by days, hours, minutes.
	at := func(days, hours, minutes int) time.Time {
		return weekStart.Add(time.Duration(days)*24*time.Hour +
			time.Duration(hours)*time.Hour +
			time.Duration(minutes)*time.Minute)
	}
	day := func(days int) time.Time {
		return weekStart.AddDate(0, 0, days)
	}

	type seedEvent struct {
		calUID  string
		uid     string
		summary string
		loc     string
		desc    string
		start   time.Time
		end     time.Time
		allDay  bool
	}

	events := []seedEvent{
		// Monday
		{"personal", "team-standup", "Team Standup", "Zoom", "Daily sync with the engineering team",
			at(0, 9, 30), at(0, 9, 45), false},
		// Monday
		{"personal", "lunch-alice", "Lunch with Alice", "Cafe Roma", "",
			at(0, 12, 0), at(0, 13, 0), false},
		// Tuesday
		{"personal", "dentist-appt", "Dentist Appointment", "123 Main St", "",
			at(1, 14, 0), at(1, 15, 0), false},
		// Saturday (all day)
		{"personal", "mom-birthday", "Mom's Birthday", "", "",
			day(5), day(6), true},
		// Tuesday
		{"work", "sprint-review", "Sprint Review", "Conference Room B", "Demo new features to stakeholders",
			at(1, 16, 0), at(1, 17, 0), false},
		// Wednesday
		{"work", "1on1-manager", "1:1 with Manager", "Office 204", "",
			at(2, 10, 0), at(2, 10, 30), false},
		// Thursday (next week)
		{"work", "company-allhands", "Company All-Hands", "Main Auditorium", "Quarterly update from leadership",
			at(10, 15, 0), at(10, 16, 30), false},
		// Friday-Monday next week (all day)
		{"work", "deploy-freeze", "Deploy Freeze", "", "No deployments during release window",
			day(11), day(14), true},
	}

	for _, ev := range events {
		var icsData string
		if ev.allDay {
			icsData = icsAllDay(ev.uid, ev.summary,
				ev.start.Format("20060102"), ev.end.Format("20060102"))
		} else {
			icsData = ics(ev.uid, ev.summary, ev.desc, ev.loc,
				ev.start.Format("20060102T150405Z"), ev.end.Format("20060102T150405Z"))
		}
		etag := fmt.Sprintf("%x", md5.Sum([]byte(icsData)))
		var calID int64
		db.QueryRow(`SELECT id FROM calendars WHERE uid = ?`, ev.calUID).Scan(&calID)
		_, err := db.Exec(`INSERT INTO events (calendar_id, uid, etag, summary, location, description, dtstart, dtend, is_all_day, ics_data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			calID, ev.uid, etag, ev.summary, ev.loc, ev.desc, ev.start.Unix(), ev.end.Unix(), ev.allDay, icsData)
		if err != nil {
			p.log.Error("seed event", "uid", ev.uid, "err", err)
		} else {
			p.log.Info("seeded event", "summary", ev.summary)
		}
	}

	db.Exec(`UPDATE calendars SET ctag = (SELECT COUNT(*) FROM events WHERE calendar_id = calendars.id)`)
	return nil
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
