package pipeline_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
)

type copyProcessor struct{}

func (copyProcessor) Process(_ context.Context, in, out string) error {
	src, err := os.Open(in)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(out)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return err
}

func TestProfanityWorker(t *testing.T) {
	s := newPipelineStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))

	dir := t.TempDir()
	src := filepath.Join(dir, "in.mp3")
	require.NoError(t, os.WriteFile(src, []byte("audio bytes"), 0o644))
	require.NoError(t, s.MarkDownloadReady(id, src, 11))
	require.NoError(t, s.EnqueueProfanity(id))

	w := pipeline.NewProfanity(s, copyProcessor{})
	require.NoError(t, w.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.CleanAudioPath == "" {
		t.Fatal("clean_audio_path not set")
	}
	if _, err := os.Stat(d.CleanAudioPath); err != nil {
		t.Fatalf("clean file missing: %v", err)
	}
	if d.NeedsProfanity {
		t.Fatal("needs_profanity not cleared")
	}
}

func TestProfanityWorkerSkipsNotReady(t *testing.T) {
	s := newPipelineStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, s.EnqueueProfanity(id)) // queued state, no download yet

	w := pipeline.NewProfanity(s, copyProcessor{})
	require.NoError(t, w.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.CleanAudioPath != "" {
		t.Fatal("clean_audio_path should not be set for non-ready download")
	}
}
