package calendar

import (
	"fmt"
	"strings"
	"time"
)

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
