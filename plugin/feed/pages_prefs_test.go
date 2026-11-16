package feed_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// TestHideEmptyToggleViaHTTP posts the hide_empty toggle over HTTP with a valid
// session and CSRF token, then asserts the pref persisted in the store.
func TestHideEmptyToggleViaHTTP(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	sessionCookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrfToken := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	form := url.Values{
		"csrf_token": {csrfToken},
		"hide_empty": {"1"},
	}
	// With HX-Target: feed-sidebar the handler returns the refreshed sidebar (200).
	hxTarget := cogtest.Header("HX-Target", "feed-sidebar")
	resp := env.PostForm(t, "/apps/feed/player/prefs", form, sessionCookie, hxTarget)
	resp.Status(http.StatusOK)
	resp.Contains("feed-sidebar")

	store := feedStore(t, env, u.Username)
	got, err := store.LoadPlayerPrefs()
	require.NoError(t, err)
	if !got.HideEmpty {
		t.Fatal("hide_empty pref did not persist after HTTP toggle")
	}
}

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

func TestSavePlayerPrefsFormSkipSilenceMin(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "carol", "pass")
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	sessionCookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrfToken := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	store := feedStore(t, env, u.Username)

	// NaN and Inf parse as floats but are ignored, like any unparseable
	// value: the stored 3 stays.
	for _, value := range []string{"3", "NaN", "Inf", "-Inf", "x"} {
		post := func(value string) {
			env.PostForm(t, "/apps/feed/player/prefs", url.Values{
				"csrf_token":           {csrfToken},
				"skip_silence_min_sec": {value},
			}, sessionCookie).Status(http.StatusNoContent)
		}
		post(value)
		got, err := store.LoadPlayerPrefs()
		require.NoError(t, err)
		if got.SkipSilenceMinSec != 3 {
			t.Errorf("skip_silence_min_sec=%s stored %g, want 3", value, got.SkipSilenceMinSec)
		}
	}
	for value, want := range map[string]float64{"99": 10, "0.1": 0.5} {
		env.PostForm(t, "/apps/feed/player/prefs", url.Values{
			"csrf_token":           {csrfToken},
			"skip_silence_min_sec": {value},
		}, sessionCookie).Status(http.StatusNoContent)
		got, err := store.LoadPlayerPrefs()
		require.NoError(t, err)
		if got.SkipSilenceMinSec != want {
			t.Errorf("skip_silence_min_sec=%s stored %g, want %g", value, got.SkipSilenceMinSec, want)
		}
	}
}

func TestSidebarHidesEmptyWhenEnabled(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	if !ok {
		t.Fatal("feed plugin not registered")
	}
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)

	withItems, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://a.test/f", Title: "HasUnread"})
	require.NoError(t, err)
	_, err = s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://b.test/f", Title: "EmptySub"})
	require.NoError(t, err)
	_, _, err = s.UpsertItem(feed.Item{SubscriptionID: withItems, GUID: "g1", Type: "text", Title: "x"})
	require.NoError(t, err)

	p, err := s.LoadPlayerPrefs()
	require.NoError(t, err)
	p.HideEmpty = true
	require.NoError(t, s.SavePlayerPrefs(p))

	resp := env.Get(t, "/apps/feed/", u.Auth)
	resp.Status(200)
	body := resp.ReadBody()
	if !strings.Contains(body, "HasUnread") {
		t.Fatal("subscription with items should appear in sidebar")
	}
	if strings.Contains(body, "EmptySub") {
		t.Fatal("empty subscription should be hidden when hide_empty enabled")
	}
}

func TestHideEmptyPrefRoundTrips(t *testing.T) {
	s := newStore(t)
	p, err := s.LoadPlayerPrefs()
	require.NoError(t, err)
	if p.HideEmpty {
		t.Fatal("hide_empty should default to false")
	}
	p.HideEmpty = true
	require.NoError(t, s.SavePlayerPrefs(p))
	got, err := s.LoadPlayerPrefs()
	require.NoError(t, err)
	if !got.HideEmpty {
		t.Fatal("hide_empty did not persist")
	}
}
