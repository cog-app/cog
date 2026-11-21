package shell

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"mvdan.cc/sh/v3/interp"
)

func (s *Shell) newDateCmd() *cobra.Command {
	var utc bool
	var reference, date string
	cmd := &cobra.Command{
		Use:   "date [flags] [+format]",
		Short: "Print the date and time",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hc := interp.HandlerCtx(cmd.Context())
			format := "%a %b %e %H:%M:%S %Z %Y"
			if len(args) == 1 {
				f, ok := strings.CutPrefix(args[0], "+")
				if !ok {
					s.errorf(hc.Stderr, "date: setting the date is not supported\n")
					return exitErr(1)
				}
				format = f
			}
			loc := time.Local
			if tz := hc.Env.Get("TZ"); tz.IsSet() && tz.String() != "" {
				// An unknown zone is UTC, as for GNU date.
				l, err := time.LoadLocation(tz.String())
				if err != nil {
					l = time.UTC
				}
				loc = l
			}
			if utc {
				loc = time.UTC
			}
			t := time.Now()
			switch {
			case reference != "" && date != "":
				s.errorf(hc.Stderr, "date: the options to specify dates are mutually exclusive\n")
				return exitErr(1)
			case reference != "":
				fi, err := s.fs.Stat(s.toVirtualPath(hc.Dir, reference))
				if err != nil {
					s.errorf(hc.Stderr, "date: %s: %v\n", reference, err)
					return exitErr(1)
				}
				t = fi.ModTime
			case date != "":
				var err error
				if t, err = parseDate(date, t, loc); err != nil {
					s.errorf(hc.Stderr, "date: %v\n", err)
					return exitErr(1)
				}
			}
			out := strftime(make([]byte, 0, 64), format, t.In(loc))
			_, err := hc.Stdout.Write(append(out, '\n'))
			return err
		},
	}
	cmd.Flags().BoolVarP(&utc, "utc", "u", false, "Print Coordinated Universal Time")
	cmd.Flags().StringVarP(&reference, "reference", "r", "", "Show the last modification time of FILE")
	cmd.Flags().StringVarP(&date, "date", "d", "", "Show the time given by STRING, not now")
	return cmd
}

var dateLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	time.RFC1123Z,
	time.RFC1123,
	time.UnixDate,
}

// parseDate understands @epoch, now, today, yesterday, tomorrow and common
// absolute forms; GNU date's free-form relative phrases are not supported.
func parseDate(s string, now time.Time, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if sec, ok := strings.CutPrefix(s, "@"); ok {
		f, err := strconv.ParseFloat(sec, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid date %q", s)
		}
		whole := int64(f)
		return time.Unix(whole, int64((f-float64(whole))*1e9)), nil
	}
	day := func(offset int) time.Time {
		y, m, d := now.In(loc).Date()
		return time.Date(y, m, d+offset, 0, 0, 0, 0, loc)
	}
	switch strings.ToLower(s) {
	case "now":
		return now, nil
	case "today":
		return day(0), nil
	case "yesterday":
		return day(-1), nil
	case "tomorrow":
		return day(1), nil
	}
	for _, layout := range dateLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date %q", s)
}

// strftime appends t formatted by the C strftime conversions GNU date
// documents most; an unknown conversion is copied as it is.
func strftime(b []byte, format string, t time.Time) []byte {
	pad := func(b []byte, v, width int, fill byte) []byte {
		var num [20]byte
		d := strconv.AppendInt(num[:0], int64(v), 10)
		for range width - len(d) {
			b = append(b, fill)
		}
		return append(b, d...)
	}
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '%' || i+1 == len(format) {
			b = append(b, c)
			continue
		}
		i++
		switch format[i] {
		case 'Y':
			b = pad(b, t.Year(), 4, '0')
		case 'y':
			b = pad(b, t.Year()%100, 2, '0')
		case 'C':
			b = pad(b, t.Year()/100, 2, '0')
		case 'm':
			b = pad(b, int(t.Month()), 2, '0')
		case 'd':
			b = pad(b, t.Day(), 2, '0')
		case 'e':
			b = pad(b, t.Day(), 2, ' ')
		case 'H':
			b = pad(b, t.Hour(), 2, '0')
		case 'k':
			b = pad(b, t.Hour(), 2, ' ')
		case 'I':
			b = pad(b, (t.Hour()+11)%12+1, 2, '0')
		case 'l':
			b = pad(b, (t.Hour()+11)%12+1, 2, ' ')
		case 'M':
			b = pad(b, t.Minute(), 2, '0')
		case 'S':
			b = pad(b, t.Second(), 2, '0')
		case 'N':
			b = pad(b, t.Nanosecond(), 9, '0')
		case 'p':
			if t.Hour() < 12 {
				b = append(b, "AM"...)
			} else {
				b = append(b, "PM"...)
			}
		case 's':
			b = strconv.AppendInt(b, t.Unix(), 10)
		case 'j':
			b = pad(b, t.YearDay(), 3, '0')
		case 'u':
			b = pad(b, (int(t.Weekday())+6)%7+1, 1, '0')
		case 'w':
			b = pad(b, int(t.Weekday()), 1, '0')
		case 'a':
			b = append(b, t.Weekday().String()[:3]...)
		case 'A':
			b = append(b, t.Weekday().String()...)
		case 'b', 'h':
			b = append(b, t.Month().String()[:3]...)
		case 'B':
			b = append(b, t.Month().String()...)
		case 'Z':
			name, _ := t.Zone()
			b = append(b, name...)
		case 'z':
			b = t.AppendFormat(b, "-0700")
		case 'F':
			b = strftime(b, "%Y-%m-%d", t)
		case 'T':
			b = strftime(b, "%H:%M:%S", t)
		case 'D':
			b = strftime(b, "%m/%d/%y", t)
		case 'R':
			b = strftime(b, "%H:%M", t)
		case 'c':
			b = strftime(b, "%a %b %e %H:%M:%S %Y", t)
		case 'n':
			b = append(b, '\n')
		case 't':
			b = append(b, '\t')
		case '%':
			b = append(b, '%')
		default:
			b = append(b, '%', format[i])
		}
	}
	return b
}

func (s *Shell) newSleepCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sleep <number[smhd]...>",
		Short: "Wait for the total of the given times",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stderr, _, _ := getHC(cmd)
			var total time.Duration
			for _, a := range args {
				d, err := parseSleep(a)
				if err != nil {
					s.errorf(stderr, "sleep: %v\n", err)
					return exitErr(1)
				}
				total += d
			}
			t := time.NewTimer(total)
			defer t.Stop()
			select {
			case <-t.C:
				return nil
			case <-cmd.Context().Done():
				return exitErr(130)
			}
		},
	}
}

func parseSleep(a string) (time.Duration, error) {
	unit := time.Second
	num := a
	if n := len(a); n > 0 {
		switch a[n-1] {
		case 's':
			num = a[:n-1]
		case 'm':
			unit, num = time.Minute, a[:n-1]
		case 'h':
			unit, num = time.Hour, a[:n-1]
		case 'd':
			unit, num = 24*time.Hour, a[:n-1]
		}
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid time interval %q", a)
	}
	d := f * float64(unit)
	if d > float64(1<<62) {
		return 0, errors.New("time interval too long")
	}
	return time.Duration(d), nil
}
