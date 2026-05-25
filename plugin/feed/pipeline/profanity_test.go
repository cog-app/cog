package pipeline_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
)

// captureProcessor records what it was given and just copies in->out.
type captureProcessor struct {
	gotCaptions []cogplugin.CaptionSegment
}

func (c *captureProcessor) Process(_ context.Context, in, out string, captions []cogplugin.CaptionSegment) error {
	c.gotCaptions = captions
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

func TestProfanityWorkerUsesCaptions(t *testing.T) {
	s := newPipelineStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))

	dir := t.TempDir()
	src := filepath.Join(dir, "in.mp3")
	require.NoError(t, os.WriteFile(src, []byte("audio bytes"), 0o644))
	require.NoError(t, s.MarkDownloadReady(id, src, 11))

	// Captions file.
	vtt := filepath.Join(dir, "g.vtt")
	require.NoError(t, os.WriteFile(vtt, []byte(`WEBVTT

00:00:00.000 --> 00:00:02.000
hello world
`), 0o644))
	require.NoError(t, s.SetCaptionsPath(id, vtt))
	require.NoError(t, s.EnqueueProfanity(id))

	proc := &captureProcessor{}
	w := pipeline.NewProfanity(s, proc)
	require.NoError(t, w.RunOnce(context.Background()))

	if len(proc.gotCaptions) == 0 {
		t.Fatal("processor did not receive captions")
	}
	d, _ := s.GetDownload(id)
	if d.CleanAudioPath == "" {
		t.Fatal("clean_audio_path not set")
	}
	if d.NeedsProfanity {
		t.Fatal("needs_profanity not cleared")
	}
}

func TestProfanityWorkerSkipsWithoutCaptions(t *testing.T) {
	s := newPipelineStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))
	require.NoError(t, s.MarkDownloadReady(id, "/dev/null", 1))
	// No SetCaptionsPath — captions_path stays empty.
	require.NoError(t, s.EnqueueProfanity(id))

	proc := &captureProcessor{}
	w := pipeline.NewProfanity(s, proc)
	require.NoError(t, w.RunOnce(context.Background()))

	if len(proc.gotCaptions) != 0 {
		t.Error("processor should not have been called")
	}
	d, _ := s.GetDownload(id)
	if d.CleanAudioPath != "" {
		t.Error("clean_audio_path should remain empty")
	}
	if !d.NeedsProfanity {
		t.Error("needs_profanity flag should remain set so worker can retry")
	}
}
