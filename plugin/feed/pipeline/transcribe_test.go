package pipeline_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
)

func TestTranscribeYouTubeCaptions(t *testing.T) {
	abs, _ := filepath.Abs("testdata")
	t.Setenv("PATH", abs+":"+os.Getenv("PATH"))

	s, space := newPipeline(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "youtube", URL: "https://yt/c1"})
	id, _, _ := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "v1", Type: "video",
		MediaURL: "https://www.youtube.com/watch?v=v1",
	})
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))
	require.NoError(t, s.MarkDownloadReady(id, "/dev/null", 1))
	require.NoError(t, s.EnqueueTranscribe(id))

	w := pipeline.NewTranscribe(s, space)
	require.NoError(t, w.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.CaptionsPath == "" {
		t.Fatal("captions_path not set")
	}
	if _, err := space.Own.Stat(d.CaptionsPath); err != nil {
		t.Fatalf("captions file missing: %v", err)
	}
	if d.NeedsTranscribe {
		t.Fatal("flag not cleared")
	}
}

func TestTranscribeWhisperFallback(t *testing.T) {
	abs, _ := filepath.Abs("testdata")
	t.Setenv("PATH", abs+":"+os.Getenv("PATH"))

	s, space := newPipeline(t)
	// RSS subscription -> always falls back to whisper.
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))

	// Use a real local file so whisper-cli stub can pretend to process it.
	_, err := space.Own.Put("media/in.wav", strings.NewReader("dummy"))
	require.NoError(t, err)
	require.NoError(t, s.MarkDownloadReady(id, "media/in.wav", 5))
	require.NoError(t, s.EnqueueTranscribe(id))

	// Pre-create the model file so EnsureWhisperModel doesn't try to download.
	modelsDir := filepath.Join(t.TempDir(), "models")
	require.NoError(t, os.MkdirAll(modelsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(modelsDir, "ggml-base.en.bin"), []byte("fake"), 0o644))
	require.NoError(t, s.SetSetting("whisper_models_dir", modelsDir))

	w := pipeline.NewTranscribe(s, space)
	require.NoError(t, w.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.CaptionsPath == "" {
		t.Fatal("captions_path not set")
	}
	if _, err := space.Own.Stat(d.CaptionsPath); err != nil {
		t.Fatalf("captions file missing: %v", err)
	}
	if d.NeedsTranscribe {
		t.Fatal("flag not cleared")
	}
}

func TestTranscribeNoneSkips(t *testing.T) {
	s, space := newPipeline(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))
	require.NoError(t, s.MarkDownloadReady(id, "/tmp/x", 1))
	require.NoError(t, s.EnqueueTranscribe(id))
	require.NoError(t, s.SetSetting("whisper_model", "none"))

	w := pipeline.NewTranscribe(s, space)
	require.NoError(t, w.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.CaptionsPath != "" {
		t.Errorf("captions should remain empty: %q", d.CaptionsPath)
	}
	if d.NeedsTranscribe {
		t.Error("flag should be cleared even when skipping")
	}
}

// A transcription failure (here whisper-cli missing) is recorded, so what
// waits on the captions can stop waiting; the flag stays, and the next tick
// tries again.
func TestTranscribeRecordsError(t *testing.T) {
	s, space := newPipeline(t)
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	require.NoError(t, err)
	id, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, err)
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))
	_, err = space.Own.Put("media/in.wav", strings.NewReader("dummy"))
	require.NoError(t, err)
	require.NoError(t, s.MarkDownloadReady(id, "media/in.wav", 5))
	require.NoError(t, s.EnqueueTranscribe(id))
	modelsDir := filepath.Join(t.TempDir(), "models")
	require.NoError(t, os.MkdirAll(modelsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(modelsDir, "ggml-base.en.bin"), []byte("fake"), 0o644))
	require.NoError(t, s.SetSetting("whisper_models_dir", modelsDir))
	require.NoError(t, s.SetSetting("whisper_cli_path", filepath.Join(t.TempDir(), "no-whisper-cli")))

	require.NoError(t, pipeline.NewTranscribe(s, space).RunOnce(context.Background()))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Contains(t, d.TranscribeError, "whisper-cli")
	assert.True(t, d.NeedsTranscribe)
}
