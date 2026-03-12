package logbuf

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestTeeHandlerCaptures(t *testing.T) {
	buf := New(10)
	var out bytes.Buffer
	inner := slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(NewTeeHandler(inner, buf))

	logger.Info("hello", "key", "val")
	logger.Debug("world")

	if buf.Len() != 2 {
		t.Fatalf("got %d entries, want 2", buf.Len())
	}
	entries := buf.Entries()
	if !strings.Contains(entries[0].Message, "hello") {
		t.Errorf("entry 0 missing 'hello': %s", entries[0].Message)
	}
	if entries[0].Level != slog.LevelInfo {
		t.Errorf("entry 0 level = %v, want INFO", entries[0].Level)
	}
	if !strings.Contains(entries[1].Message, "world") {
		t.Errorf("entry 1 missing 'world': %s", entries[1].Message)
	}
}

func TestTeeHandlerWithAttrs(t *testing.T) {
	buf := New(10)
	var out bytes.Buffer
	inner := slog.NewTextHandler(&out, nil)
	logger := slog.New(NewTeeHandler(inner, buf)).With("component", "test")

	logger.Info("msg")

	entries := buf.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if !strings.Contains(entries[0].Message, "component=test") {
		t.Errorf("entry missing attr: %s", entries[0].Message)
	}
}

func TestTeeHandlerWithGroup(t *testing.T) {
	buf := New(10)
	var out bytes.Buffer
	inner := slog.NewTextHandler(&out, nil)
	logger := slog.New(NewTeeHandler(inner, buf)).WithGroup("grp").With("k", "v")

	logger.Info("msg")

	entries := buf.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if !strings.Contains(entries[0].Message, "grp.k=v") {
		t.Errorf("entry missing group attr: %s", entries[0].Message)
	}
}
