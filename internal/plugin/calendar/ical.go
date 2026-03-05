package calendar

import (
	"fmt"
	"strings"
	"time"
)

// VAlarm holds extracted fields from a VALARM component.
type VAlarm struct {
	Action  string // "DISPLAY", "AUDIO", "EMAIL"
	Trigger string // e.g. "-PT15M" (15 min before)
}

// AttendeeField holds extracted ATTENDEE property fields from an iCalendar component.
type AttendeeField struct {
	Email    string
	CN       string
	PartStat string
	Role     string
}

// VEventFields holds extracted fields from a VEVENT component.
type VEventFields struct {
	UID         string
	Summary     string
	Description string
	Location    string
	DTStart     time.Time
	DTEnd       time.Time
	IsAllDay    bool
	Color       string // per-event color (Cog extension, not stored in ICS)
	RRule       string // raw RRULE value, e.g. "FREQ=WEEKLY;BYDAY=MO,WE,FR"
	Alarms      []VAlarm
	Attendees   []AttendeeField
}

// ParseVEvent extracts fields from an iCalendar blob containing a VEVENT.
func ParseVEvent(icsData string) (VEventFields, error) {
	// Unfold continuation lines (RFC 5545 §3.1).
	icsData = strings.ReplaceAll(icsData, "\r\n ", "")
	icsData = strings.ReplaceAll(icsData, "\r\n\t", "")

	lines := strings.Split(icsData, "\n")
	// Normalize \r
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}

	var inEvent, inAlarm bool
	var f VEventFields
	var curAlarm VAlarm
	found := false

	for _, line := range lines {
		if line == "BEGIN:VEVENT" {
			inEvent = true
			found = true
			continue
		}
		if line == "END:VEVENT" {
			break
		}
		if line == "BEGIN:VALARM" {
			inAlarm = true
			curAlarm = VAlarm{}
			continue
		}
		if line == "END:VALARM" {
			inAlarm = false
			f.Alarms = append(f.Alarms, curAlarm)
			continue
		}
		if !inEvent {
			continue
		}

		// Split into property name (with optional params) and value.
		propFull, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		// Property name is before any semicolon (params after).
		propName, _, _ := strings.Cut(propFull, ";")

		if inAlarm {
			switch propName {
			case "ACTION":
				curAlarm.Action = value
			case "TRIGGER":
				curAlarm.Trigger = value
			}
			continue
		}

		switch propName {
		case "UID":
			f.UID = value
		case "SUMMARY":
			f.Summary = unescapeICS(value)
		case "DESCRIPTION":
			f.Description = unescapeICS(value)
		case "LOCATION":
			f.Location = unescapeICS(value)
		case "DTSTART":
			t, allDay := parseDateTime(propFull, value)
			f.DTStart = t
			if allDay {
				f.IsAllDay = true
			}
		case "DTEND":
			t, _ := parseDateTime(propFull, value)
			f.DTEnd = t
		case "RRULE":
			f.RRule = value
		case "ATTENDEE":
			af := AttendeeField{}
			// Extract email from MAILTO: value
			mailto := strings.TrimPrefix(value, "mailto:")
			mailto = strings.TrimPrefix(mailto, "MAILTO:")
			af.Email = mailto
			// Extract params from propFull (e.g. ATTENDEE;CN=Name;PARTSTAT=ACCEPTED;ROLE=REQ-PARTICIPANT)
			for param := range strings.SplitSeq(propFull, ";") {
				pk, pv, ok := strings.Cut(param, "=")
				if !ok {
					continue
				}
				switch strings.ToUpper(pk) {
				case "CN":
					af.CN = strings.Trim(pv, "\"")
				case "PARTSTAT":
					af.PartStat = pv
				case "ROLE":
					af.Role = pv
				}
			}
			if af.PartStat == "" {
				af.PartStat = "NEEDS-ACTION"
			}
			if af.Role == "" {
				af.Role = "REQ-PARTICIPANT"
			}
			f.Attendees = append(f.Attendees, af)
		}
	}

	if !found {
		return f, fmt.Errorf("no VEVENT found in iCalendar data")
	}
	if f.UID == "" {
		return f, fmt.Errorf("VEVENT missing UID")
	}
	return f, nil
}

