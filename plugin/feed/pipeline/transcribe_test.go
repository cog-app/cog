package pipeline_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
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

	w := pipeline.NewTranscribe(s, space, whisperWith(feed.WhisperSettings{Model: feed.WhisperBaseEn}))
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

	w := pipeline.NewTranscribe(s, space, whisperWith(feed.WhisperSettings{Model: feed.WhisperBaseEn, ModelsDir: modelsDir}))
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

	w := pipeline.NewTranscribe(s, space, whisperWith(feed.WhisperSettings{Model: feed.WhisperNone}))
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
	whisper := whisperWith(feed.WhisperSettings{
		Model:     feed.WhisperBaseEn,
		ModelsDir: modelsDir,
		CLIPath:   filepath.Join(t.TempDir(), "no-whisper-cli"),
	})

	require.NoError(t, pipeline.NewTranscribe(s, space, whisper).RunOnce(context.Background()))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Contains(t, d.TranscribeError, "whisper-cli")
	assert.True(t, d.NeedsTranscribe)
}

// whisperWith is a transcribe worker's whisper settings that are always ws.
func whisperWith(ws feed.WhisperSettings) func() (feed.WhisperSettings, error) {
	return func() (feed.WhisperSettings, error) { return ws, nil }
}

// The whisper settings are the server's, saved by the admin: a different,
// non-admin user's worker transcribes with them, whatever that user's own
// database still holds from when they were per user.
func TestWhisperSettingsServerWide(t *testing.T) {
	env := cogtest.New(t, feed.New())
	admin := env.CreateUser(t, "admin", "pw")
	bob := env.CreateUser(t, "bob", "pw")
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok, "feed plugin not registered")
	p := raw.(*feed.Plugin)

	modelsDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(modelsDir, "ggml-tiny.en.bin"), []byte("fake"), 0o644))
	cli, err := filepath.Abs("testdata/whisper-cli-model")
	require.NoError(t, err)
	sess, err := env.Providers.Sessions.Create(admin.ID)
	require.NoError(t, err)
	env.PostForm(t, "/admin/plugins/"+feed.PluginID, url.Values{
		"csrf_token":         {auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)},
		"whisper_model":      {string(feed.WhisperTinyEn)},
		"whisper_models_dir": {modelsDir},
		"whisper_cli_path":   {cli},
		"youtube_mode":       {string(feed.YouTubeDirect)},
	}, cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID), cogtest.StopRedirect).
		Status(http.StatusSeeOther)

	db, err := p.DB(bob.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	require.NoError(t, s.SetSetting("whisper_model", string(feed.WhisperNone)))
	space, err := p.Space(bob.Username)
	require.NoError(t, err)
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

	require.NoError(t, pipeline.NewTranscribe(s, space, p.WhisperSettings).RunOnce(context.Background()))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	require.True(t, d.CaptionsPath != "", "no captions; transcribe error: %q", d.TranscribeError)
	f, err := space.Own.Open(feed.Name(d.CaptionsPath))
	require.NoError(t, err)
	defer f.Close()
	vtt, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Contains(t, string(vtt), "model "+filepath.Join(modelsDir, "ggml-tiny.en.bin"))
}
