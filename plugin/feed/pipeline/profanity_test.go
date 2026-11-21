package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
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
	s, space := newPipeline(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))

	local := fmt.Sprintf("media/%d.hls-audio.m4a", id)
	_, err := space.Own.Put(local, strings.NewReader("audio bytes"))
	require.NoError(t, err)
	require.NoError(t, s.MarkDownloadReady(id, local, 11))

	// Captions file.
	_, err = space.Own.Put("captions/g.vtt", strings.NewReader(`WEBVTT

00:00:00.000 --> 00:00:02.000
hello world
`))
	require.NoError(t, err)
	require.NoError(t, s.SetCaptionsPath(id, "captions/g.vtt"))
	require.NoError(t, s.EnqueueProfanity(id))

	proc := &captureProcessor{}
	w := pipeline.NewProfanity(s, space, proc)
	require.NoError(t, w.RunOnce(context.Background()))

	if len(proc.gotCaptions) == 0 {
		t.Fatal("processor did not receive captions")
	}
	d, _ := s.GetDownload(id)
	assert.Equal(t, fmt.Sprintf("media/%d.clean.m4a", id), d.CleanAudioPath)
	if got := readStored(t, space, d.CleanAudioPath); got != "audio bytes" {
		t.Fatalf("clean audio %q", got)
	}
	if d.NeedsProfanity {
		t.Fatal("needs_profanity not cleared")
	}
}

func TestProfanityWorkerSkipsWithoutCaptions(t *testing.T) {
	s, space := newPipeline(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))
	require.NoError(t, s.MarkDownloadReady(id, "/dev/null", 1))
	// No SetCaptionsPath — captions_path stays empty.
	require.NoError(t, s.EnqueueProfanity(id))

	proc := &captureProcessor{}
	w := pipeline.NewProfanity(s, space, proc)
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

// noRanges is a Processor that finds nothing to mute.
type noRanges struct{ calls int }

func (n *noRanges) Process(context.Context, string, string, []cogplugin.CaptionSegment) error {
	n.calls++
	return fmt.Errorf("wrapped: %w", profanity.ErrNoRanges)
}

// noRangesItem makes an item with captions, a ready download and a queued
// profanity pass.
func noRangesItem(t *testing.T, s *feed.Store, space feed.Space) int64 {
	t.Helper()
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	require.NoError(t, err)
	id, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, err)
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))
	local := fmt.Sprintf("media/%d.hls-audio.m4a", id)
	_, err = space.Own.Put(local, strings.NewReader("audio bytes"))
	require.NoError(t, err)
	require.NoError(t, s.MarkDownloadReady(id, local, 11))
	_, err = space.Own.Put("captions/g.vtt", strings.NewReader("WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nhello world\n"))
	require.NoError(t, err)
	require.NoError(t, s.SetCaptionsPath(id, "captions/g.vtt"))
	require.NoError(t, s.EnqueueProfanity(id))
	return id
}

func TestProfanityWorkerNoRangesClearsFlag(t *testing.T) {
	s, space := newPipeline(t)
	id := noRangesItem(t, s, space)

	proc := &noRanges{}
	w := pipeline.NewProfanity(s, space, proc)
	require.NoError(t, w.RunOnce(context.Background()))
	require.NoError(t, w.RunOnce(context.Background()))

	assert.Equal(t, 1, proc.calls)
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.False(t, d.NeedsProfanity)
	assert.Equal(t, "", d.CleanAudioPath)
}

func TestProfanityWorkerNoRangesDropsOldClean(t *testing.T) {
	s, space := newPipeline(t)
	id := noRangesItem(t, s, space)
	old := fmt.Sprintf("media/%d.clean.m4a", id)
	_, err := space.Own.Put(old, strings.NewReader("old clean"))
	require.NoError(t, err)
	require.NoError(t, s.SetCleanAudioPath(id, old))

	w := pipeline.NewProfanity(s, space, &noRanges{})
	require.NoError(t, w.RunOnce(context.Background()))
	require.NoError(t, pipeline.Collect(s, space))

	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.CleanAudioPath)
	_, err = space.Own.Open(old)
	assert.True(t, errors.Is(err, fs.ErrNotExist), "open old clean: %v", err)
}

// failing is a Processor that fails.
type failing struct{}

func (failing) Process(context.Context, string, string, []cogplugin.CaptionSegment) error {
	return errors.New("tts: not configured")
}

// A muting failure is recorded, so what waits on muting can stop waiting;
// the flag stays, and the next tick tries again. A success clears it.
func TestProfanityWorkerRecordsError(t *testing.T) {
	s, space := newPipeline(t)
	id := noRangesItem(t, s, space)
	require.NoError(t, pipeline.NewProfanity(s, space, failing{}).RunOnce(context.Background()))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Contains(t, d.ProfanityError, "tts: not configured")
	assert.True(t, d.NeedsProfanity)

	require.NoError(t, pipeline.NewProfanity(s, space, &captureProcessor{}).RunOnce(context.Background()))
	d, err = s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.ProfanityError)
	assert.False(t, d.NeedsProfanity)
}
