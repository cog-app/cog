package feed_test

import (
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestEnsureWhisperModelSkipsIfPresent(t *testing.T) {
	dir := t.TempDir()
	// Pre-create the file with garbage; the function should NOT re-download.
	path := filepath.Join(dir, "ggml-base.en.bin")
	require.NoError(t, os.WriteFile(path, []byte("fake"), 0o644))

	got, err := feed.EnsureWhisperModel(dir, feed.WhisperBaseEn)
	require.NoError(t, err)
	if got != path {
		t.Fatalf("got %q want %q", got, path)
	}
	// File should be untouched.
	b, _ := os.ReadFile(got)
	if string(b) != "fake" {
		t.Fatalf("file was overwritten: %q", string(b))
	}
}

func TestEnsureWhisperModelRejectsUnknown(t *testing.T) {
	if _, err := feed.EnsureWhisperModel(t.TempDir(), feed.WhisperModel("bogus")); err == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestWhisperModelFile(t *testing.T) {
	if feed.WhisperModelFile(feed.WhisperBaseEn) != "ggml-base.en.bin" {
		t.Fatal("wrong filename for base.en")
	}
	if feed.WhisperModelFile(feed.WhisperNone) != "" {
		t.Fatal("none should map to empty string")
	}
}
