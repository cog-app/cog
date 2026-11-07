package web_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/files"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestSignedOutHTMXRedirectsPage verifies that an HTMX request made after the
// session ended navigates the whole page to the login form, back to the page
// it was made from, instead of following a redirect and swapping the login
// form into the element it was filling.
func TestSignedOutHTMXRedirectsPage(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	for _, p := range []string{
		"/settings/profile",   // RequireAuthWeb
		"/apps/files/",        // a plugin page
		"/api/search/partial", // RequireAuth
	} {
		resp := env.Get(t, p, cogtest.StopRedirect,
			cogtest.Header("HX-Request", "true"),
			cogtest.Header("HX-Current-URL", env.Providers.Config.BaseURL+"/apps/notes/?id=4"))
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		require.Equal(t, "/login?next=%2Fapps%2Fnotes%2F%3Fid%3D4", resp.Header.Get("HX-Redirect"))
	}

	// Without the current URL, or with another site's, it falls back to the
	// request's own.
	resp := env.Get(t, "/settings/profile", cogtest.StopRedirect, cogtest.Header("HX-Request", "true"))
	require.Equal(t, "/login?next=%2Fsettings%2Fprofile", resp.Header.Get("HX-Redirect"))
	resp = env.Get(t, "/settings/profile", cogtest.StopRedirect, cogtest.Header("HX-Request", "true"),
		cogtest.Header("HX-Current-URL", "https://elsewhere.invalid/apps/notes/"))
	require.Equal(t, "/login?next=%2Fsettings%2Fprofile", resp.Header.Get("HX-Redirect"))
}

type trayFeed struct {
	Items []struct {
		ID      int64  `json:"id"`
		App     string `json:"app"`
		Type    string `json:"type"`
		Subject string `json:"subject"`
		Own     bool   `json:"own"`
		Notify  bool   `json:"notify"`
	} `json:"items"`
	Apps []string `json:"apps"`
}

func TestActivityFeed(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.CreateUser(t, "bob", "pass")

	acts := env.Providers.Activities
	require.NoError(t, acts.Log("alice", "alice", "files", "file_created", "alice uploaded a.txt", "files", "1", "/a.txt"))
	require.NoError(t, acts.Notification("alice", "bob", "sharing", "share_received", "bob shared b.txt", "files:file", "2", "b.txt"))
	require.NoError(t, acts.Log("bob", "bob", "files", "file_created", "bob uploaded c.txt", "files", "3", "/c.txt"))

	var got trayFeed
	env.Get(t, "/api/activity?apps=1", alice.Auth).Status(http.StatusOK).JSON(&got)
	require.Equal(t, 2, len(got.Items))
	require.Equal(t, "bob shared b.txt", got.Items[0].Subject)
	require.Equal(t, false, got.Items[0].Own)
	require.Equal(t, true, got.Items[0].Notify)
	require.Equal(t, "share_received", got.Items[0].Type)
	require.Equal(t, "alice uploaded a.txt", got.Items[1].Subject)
	require.Equal(t, true, got.Items[1].Own)
	require.Equal(t, false, got.Items[1].Notify)
	require.Equal(t, []string{"files", "sharing"}, got.Apps)

	got = trayFeed{}
	env.Get(t, "/api/activity?filter=notifications", alice.Auth).Status(http.StatusOK).JSON(&got)
	require.Equal(t, 1, len(got.Items))
	require.Equal(t, "bob shared b.txt", got.Items[0].Subject)
	require.Equal(t, 0, len(got.Apps))

	got = trayFeed{}
	env.Get(t, "/api/activity?filter=app:files", alice.Auth).Status(http.StatusOK).JSON(&got)
	require.Equal(t, 1, len(got.Items))
	require.Equal(t, "alice uploaded a.txt", got.Items[0].Subject)

	env.Get(t, "/api/activity?filter=bogus", alice.Auth).Status(http.StatusBadRequest)
	env.Get(t, "/api/activity").Status(http.StatusUnauthorized)
}

// TestShareNotifiesRecipients verifies that receiving a share is a
// notification for its user, or for every member of its group but the owner.
func TestShareNotifiesRecipients(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	carol := env.CreateUser(t, "carol", "pass")

	plug, ok := env.Providers.Plugins.Plugin(files.PluginID)
	require.True(t, ok, "files plugin loaded")
	fp := plug.(*files.Plugin)
	meta, err := fp.Upload("alice", "/report.txt", strings.NewReader("hi"))
	require.NoError(t, err)
	dir, err := fp.CreateDir("alice", "/Photos")
	require.NoError(t, err)

	_, err = env.Providers.Shares.Create(alice.ID, "files:file", strconv.FormatInt(meta.ID, 10), "USER", bob.ID, plugin.PermRead)
	require.NoError(t, err)

	g, err := env.Providers.Groups.Create("Family", nil, &alice.ID)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, carol.ID, group.User))
	_, err = env.Providers.Shares.Create(alice.ID, "files:directory", strconv.FormatInt(dir.ID, 10), "GROUP", g.ID, plugin.PermRead)
	require.NoError(t, err)

	var got trayFeed
	env.Get(t, "/api/activity?filter=notifications", bob.Auth).Status(http.StatusOK).JSON(&got)
	require.Equal(t, 1, len(got.Items))
	require.Equal(t, `alice shared the file "report.txt" with you`, got.Items[0].Subject)

	got = trayFeed{}
	env.Get(t, "/api/activity?filter=notifications", carol.Auth).Status(http.StatusOK).JSON(&got)
	require.Equal(t, 1, len(got.Items))
	require.Equal(t, `alice shared the folder "Photos" with Family`, got.Items[0].Subject)

	// The owner, a member of the group, is not told of their own share.
	got = trayFeed{}
	env.Get(t, "/api/activity?filter=notifications", alice.Auth).Status(http.StatusOK).JSON(&got)
	require.Equal(t, 0, len(got.Items))
}

// TestSignedOutPageScriptGetsNoChallenge verifies that a script on a page is
// refused without a Basic challenge, which would have the browser prompt for
// a password over the page, while API clients still get one.
func TestSignedOutPageScriptGetsNoChallenge(t *testing.T) {
	env := cogtest.New(t)

	resp := env.Get(t, "/api/activity",
		cogtest.Header("Sec-Fetch-Site", "same-origin"),
		cogtest.Header("Sec-Fetch-Mode", "cors")).Status(http.StatusUnauthorized)
	require.Equal(t, "", resp.Header.Get("WWW-Authenticate"))

	resp = env.Get(t, "/api/activity").Status(http.StatusUnauthorized)
	require.Equal(t, `Basic realm="Cog"`, resp.Header.Get("WWW-Authenticate"))
}
