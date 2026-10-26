package calendar

import "testing"

func TestFormatRRule(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"FREQ=DAILY", "Every day"},
		{"FREQ=WEEKLY;BYDAY=MO,WE,FR", "Every week on Monday, Wednesday, Friday"},
		{"FREQ=MONTHLY;INTERVAL=2", "Every 2 months"},
		{"FREQ=YEARLY", "Every year"},
		{"FREQ=WEEKLY;COUNT=10", "Every week, 10 times"},
		{"FREQ=DAILY;UNTIL=20261231T235959Z", "Every day, until Dec 31, 2026"},
		{"FREQ=WEEKLY;INTERVAL=3;BYDAY=TU", "Every 3 weeks on Tuesday"},
	}
	for _, tt := range tests {
		got := FormatRRule(tt.input)
		if got != tt.want {
			t.Errorf("FormatRRule(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatTrigger(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"-PT15M", "15 minutes before"},
		{"-PT1H", "1 hour before"},
		{"-PT1D", "1 day before"},
		{"-PT2W", "2 weeks before"},
		{"+PT30M", "30 minutes after"},
		{"-PT1M", "1 minute before"},
	}
	for _, tt := range tests {
		got := FormatTrigger(tt.input)
		if got != tt.want {
			t.Errorf("FormatTrigger(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