// parseDateTime parses an iCalendar date or datetime value.
// Returns the parsed time and whether it's an all-day (DATE) value.
func parseDateTime(propFull, value string) (time.Time, bool) {
	// Check for VALUE=DATE parameter.
	upper := strings.ToUpper(propFull)
	if strings.Contains(upper, "VALUE=DATE") && !strings.Contains(upper, "VALUE=DATE-TIME") {
		t, err := time.Parse("20060102", value)
		if err == nil {
			return t, true
		}
	}

	// Try common datetime formats.
	// YYYYMMDDTHHMMSSZ
	if t, err := time.Parse("20060102T150405Z", value); err == nil {
		return t, false
	}
	// YYYYMMDDTHHMMSS (floating, treat as UTC)
	if t, err := time.Parse("20060102T150405", value); err == nil {
		return t.UTC(), false
	}
	// DATE only (8 digits, no T)
	if len(value) == 8 {
		if t, err := time.Parse("20060102", value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// unescapeICS reverses iCalendar text escaping.
func unescapeICS(s string) string {
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\N", "\n")
	s = strings.ReplaceAll(s, "\\,", ",")
	s = strings.ReplaceAll(s, "\\;", ";")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	return s
}

// escapeICS applies iCalendar text escaping.
func escapeICS(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

// GenerateICS produces a minimal iCalendar blob for a single VEVENT.
func GenerateICS(uid, summary, description, location string, dtstart, dtend time.Time, isAllDay bool) string {
	return GenerateICSFull(uid, summary, description, location, dtstart, dtend, isAllDay, "", nil, nil)
}

// GenerateICSFull produces an iCalendar blob with optional RRULE, VALARM, and ATTENDEE.
func GenerateICSFull(uid, summary, description, location string, dtstart, dtend time.Time, isAllDay bool, rrule string, alarms []VAlarm, attendees []AttendeeField) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//Cog//Calendar//EN\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	fmt.Fprintf(&b, "UID:%s\r\n", uid)

	if isAllDay {
		fmt.Fprintf(&b, "DTSTART;VALUE=DATE:%s\r\n", dtstart.Format("20060102"))
		fmt.Fprintf(&b, "DTEND;VALUE=DATE:%s\r\n", dtend.Format("20060102"))
	} else {
		fmt.Fprintf(&b, "DTSTART:%s\r\n", dtstart.UTC().Format("20060102T150405Z"))
		fmt.Fprintf(&b, "DTEND:%s\r\n", dtend.UTC().Format("20060102T150405Z"))
	}

	if summary != "" {
		fmt.Fprintf(&b, "SUMMARY:%s\r\n", escapeICS(summary))
	}
	if description != "" {
		fmt.Fprintf(&b, "DESCRIPTION:%s\r\n", escapeICS(description))
	}
	if location != "" {
		fmt.Fprintf(&b, "LOCATION:%s\r\n", escapeICS(location))
	}
	if rrule != "" {
		fmt.Fprintf(&b, "RRULE:%s\r\n", rrule)
	}

	for _, att := range attendees {
		params := fmt.Sprintf("ATTENDEE;ROLE=%s;PARTSTAT=%s", att.Role, att.PartStat)
		if att.CN != "" {
			params += fmt.Sprintf(";CN=%s", att.CN)
		}
		fmt.Fprintf(&b, "%s:mailto:%s\r\n", params, att.Email)
	}

	fmt.Fprintf(&b, "DTSTAMP:%s\r\n", time.Now().UTC().Format("20060102T150405Z"))

	for _, alarm := range alarms {
		b.WriteString("BEGIN:VALARM\r\n")
		fmt.Fprintf(&b, "ACTION:%s\r\n", alarm.Action)
		fmt.Fprintf(&b, "TRIGGER:%s\r\n", alarm.Trigger)
		if alarm.Action == "DISPLAY" {
			b.WriteString("DESCRIPTION:Reminder\r\n")
		}
		b.WriteString("END:VALARM\r\n")
	}

	b.WriteString("END:VEVENT\r\n")
	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

// GenerateICSInvite produces an iCalendar blob with METHOD for email invitations.
func GenerateICSInvite(method, uid, summary, description, location string, dtstart, dtend time.Time, isAllDay bool, attendees []AttendeeField) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//Cog//Calendar//EN\r\n")
	fmt.Fprintf(&b, "METHOD:%s\r\n", method)
	b.WriteString("BEGIN:VEVENT\r\n")
	fmt.Fprintf(&b, "UID:%s\r\n", uid)

	if isAllDay {
		fmt.Fprintf(&b, "DTSTART;VALUE=DATE:%s\r\n", dtstart.Format("20060102"))
		fmt.Fprintf(&b, "DTEND;VALUE=DATE:%s\r\n", dtend.Format("20060102"))
	} else {
		fmt.Fprintf(&b, "DTSTART:%s\r\n", dtstart.UTC().Format("20060102T150405Z"))
		fmt.Fprintf(&b, "DTEND:%s\r\n", dtend.UTC().Format("20060102T150405Z"))
	}

	if summary != "" {
		fmt.Fprintf(&b, "SUMMARY:%s\r\n", escapeICS(summary))
	}
	if description != "" {
		fmt.Fprintf(&b, "DESCRIPTION:%s\r\n", escapeICS(description))
	}
	if location != "" {
		fmt.Fprintf(&b, "LOCATION:%s\r\n", escapeICS(location))
	}

	for _, att := range attendees {
		params := fmt.Sprintf("ATTENDEE;ROLE=%s;PARTSTAT=%s", att.Role, att.PartStat)
		if att.CN != "" {
			params += fmt.Sprintf(";CN=%s", att.CN)
		}
		fmt.Fprintf(&b, "%s:mailto:%s\r\n", params, att.Email)
	}

	fmt.Fprintf(&b, "DTSTAMP:%s\r\n", time.Now().UTC().Format("20060102T150405Z"))
	b.WriteString("END:VEVENT\r\n")
	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

// FormatRRule produces a human-readable recurrence description.
func FormatRRule(rrule string) string {
	if rrule == "" {
		return ""
	}
	params := make(map[string]string)
	for part := range strings.SplitSeq(rrule, ";") {
		k, v, _ := strings.Cut(part, "=")
		params[strings.ToUpper(k)] = v
	}

	freq := params["FREQ"]
	interval := 1
	if v, ok := params["INTERVAL"]; ok {
		if n, err := fmt.Sscanf(v, "%d", &interval); n == 0 || err != nil {
			interval = 1
		}
	}

	var desc string
	switch freq {
	case "DAILY":
		if interval == 1 {
			desc = "Every day"
		} else {
			desc = fmt.Sprintf("Every %d days", interval)
		}
	case "WEEKLY":
		if interval == 1 {
			desc = "Every week"
		} else {
			desc = fmt.Sprintf("Every %d weeks", interval)
		}
		if days, ok := params["BYDAY"]; ok {
			desc += " on " + formatByDay(days)
		}
	case "MONTHLY":
		if interval == 1 {
			desc = "Every month"
		} else {
			desc = fmt.Sprintf("Every %d months", interval)
		}
	case "YEARLY":
		if interval == 1 {
			desc = "Every year"
		} else {
			desc = fmt.Sprintf("Every %d years", interval)
		}
	default:
		return rrule
	}

	if count, ok := params["COUNT"]; ok {
		desc += fmt.Sprintf(", %s times", count)
	}
	if until, ok := params["UNTIL"]; ok {
		if t, err := time.Parse("20060102T150405Z", until); err == nil {
			desc += fmt.Sprintf(", until %s", t.Format("Jan 2, 2006"))
		} else if t, err := time.Parse("20060102", until); err == nil {
			desc += fmt.Sprintf(", until %s", t.Format("Jan 2, 2006"))
		}
	}

	return desc
}

var dayNames = map[string]string{
	"MO": "Monday", "TU": "Tuesday", "WE": "Wednesday",
	"TH": "Thursday", "FR": "Friday", "SA": "Saturday", "SU": "Sunday",
}

func formatByDay(byday string) string {
	parts := strings.Split(byday, ",")
	names := make([]string, 0, len(parts))
	for _, d := range parts {
		d = strings.TrimSpace(d)
		if name, ok := dayNames[d]; ok {
			names = append(names, name)
		} else {
			names = append(names, d)
		}
	}
	return strings.Join(names, ", ")
}

// FormatTrigger produces a human-readable alarm trigger description.
func FormatTrigger(trigger string) string {
	if trigger == "" {
		return ""
	}
	trigger = strings.TrimSpace(trigger)
	negative := strings.HasPrefix(trigger, "-")
	s := strings.TrimPrefix(trigger, "-")
	s = strings.TrimPrefix(s, "+")
	s = strings.TrimPrefix(s, "P")
	s = strings.TrimPrefix(s, "T")

	suffix := "before"
	if !negative {
		suffix = "after"
	}

	// Simple cases: "15M", "1H", "1D", "1W"
	if len(s) >= 2 {
		unit := s[len(s)-1]
		numStr := s[:len(s)-1]
		var n int
		if _, err := fmt.Sscanf(numStr, "%d", &n); err == nil {
			switch unit {
			case 'M':
				if n == 1 {
					return fmt.Sprintf("1 minute %s", suffix)
				}
				return fmt.Sprintf("%d minutes %s", n, suffix)
			case 'H':
				if n == 1 {
					return fmt.Sprintf("1 hour %s", suffix)
				}
				return fmt.Sprintf("%d hours %s", n, suffix)
			case 'D':
				if n == 1 {
					return fmt.Sprintf("1 day %s", suffix)
				}
				return fmt.Sprintf("%d days %s", n, suffix)
			case 'W':
				if n == 1 {
					return fmt.Sprintf("1 week %s", suffix)
				}
				return fmt.Sprintf("%d weeks %s", n, suffix)
			}
		}
	}

	return trigger
}
