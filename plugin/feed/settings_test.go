package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestGetSetSetting(t *testing.T) {
	s := newStore(t)
	if got := s.GetSetting("whisper_model", "base.en"); got != "base.en" {
		t.Fatalf("default not returned: %q", got)
	}
	require.NoError(t, s.SetSetting("whisper_model", "small.en"))
	if got := s.GetSetting("whisper_model", "base.en"); got != "small.en" {
		t.Fatalf("got %q want small.en", got)
	}
}

type whisperModel string

const (
	whisperBaseEn  whisperModel = "base.en"
	whisperSmallEn whisperModel = "small.en"
)

func TestTypedSetting(t *testing.T) {
	s := newStore(t)
	db := s.DB()

	setting := feed.StringSetting("whisper_model", whisperBaseEn)

	got := setting.MustGet(db)
	if got != whisperBaseEn {
		t.Fatalf("default: %q", got)
	}
	require.NoError(t, setting.Set(db, whisperSmallEn))
	got = setting.MustGet(db)
	if got != whisperSmallEn {
		t.Fatalf("got %q", got)
	}
}

func TestIntSetting(t *testing.T) {
	s := newStore(t)
	db := s.DB()

	setting := feed.IntSetting("poll_workers", 4)

	if got := setting.MustGet(db); got != 4 {
		t.Fatalf("default: %d", got)
	}
	require.NoError(t, setting.Set(db, 8))
	if got := setting.MustGet(db); got != 8 {
		t.Fatalf("got %d want 8", got)
	}
}

func TestBoolSetting(t *testing.T) {
	s := newStore(t)
	db := s.DB()

	setting := feed.BoolSetting("auto_download", false)

	if got := setting.MustGet(db); got != false {
		t.Fatalf("default: %v", got)
	}
	require.NoError(t, setting.Set(db, true))
	if got := setting.MustGet(db); got != true {
		t.Fatalf("got %v want true", got)
	}
}

func TestSettingsRedirectsToSubscriptions(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	resp := env.Get(t, "/apps/feed/settings", u.Auth, cogtest.StopRedirect)
	resp.Status(303)
	if loc := resp.Header.Get("Location"); loc != "/apps/feed/settings/subscriptions" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestSettingsPagesRender(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	for _, p := range []string{"subscriptions", "filters", "playlists"} {
		resp := env.Get(t, "/apps/feed/settings/"+p, u.Auth)
		resp.Status(200)
		resp.Contains("feed-sidebar")
	}
}
