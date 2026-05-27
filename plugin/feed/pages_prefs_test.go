package feed_test

import (
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestSavePlayerPrefsEndpoint(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "bob", "pass")

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	sessionCookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrfToken := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	form := url.Values{
		"csrf_token":    {csrfToken},
		"speed":         {"1.5"},
		"skip_back_sec": {"10"},
		"skip_fwd_sec":  {"45"},
		"volume":        {"0.8"},
		"autoplay_next": {"false"},
		"skip_silence":  {"true"},
	}
	env.PostForm(t, "/apps/feed/player/prefs", form, sessionCookie).Status(http.StatusNoContent)

	store := feedStore(t, env, u.Username)
	got, err := store.LoadPlayerPrefs()
	require.NoError(t, err)
	if got.Speed != 1.5 {
		t.Errorf("Speed = %v, want 1.5", got.Speed)
	}
	if got.SkipBackSec != 10 || got.SkipFwdSec != 45 {
		t.Errorf("skip = %d/%d, want 10/45", got.SkipBackSec, got.SkipFwdSec)
	}
	if got.Volume != 0.8 {
		t.Errorf("Volume = %v, want 0.8", got.Volume)
	}
	if got.AutoplayNext {
		t.Errorf("AutoplayNext = true, want false")
	}
	if !got.SkipSilence {
		t.Errorf("SkipSilence = false, want true")
	}
}
