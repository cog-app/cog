package tasks

import (
	"fmt"
	"strings"
	"time"
)

// VTodoFields holds extracted fields from a VTODO component.
type VTodoFields struct {
	UID             string
	Summary         string
	Description     string
	Priority        int
	PercentComplete int
	Status          string // NEEDS-ACTION, COMPLETED, IN-PROCESS, CANCELLED
	Due             time.Time
	Start           time.Time
	Completed       time.Time
	RelatedTo       string // parent UID for subtasks
}

// ParseVTodo extracts fields from an iCalendar blob containing a VTODO.
func ParseVTodo(icsData string) (VTodoFields, error) {
	// Unfold continuation lines (RFC 5545 §3.1).
	icsData = strings.ReplaceAll(icsData, "\r\n ", "")
	icsData = strings.ReplaceAll(icsData, "\r\n\t", "")

	lines := strings.Split(icsData, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}

	var inTodo bool
	var f VTodoFields
	found := false

	for _, line := range lines {
		if line == "BEGIN:VTODO" {
			inTodo = true
			found = true
			continue
		}
		if line == "END:VTODO" {
			break
		}
		if !inTodo {
			continue
		}

		propFull, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		propName, _, _ := strings.Cut(propFull, ";")

		switch propName {
		case "UID":
			f.UID = value
		case "SUMMARY":
			f.Summary = unescapeICS(value)
		case "DESCRIPTION":
			f.Description = unescapeICS(value)
		case "PRIORITY":
			fmt.Sscanf(value, "%d", &f.Priority)
		case "PERCENT-COMPLETE":
			fmt.Sscanf(value, "%d", &f.PercentComplete)
		case "STATUS":
			f.Status = value
		case "DUE":
			f.Due, _ = parseDateTime(propFull, value)
		case "DTSTART":
			f.Start, _ = parseDateTime(propFull, value)
		case "COMPLETED":
			f.Completed, _ = parseDateTime(propFull, value)
		case "RELATED-TO":
			f.RelatedTo = value
		}
	}

	if !found {
		return f, fmt.Errorf("no VTODO found in iCalendar data")
	}
	if f.UID == "" {
		return f, fmt.Errorf("VTODO missing UID")
	}
	return f, nil
}

// GenerateVTodoICS produces a VCALENDAR wrapping a VTODO.
func GenerateVTodoICS(uid string, fields VTodoFields, now time.Time) string {
	if uid == "" {
		uid = fields.UID
	}
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//Cog//Tasks//EN\r\n")
	b.WriteString("BEGIN:VTODO\r\n")
	fmt.Fprintf(&b, "UID:%s\r\n", uid)

	if fields.Summary != "" {
		fmt.Fprintf(&b, "SUMMARY:%s\r\n", escapeICS(fields.Summary))
	}
	if fields.Description != "" {
		fmt.Fprintf(&b, "DESCRIPTION:%s\r\n", escapeICS(fields.Description))
	}
	if fields.Priority > 0 {
		fmt.Fprintf(&b, "PRIORITY:%d\r\n", fields.Priority)
	}
	if fields.PercentComplete > 0 {
		fmt.Fprintf(&b, "PERCENT-COMPLETE:%d\r\n", fields.PercentComplete)
	}
	if fields.Status != "" {
		fmt.Fprintf(&b, "STATUS:%s\r\n", fields.Status)
	}
	if !fields.Due.IsZero() {
		fmt.Fprintf(&b, "DUE:%s\r\n", fields.Due.UTC().Format("20060102T150405Z"))
	}
	if !fields.Start.IsZero() {
		fmt.Fprintf(&b, "DTSTART:%s\r\n", fields.Start.UTC().Format("20060102T150405Z"))
	}
	if !fields.Completed.IsZero() {
		fmt.Fprintf(&b, "COMPLETED:%s\r\n", fields.Completed.UTC().Format("20060102T150405Z"))
	}
	if fields.RelatedTo != "" {
		fmt.Fprintf(&b, "RELATED-TO:%s\r\n", fields.RelatedTo)
	}

	fmt.Fprintf(&b, "DTSTAMP:%s\r\n", now.UTC().Format("20060102T150405Z"))
	b.WriteString("END:VTODO\r\n")
	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

// parseDateTime parses an iCalendar date or datetime value.
func parseDateTime(propFull, value string) (time.Time, bool) {
	upper := strings.ToUpper(propFull)
	if strings.Contains(upper, "VALUE=DATE") && !strings.Contains(upper, "VALUE=DATE-TIME") {
		t, err := time.Parse("20060102", value)
		if err == nil {
			return t, true
		}
	}
	if t, err := time.Parse("20060102T150405Z", value); err == nil {
		return t, false
	}
	if t, err := time.Parse("20060102T150405", value); err == nil {
		return t.UTC(), false
	}
	if len(value) == 8 {
		if t, err := time.Parse("20060102", value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func unescapeICS(s string) string {
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\N", "\n")
	s = strings.ReplaceAll(s, "\\,", ",")
	s = strings.ReplaceAll(s, "\\;", ";")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	return s
}

func escapeICS(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}
