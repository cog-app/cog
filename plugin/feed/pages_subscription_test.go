package feed_test

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// TestWebSubscriptionSettingsTitle checks the settings form makes a title the
// user's only when they change it: saving the other settings leaves the feed's
// title following the feed.
func TestWebSubscriptionSettingsTitle(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	id, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "Feed"})
	require.NoError(t, err)
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	post := func(form url.Values) {
		t.Helper()
		form.Set("csrf_token", auth.CSRFToken(sess.ID, env.Providers.CSRFSecret))
		env.PostForm(t, fmt.Sprintf("/apps/feed/subscription/%d/settings", id), form,
			cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID), cogtest.StopRedirect,
		).Status(http.StatusSeeOther)
	}
	title := func() string {
		t.Helper()
		sub, err := store.GetSubscription(id)
		require.NoError(t, err)
		return sub.Title
	}

	post(url.Values{"title": {"Feed"}, "poll_interval_sec": {"900"}})
	require.NoError(t, store.UpdateSubscriptionMetadata(id, map[string]string{"title": "Feed, renamed"}))
	assert.Equal(t, "Feed, renamed", title())

	post(url.Values{"title": {" Mine "}})
	require.NoError(t, store.UpdateSubscriptionMetadata(id, map[string]string{"title": "Feed again"}))
	assert.Equal(t, "Mine", title())
	sub, err := store.GetSubscription(id)
	require.NoError(t, err)
	assert.Equal(t, int64(900), sub.PollIntervalSec)

	post(url.Values{"title": {""}})
	require.NoError(t, store.UpdateSubscriptionMetadata(id, map[string]string{"title": "Feed again"}))
	assert.Equal(t, "Feed again", title())
}
