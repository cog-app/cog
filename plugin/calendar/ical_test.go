package calendar

import (
	"strings"
	"testing"
	"time"
)

func TestParseVEvent(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:test-123\r\nSUMMARY:Team Meeting\r\nDESCRIPTION:Discuss\\, plan\\nand review\r\nLOCATION:Room 42\r\nDTSTART:20260315T140000Z\r\nDTEND:20260315T150000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	f, err := ParseVEvent(ics)
	if err != nil {
		t.Fatal(err)
	}
	if f.UID != "test-123" {
		t.Errorf("UID = %q, want %q", f.UID, "test-123")
	}
	if f.Summary != "Team Meeting" {
		t.Errorf("Summary = %q, want %q", f.Summary, "Team Meeting")
	}
	if f.Description != "Discuss, plan\nand review" {
		t.Errorf("Description = %q, want %q", f.Description, "Discuss, plan\nand review")
	}
	if f.Location != "Room 42" {
		t.Errorf("Location = %q, want %q", f.Location, "Room 42")
	}
	if f.IsAllDay {
		t.Error("IsAllDay should be false")
	}
	want := time.Date(2026, 3, 15, 14, 0, 0, 0, time.UTC)
	if !f.DTStart.Equal(want) {
		t.Errorf("DTStart = %v, want %v", f.DTStart, want)
	}
}

func TestParseVEventAllDay(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:allday-1\r\nSUMMARY:Holiday\r\nDTSTART;VALUE=DATE:20260101\r\nDTEND;VALUE=DATE:20260102\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	f, err := ParseVEvent(ics)
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsAllDay {
		t.Error("IsAllDay should be true")
	}
	if f.DTStart.Format("20060102") != "20260101" {
		t.Errorf("DTStart = %v, want 20260101", f.DTStart)
	}
}

func TestParseVEventWithTZID(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:tz-1\r\nSUMMARY:Test\r\nDTSTART;TZID=America/New_York:20260315T100000\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	f, err := ParseVEvent(ics)
	if err != nil {
		t.Fatal(err)
	}
	if f.Summary != "Test" {
		t.Errorf("Summary = %q, want %q", f.Summary, "Test")
	}
	// Floating time treated as UTC.
	if f.DTStart.Hour() != 10 {
		t.Errorf("DTStart hour = %d, want 10", f.DTStart.Hour())
	}
}

func TestParseVEventNoEvent(t *testing.T) {
	_, err := ParseVEvent("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n")
	if err == nil {
		t.Error("expected error for missing VEVENT")
	}
}

func TestParseVEventNoUID(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSUMMARY:No UID\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	_, err := ParseVEvent(ics)
	if err == nil {
		t.Error("expected error for missing UID")
	}
}

func TestParseVEventUnfold(t *testing.T) {
	// RFC 5545 line folding: continuation lines start with a space.
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:fold-1\r\nSUMMARY:This is a very long\r\n  summary line\r\nDTSTART:20260315T140000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	f, err := ParseVEvent(ics)
	if err != nil {
		t.Fatal(err)
	}
	if f.Summary != "This is a very long summary line" {
		t.Errorf("Summary = %q, want %q", f.Summary, "This is a very long summary line")
	}
}

func TestGenerateICS(t *testing.T) {
	start := time.Date(2026, 3, 15, 14, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 15, 15, 0, 0, 0, time.UTC)

	ics := GenerateICS("test-uid", "Meeting", "Notes", "Room 1", start, end, false, time.Now())

	if !strings.Contains(ics, "UID:test-uid") {
		t.Error("missing UID")
	}
	if !strings.Contains(ics, "SUMMARY:Meeting") {
		t.Error("missing SUMMARY")
	}
	if !strings.Contains(ics, "DTSTART:20260315T140000Z") {
		t.Error("missing DTSTART")
	}

	// Round-trip: generate then parse.
	f, err := ParseVEvent(ics)
	if err != nil {
		t.Fatal(err)
	}
	if f.UID != "test-uid" {
		t.Errorf("round-trip UID = %q", f.UID)
	}
	if f.Summary != "Meeting" {
		t.Errorf("round-trip Summary = %q", f.Summary)
	}
}

func TestGenerateICSAllDay(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	ics := GenerateICS("allday-uid", "Holiday", "", "", start, end, true, time.Now())

	if !strings.Contains(ics, "DTSTART;VALUE=DATE:20260101") {
		t.Error("missing all-day DTSTART")
	}
	if !strings.Contains(ics, "DTEND;VALUE=DATE:20260102") {
		t.Error("missing all-day DTEND")
	}

	f, err := ParseVEvent(ics)
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsAllDay {
		t.Error("round-trip should be all-day")
	}
}

func TestParseVEventRRule(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:rrule-1\r\nSUMMARY:Weekly Standup\r\nRRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR\r\nDTSTART:20260315T090000Z\r\nDTEND:20260315T091500Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	f, err := ParseVEvent(ics)
	if err != nil {
		t.Fatal(err)
	}
	if f.RRule != "FREQ=WEEKLY;BYDAY=MO,WE,FR" {
		t.Errorf("RRule = %q, want %q", f.RRule, "FREQ=WEEKLY;BYDAY=MO,WE,FR")
	}
}

func TestParseVEventVAlarm(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:alarm-1\r\nSUMMARY:Meeting\r\nDTSTART:20260315T140000Z\r\nDTEND:20260315T150000Z\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nDESCRIPTION:Reminder\r\nEND:VALARM\r\nBEGIN:VALARM\r\nACTION:AUDIO\r\nTRIGGER:-PT5M\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	f, err := ParseVEvent(ics)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Alarms) != 2 {
		t.Fatalf("Alarms count = %d, want 2", len(f.Alarms))
	}
	if f.Alarms[0].Action != "DISPLAY" {
		t.Errorf("Alarm[0].Action = %q, want DISPLAY", f.Alarms[0].Action)
	}
	if f.Alarms[0].Trigger != "-PT15M" {
		t.Errorf("Alarm[0].Trigger = %q, want -PT15M", f.Alarms[0].Trigger)
	}
	if f.Alarms[1].Action != "AUDIO" {
		t.Errorf("Alarm[1].Action = %q, want AUDIO", f.Alarms[1].Action)
	}
}

func TestGenerateICSFull(t *testing.T) {
	start := time.Date(2026, 3, 15, 14, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 15, 15, 0, 0, 0, time.UTC)
	alarms := []VAlarm{{Action: "DISPLAY", Trigger: "-PT10M"}}

	ics := GenerateICSFull("full-uid", "Meeting", "", "", start, end, false, "FREQ=WEEKLY", alarms, nil, time.Now())

	if !strings.Contains(ics, "RRULE:FREQ=WEEKLY") {
		t.Error("missing RRULE")
	}
	if !strings.Contains(ics, "BEGIN:VALARM") {
		t.Error("missing VALARM")
	}
	if !strings.Contains(ics, "TRIGGER:-PT10M") {
		t.Error("missing TRIGGER")
	}

	f, err := ParseVEvent(ics)
	if err != nil {
		t.Fatal(err)
	}
	if f.RRule != "FREQ=WEEKLY" {
		t.Errorf("round-trip RRule = %q", f.RRule)
	}
	if len(f.Alarms) != 1 {
		t.Fatalf("round-trip Alarms count = %d", len(f.Alarms))
	}
}

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
