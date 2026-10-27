package calendar_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
)

// flashOf returns the message a redirect carries to the page it names.
func flashOf(t *testing.T, r *cogtest.Response) plugin.Flash {
	t.Helper()
	for _, c := range r.Cookies() {
		if c.Name != plugin.FlashCookieName {
			continue
		}
		v, err := url.QueryUnescape(c.Value)
		require.NoError(t, err)
		var f plugin.Flash
		require.NoError(t, json.Unmarshal([]byte(v), &f))
		return f
	}
	t.Fatal("no flash")
	return plugin.Flash{}
}

// TestSettingsFeeds makes, shows, renews and revokes a feed from the
// settings page; each form redirects back with a flash.
func TestSettingsFeeds(t *testing.T) {
	env, alice, _, _, cal := calEnv(t)
	cookie, csrf := sessionAuth(t, env, alice.ID)
	feedForm := func(kind, action string) *cogtest.Response {
		return env.PostForm(t, fmt.Sprintf("/apps/calendar/calendars/%d/feed", cal.ID),
			url.Values{"kind": {kind}, "action": {action}}, cookie, csrf, cogtest.StopRedirect).Status(303)
	}

	r := feedForm("full", "renew")
	assert.Equal(t, "/apps/calendar/settings", r.Header.Get("Location"))
	f := flashOf(t, r)
	assert.Equal(t, "success", f.Kind)
	require.True(t, strings.Contains(f.URL, "/remote.php/dav/feeds/"), f.URL)
	path := strings.TrimPrefix(f.URL, env.Providers.Config.BaseURL)
	env.Do(t, cogtest.NewRequest(t, "GET", f.URL, nil)).Status(200).Contains("X-WR-CALNAME:Personal")

	// The settings page shows it, copyable, with a webcal link.
	page := env.Get(t, "/apps/calendar/settings", cookie).Status(200).ReadBody()
	assert.Contains(t, page, f.URL)
	assert.Contains(t, page, `href="webcal://`+strings.TrimPrefix(strings.TrimPrefix(f.URL, "https://"), "http://")+`"`)

	// A new address replaces it.
	renewed := flashOf(t, feedForm("full", "renew"))
	assert.True(t, renewed.URL != f.URL)
	env.Get(t, path).Status(404)
	feedForm("full", "revoke")
	env.Do(t, cogtest.NewRequest(t, "GET", renewed.URL, nil)).Status(404)

	// Someone else's calendar is not found.
	bob := env.CreateUser(t, "bob", "pass")
	bobCookie, bobCSRF := sessionAuth(t, env, bob.ID)
	env.PostForm(t, fmt.Sprintf("/apps/calendar/calendars/%d/feed", cal.ID),
		url.Values{"kind": {"full"}, "action": {"renew"}}, bobCookie, bobCSRF, cogtest.StopRedirect).Status(404)
}

// TestSettingsSubscriptionsAndBirthdays subscribes and turns the birthday
// calendar on from the settings page, and checks their events cannot be
// edited in the web UI.
func TestSettingsSubscriptionsAndBirthdays(t *testing.T) {
	env, alice, cp, db, _ := calEnv(t)
	cookie, csrf := sessionAuth(t, env, alice.ID)
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//EN\r\nBEGIN:VEVENT\r\nUID:match-1\r\nDTSTAMP:20260101T000000Z\r\n" +
			"DTSTART:20261107T150000Z\r\nDTEND:20261107T170000Z\r\nSUMMARY:Match\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
	}))
	t.Cleanup(feed.Close)

	// Through safehttp, a feed on loopback is refused, and says why.
	r := env.PostForm(t, "/apps/calendar/subscriptions", url.Values{"url": {feed.URL}, "name": {"Team"}}, cookie, csrf, cogtest.StopRedirect).Status(303)
	f := flashOf(t, r)
	assert.Equal(t, "error", f.Kind)
	assert.Contains(t, f.Message, "blocked")
	r = env.PostForm(t, "/apps/calendar/subscriptions", url.Values{"url": {"gopher://x"}, "name": {"Team"}}, cookie, csrf, cogtest.StopRedirect).Status(303)
	assert.Contains(t, flashOf(t, r).Message, "webcal://")

	env.Providers.DAV.Fetch = feed.Client()
	r = env.PostForm(t, "/apps/calendar/subscriptions", url.Values{"url": {feed.URL}, "name": {"Team"}, "color": {"#ff0000"}}, cookie, csrf, cogtest.StopRedirect).Status(303)
	f = flashOf(t, r)
	assert.Equal(t, "success", f.Kind)
	assert.Contains(t, f.Message, "1 events")
	cals, err := cp.Calendars(alice.ID)
	require.NoError(t, err)
	var team *calendar.Calendar
	for i := range cals {
		if cals[i].Kind == dav.KindSubscription {
			team = &cals[i]
		}
	}
	require.True(t, team != nil && team.ReadOnly)
	page := env.Get(t, "/apps/calendar/settings", cookie).Status(200).ReadBody()
	assert.Contains(t, page, "<code>"+feed.URL+"</code>")
	r = env.PostForm(t, fmt.Sprintf("/apps/calendar/subscriptions/%d/refresh", team.ID), nil, cookie, csrf, cogtest.StopRedirect).Status(303)
	assert.Contains(t, flashOf(t, r).Message, "Refreshed Team")

	// Its events show, and cannot be edited, moved into or attached to.
	events, err := calendar.ListEvents(db, team.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(events))
	detail := fmt.Sprintf("/apps/calendar/events/%d", events[0].ID)
	modal := env.Get(t, detail, cookie).Status(200).ReadBody()
	assert.Contains(t, modal, "subscribed calendar")
	assert.NotContains(t, modal, "Edit Event")
	env.PostForm(t, detail, url.Values{"summary": {"Mine"}, "datetime_start": {"2026-11-07T15:00"}, "datetime_end": {"2026-11-07T16:00"}}, cookie, csrf, cogtest.StopRedirect).Status(403)
	env.Delete(t, detail, cookie, csrf).Status(403)
	env.PostForm(t, "/apps/calendar/events", url.Values{"calendar_id": {fmt.Sprint(team.ID)}, "summary": {"x"}, "datetime_start": {"2026-04-01T10:00"}, "datetime_end": {"2026-04-01T11:00"}}, cookie, csrf, cogtest.StopRedirect).Status(403)
	// The create form does not offer it.
	index := env.Get(t, "/apps/calendar/", cookie).Status(200).ReadBody()
	assert.NotContains(t, index, fmt.Sprintf(`<option value="%d" data-color`, team.ID))

	// Birthdays: on, then off.
	r = env.PostForm(t, "/apps/calendar/birthdays", url.Values{"enable": {"1"}}, cookie, csrf, cogtest.StopRedirect).Status(303)
	assert.Equal(t, "success", flashOf(t, r).Kind)
	bc, err := env.Providers.DAV.Birthdays(alice.ID)
	require.NoError(t, err)
	require.True(t, bc != nil)
	assert.Contains(t, env.Get(t, "/apps/calendar/settings", cookie).Status(200).ReadBody(), "Turn off")
	env.PostForm(t, "/apps/calendar/birthdays", url.Values{"enable": {"0"}}, cookie, csrf, cogtest.StopRedirect).Status(303)
	bc, err = env.Providers.DAV.Birthdays(alice.ID)
	require.NoError(t, err)
	assert.True(t, bc == nil)
}
