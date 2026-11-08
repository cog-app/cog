package feed_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// talksServer serves a conference page under /conf/2026/ linking to talk
// pages of every sort ScanTalksPage must tell apart.
func talksServer(t *testing.T) *httptest.Server {
	t.Helper()
	state := base64.StdEncoding.EncodeToString([]byte(
		`{"talk":{"note":"a \"quoted\" word","audio":[{"mediaUrl":"https://cdn.example.com/state.mp3"}]}}`))
	pages := map[string]string{
		"/conf/2026/": `<html><head><title>Spring Conference</title></head><body>
			<a href="/conf/2026/tag">Audio tag</a>
			<a href="mp3-link?x=1#top">MP3 link</a>
			<a href="/conf/2026/state">State</a>
			<a href="/conf/2026/none">None</a>
			<a href="/conf/2026/missing">Missing</a>
			<a href="/conf/2026/xss">XSS</a>
			<a href="/conf/2026/tag">Audio tag again</a>
			<a href="/conf/2026/saturday-session">Session</a>
			<a href="/conf/2026/">Self</a>
			<a href="/elsewhere/talk">Outside the path</a>
			<a href="https://other.example.com/conf/2026/tag">Other host</a>
		</body></html>`,
		"/conf/2026/tag": `<html><body><h1>Faith</h1><p class="author-name">Elder A</p>
			<audio><source src="/media/faith.mp3"></audio><video src="/media/faith.mp4"></video></body></html>`,
		"/conf/2026/mp3-link": `<html><body><h1>Hope</h1><a href="https://cdn.example.com/hope.mp3">Download</a></body></html>`,
		"/conf/2026/state": `<html><body><h1>Charity</h1><script>window.__INITIAL_STATE__="` + state + `";</script></body></html>`,
		"/conf/2026/none":  `<html><body><h1>Silence</h1><p>Transcript only.</p></body></html>`,
		"/conf/2026/xss":   `<html><body><h1>&lt;img src=x onerror=alert(1)&gt;</h1><video src="/media/x.mp4"></video></body></html>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestScanTalksPage(t *testing.T) {
	srv := talksServer(t)
	scan, err := feed.ScanTalksPage(context.Background(), srv.URL+"/conf/2026/")
	require.NoError(t, err)

	assert.Equal(t, "Spring Conference", scan.Title)
	var got []string
	for _, tk := range scan.Talks {
		got = append(got, fmt.Sprintf("%s|%s|%s|%s", strings.TrimPrefix(tk.PageURL, srv.URL), tk.Title, tk.Type, strings.TrimPrefix(tk.MediaURL, srv.URL)))
	}
	assert.Equal(t, strings.Join([]string{
		"/conf/2026/tag|Faith|audio|/media/faith.mp3",
		"/conf/2026/mp3-link|Hope|audio|https://cdn.example.com/hope.mp3",
		"/conf/2026/state|Charity|audio|https://cdn.example.com/state.mp3",
		"/conf/2026/none|Silence||",
		"/conf/2026/xss|<img src=x onerror=alert(1)>|video|/media/x.mp4",
	}, "\n"), strings.Join(got, "\n"))
	assert.Equal(t, "Elder A", scan.Talks[0].Speaker)

	require.Equal(t, 1, len(scan.Failed))
	assert.Equal(t, srv.URL+"/conf/2026/missing", scan.Failed[0].URL)
	assert.Contains(t, scan.Failed[0].Err, "404")
}

func TestScanTalksPageRefusesOtherSchemes(t *testing.T) {
	_, err := feed.ScanTalksPage(context.Background(), "file:///etc/passwd")
	require.True(t, err != nil, "file URL was scanned")
}

// talksSession signs u in and returns the cookie and CSRF token forms need.
func talksSession(t *testing.T, env *cogtest.Env, u *cogtest.User) (cogtest.HTTPOpt, string) {
	t.Helper()
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	return cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID), auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
}

func TestTalksPreviewEscapesAndReportsFailures(t *testing.T) {
	srv := talksServer(t)
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	cookie, token := talksSession(t, env, u)

	resp := env.PostForm(t, "/apps/feed/talks/preview", url.Values{
		"csrf_token": {token},
		"url":        {srv.URL + "/conf/2026/"},
	}, cookie)
	resp.Status(http.StatusOK).
		Contains(`name="talk" value="0" checked`).
		Contains("No media found").
		Contains("1 talk page could not be loaded").
		Contains("/conf/2026/missing").
		Contains("&lt;img src=x onerror=alert(1)&gt;").
		NotContains("<img src=x")

	// A page that cannot be read says why, escaped.
	env.PostForm(t, "/apps/feed/talks/preview", url.Values{
		"csrf_token": {token},
		"url":        {srv.URL + "/conf/<b>gone</b>/"},
	}, cookie).Status(http.StatusOK).
		Contains("Could not read that page").
		NotContains("<b>gone</b>")
}

// talkForm is the form a preview posts back, choosing the talks in pick.
func talkForm(token, pageURL string, talks []feed.PageTalk, pick ...int) url.Values {
	v := url.Values{"csrf_token": {token}, "page_url": {pageURL}, "title": {"Spring Conference"}}
	for i, tk := range talks {
		n := fmt.Sprint(i)
		v.Set("page_"+n, tk.PageURL)
		v.Set("title_"+n, tk.Title)
		v.Set("speaker_"+n, tk.Speaker)
		v.Set("media_"+n, tk.MediaURL)
		v.Set("type_"+n, tk.Type)
	}
	for _, i := range pick {
		v.Add("talk", fmt.Sprint(i))
	}
	return v
}

var sampleTalks = []feed.PageTalk{
	{PageURL: "https://conf.example.com/2026/a", Title: "A", Type: "audio", MediaURL: "https://cdn.example.com/a.mp3"},
	{PageURL: "https://conf.example.com/2026/b", Title: "B", Type: "audio", MediaURL: "https://cdn.example.com/b.mp3"},
	{PageURL: "https://conf.example.com/2026/c", Title: "C", Type: "video", MediaURL: "https://cdn.example.com/c.mp4"},
}

const samplePage = "https://conf.example.com/2026/"

func TestAddTalksAddsOnlyChosen(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	cookie, token := talksSession(t, env, u)
	store := feedStore(t, env, u.Username)

	resp := env.PostForm(t, "/apps/feed/talks", talkForm(token, samplePage, sampleTalks, 0, 2), cookie, cogtest.StopRedirect)
	resp.Status(http.StatusSeeOther)

	subs, err := store.ListSubscriptions()
	require.NoError(t, err)
	require.Equal(t, 1, len(subs))
	sub := subs[0]
	assert.Equal(t, "url", sub.Kind)
	assert.True(t, sub.Paused, "a page of talks must not be polled")
	assert.Equal(t, "Spring Conference", sub.Title)
	assert.Equal(t, fmt.Sprintf("/apps/feed/subscription/%d", sub.ID), resp.Header.Get("Location"))

	items, err := store.ListItemsBySubscription(sub.ID, 10)
	require.NoError(t, err)
	var got []string
	for _, it := range items {
		d, err := store.GetDownload(it.ID)
		require.NoError(t, err)
		got = append(got, it.Title+"|"+it.Type+"|"+d.State)
	}
	// Listed newest first, which is the page's order.
	assert.Equal(t, "A|audio|queued C|video|queued", strings.Join(got, " "))
}

func TestAddTalksWithNoneChosenAddsNothing(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "bob", "pass")
	cookie, token := talksSession(t, env, u)
	store := feedStore(t, env, u.Username)

	env.PostForm(t, "/apps/feed/talks", talkForm(token, samplePage, sampleTalks), cookie, cogtest.StopRedirect).
		Status(http.StatusSeeOther)

	subs, err := store.ListSubscriptions()
	require.NoError(t, err)
	assert.Equal(t, 0, len(subs))
}

func TestAddTalksAgainAddsOnlyNew(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "carol", "pass")
	cookie, token := talksSession(t, env, u)
	store := feedStore(t, env, u.Username)

	env.PostForm(t, "/apps/feed/talks", talkForm(token, samplePage, sampleTalks, 0), cookie, cogtest.StopRedirect).
		Status(http.StatusSeeOther)
	env.PostForm(t, "/apps/feed/talks", talkForm(token, samplePage, sampleTalks, 0, 1), cookie, cogtest.StopRedirect).
		Status(http.StatusSeeOther)

	subs, err := store.ListSubscriptions()
	require.NoError(t, err)
	require.Equal(t, 1, len(subs))
	items, err := store.ListItemsBySubscription(subs[0].ID, 10)
	require.NoError(t, err)
	assert.Equal(t, 2, len(items))
}

func TestAddTalksRefusesAFeedsAddress(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "dave", "pass")
	cookie, token := talksSession(t, env, u)
	store := feedStore(t, env, u.Username)
	subID, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: samplePage})
	require.NoError(t, err)

	env.PostForm(t, "/apps/feed/talks", talkForm(token, samplePage, sampleTalks, 0), cookie, cogtest.StopRedirect).
		Status(http.StatusSeeOther)

	items, err := store.ListItemsBySubscription(subID, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, len(items))
}

func TestAddTalksRefusesTalkWithoutMedia(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "erin", "pass")
	cookie, token := talksSession(t, env, u)
	store := feedStore(t, env, u.Username)

	talks := []feed.PageTalk{{PageURL: "https://conf.example.com/2026/x", Title: "X", MediaURL: "javascript:alert(1)", Type: "audio"}}
	env.PostForm(t, "/apps/feed/talks", talkForm(token, samplePage, talks, 0), cookie, cogtest.StopRedirect).
		Status(http.StatusSeeOther)

	subs, err := store.ListSubscriptions()
	require.NoError(t, err)
	assert.Equal(t, 0, len(subs))
}
