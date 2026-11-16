package feed_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// seedBrowserFeed gives a user a realistic reader: two folders, four
// subscriptions (one failing, one with a title too long for the sidebar),
// text, audio and video items in mixed read/starred states, and item tags.
//
// Publication times are fixed dates in a past year so the rendered dates
// ("2025-…") do not depend on the wall clock. No subscription has a favicon,
// so nothing is fetched from the network. It returns the article that the
// reader view opens.
func seedBrowserFeed(t *testing.T, env *cogtest.Env, username string) (articleID int64) {
	t.Helper()
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok, "feed plugin not registered")
	db, err := raw.(*feed.Plugin).DB(username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	techID, err := store.CreateFolder("Tech News")
	require.NoError(t, err)
	podID, err := store.CreateFolder("Podcasts")
	require.NoError(t, err)

	blogID, err := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: "https://blog.example.com/feed.xml", Title: "Example Engineering Blog",
		FolderID: sql.NullInt64{Int64: techID, Valid: true},
	})
	require.NoError(t, err)
	longID, err := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: "https://long.example.org/rss",
		Title: "A Subscription With An Extremely Long Title That Will Not Fit In The Sidebar",
	})
	require.NoError(t, err)
	showID, err := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: "https://audio.example.net/podcast.xml", Title: "Weekly Audio Show",
		FolderID: sql.NullInt64{Int64: podID, Valid: true},
	})
	require.NoError(t, err)
	chanID, err := store.CreateSubscription(feed.Subscription{
		Kind: "youtube", URL: "https://www.youtube.com/channel/UCexample", Title: "Video Channel",
	})
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE subscriptions SET last_error = ?, error_count = ? WHERE id = ?`,
		"GET https://long.example.org/rss: 503 Service Unavailable", 3, longID)
	require.NoError(t, err)

	at := func(day int) int64 { return time.Date(2025, time.June, day, 9, 30, 0, 0, time.UTC).Unix() }

	articleID, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: blogID, GUID: "a1", Type: "text", Author: "Jane Doe",
		Title:       "Designing a storage layer that survives a crash halfway through a write",
		URL:         "https://blog.example.com/posts/storage-layer",
		PublishedAt: at(20),
		ContentHTML: `<p>Most storage bugs are not in the happy path. They live in the half-second
between the write and the fsync, and they only show up when the power goes out.</p>
<h2>The write-ahead log</h2>
<p>Every change is appended to a log before it touches the main file. A recovery pass
replays the log on start-up:</p>
<ul><li>append the record</li><li>fsync the log</li><li>apply to the table</li></ul>
<blockquote><p>If it is not in the log, it did not happen.</p></blockquote>
<pre><code>func (w *WAL) Append(rec Record) error {
	if _, err := w.f.Write(rec.Bytes()); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	return w.f.Sync()
}</code></pre>
<p>Further reading: https://blog.example.com/posts/a-very-long-url-that-has-no-natural-break-points-and-should-wrap-instead-of-overflowing-the-card</p>`,
	})
	require.NoError(t, err)
	require.NoError(t, store.AddTag(articleID, "storage"))
	require.NoError(t, store.AddTag(articleID, "read-later"))

	starredID, _, err := store.UpsertItem(feed.Item{
		SubscriptionID: blogID, GUID: "a2", Type: "text", Author: "Sam Lee",
		Title:       "Release notes",
		URL:         "https://blog.example.com/posts/release-notes",
		PublishedAt: at(18),
		ContentHTML: "<p>Short and sweet: three fixes and one new feature.</p>",
	})
	require.NoError(t, err)
	require.NoError(t, store.MarkStarred(starredID, true))

	readID, _, err := store.UpsertItem(feed.Item{
		SubscriptionID: longID, GUID: "l1", Type: "text",
		Title:       "An already-read article from the subscription with the very long name",
		URL:         "https://long.example.org/read",
		PublishedAt: at(15),
		ContentHTML: "<p>This one has been read, so it should look visibly quieter than the unread rows above it.</p>",
	})
	require.NoError(t, err)
	require.NoError(t, store.MarkRead(readID, true))

	_, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: showID, GUID: "p1", Type: "audio", Author: "The Hosts",
		Title:    "Episode 112: Interviews, follow-ups and a surprisingly long episode title",
		URL:      "https://audio.example.net/112",
		MediaURL: "https://audio.example.net/112.mp3", MediaMIME: "audio/mpeg",
		DurationSec: 5025, PublishedAt: at(19),
	})
	require.NoError(t, err)

	_, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: chanID, GUID: "v1", Type: "video",
		Title:       "Building a bookshelf in ten minutes",
		URL:         "https://www.youtube.com/watch?v=example",
		MediaURL:    "https://www.youtube.com/watch?v=example",
		DurationSec: 612, PublishedAt: at(17),
	})
	require.NoError(t, err)

	for i := range 3 {
		_, _, err = store.UpsertItem(feed.Item{
			SubscriptionID: blogID, GUID: fmt.Sprintf("f%d", i), Type: "text",
			Title:       fmt.Sprintf("Filler article %d", i+1),
			PublishedAt: at(10 - i),
			ContentHTML: "<p>A summary line long enough that it has to be truncated rather than wrapping onto two or three lines in the list, which would make every row a different height and the list hard to scan.</p>",
		})
		require.NoError(t, err)
	}
	return articleID
}

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, feed.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "empty", "pass")
	env.CreateUser(t, "index", "pass")
	env.CreateUser(t, "expand", "pass")
	env.CreateUser(t, "reader", "pass")
	env.CreateUser(t, "subs", "pass")
	env.CreateUser(t, "sub", "pass")
	env.CreateUser(t, "star", "pass")
	env.CreateUser(t, "lists", "pass")
	seedBrowserFeed(t, env, "star")
	seedBrowserFeed(t, env, "index")
	seedBrowserFeed(t, env, "expand")
	articleID := seedBrowserFeed(t, env, "reader")
	seedBrowserFeed(t, env, "subs")
	seedBrowserFeed(t, env, "sub")
	lists := feedStore(t, env, "lists")
	for _, pl := range []feed.Playlist{
		{Name: "Commute", Query: "type:audio is:unread", Order: feed.OrderOldest},
		{Name: "Listen only", Query: "kind:youtube OR type:audio", Order: feed.OrderShuffled, ShuffleSeed: 3, ListenOnly: true},
	} {
		_, err := lists.CreatePlaylist(pl)
		require.NoError(t, err)
	}

	env.CreateUser(t, "hls", "pass")
	hlsItem := packagedItem(t, env, "hls", true)
	env.CreateUser(t, "hlsclean", "pass")
	cleanVideo := cleanItem(t, env, "hlsclean", true)
	env.CreateUser(t, "hlscc", "pass")
	ccOffsetItem := captionedItemFrom(t, env, "hlscc", "audio-offset.m4a", "video-offset.mp4", "WEBVTT\n\n00:00:03.000 --> 00:00:05.000\nHello there\n")
	ccItem := captionedItem(t, env, "hlscc", true, "WEBVTT\n\n00:00:03.000 --> 00:00:05.000\nHello there\n")

	// A packaged video plays over HLS: through hls.js where the browser has
	// Media Source Extensions (a blob: source), natively where it plays HLS
	// itself. The state is the assertion, not a snapshot: Chromium builds
	// differ in which codecs they decode.
	t.Run("HLS", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "hls", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/feed/items/%d", hlsItem))
		b.WaitVisible(t, `#feed-player[data-hls-state="loaded"]`)
		src, _ := b.Eval(t, `document.querySelector("#player-el").src`).(string)
		require.True(t, strings.HasPrefix(src, "blob:") || strings.Contains(src, "master.m3u8"), "player source %q", src)
	})

	// A cleaned video plays HLS with the Clean audio rendition, which the
	// master playlist marks DEFAULT=YES: hls.js reports it as playing.
	t.Run("HLSClean", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "hlsclean", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/feed/items/%d", cleanVideo))
		b.WaitVisible(t, `#feed-player[data-hls-state="loaded"]`)
		b.WaitVisible(t, `#feed-player[data-hls-audio="Clean"]`)
	})

	// Captions arrive as an HLS subtitle rendition. The CC button turns them on,
	// and hls.js places the cue at the item time it names: the X-TIMESTAMP-MAP
	// ties caption time 0 to the audio track's first fragment.
	t.Run("HLSCaptions", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "hlscc", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/feed/items/%d", ccItem))
		b.WaitVisible(t, `#feed-player[data-hls-state="loaded"]`)
		b.WaitVisible(t, `#player-cc:not([hidden])`)
		b.Click(t, "#player-cc")
		b.WaitVisible(t, `#feed-player[data-captions="on"]`)
		const cueStart = `() => { for (const x of document.querySelector("#player-el").textTracks) if (x.cues && x.cues.length) return x.cues[0].startTime; return -1 }`
		var start float64
		for deadline := time.Now().Add(20 * time.Second); ; {
			start, _ = b.Eval(t, cueStart).(float64)
			if start >= 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		assert.InDelta(t, 3.0, start, 0.1)
	})

	// The tracks start 1.5 s in. A cue at caption time 3 s must show 3 s after
	// the first audio sample, which is where the media element's first
	// buffered range begins: hls.js places the cue with the segment's
	// X-TIMESTAMP-MAP, and buffered.start(0) is the same timeline's origin.
	t.Run("HLSCaptionsOffset", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "hlscc", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/feed/items/%d", ccOffsetItem))
		b.WaitVisible(t, `#feed-player[data-hls-state="loaded"]`)
		b.WaitVisible(t, `#player-cc:not([hidden])`)
		b.Click(t, "#player-cc")
		b.WaitVisible(t, `#feed-player[data-captions="on"]`)
		const probe = `() => { const el = document.querySelector("#player-el"); const b0 = el.buffered.length ? el.buffered.start(0) : -1; for (const x of el.textTracks) if (x.cues && x.cues.length) return [x.cues[0].startTime, b0]; return [-1, b0] }`
		var cue, buf float64
		for deadline := time.Now().Add(20 * time.Second); ; {
			got, _ := b.Eval(t, probe).([]any)
			require.True(t, len(got) == 2, "probe returned %v", got)
			cue, _ = got[0].(float64)
			buf, _ = got[1].(float64)
			if (cue >= 0 && buf >= 0) || time.Now().After(deadline) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("cue startTime %.3f, buffered.start(0) %.3f", cue, buf)
		assert.InDelta(t, 3.0, cue-buf, 0.1)
	})

	// Under skip, captions keep their caption time, clipped to each piece,
	// and the X-TIMESTAMP-MAP stays MPEGTS 0. Kept media keeps its item-time
	// timestamps, so hls.js shows a cue after a cut where the cut moved it:
	// cue B at item time 9 s shows at 7 s once {6,8} is cut (R8). The
	// product player does not play skip yet, so the test plays the skip
	// master in a video element of its own.
	env.CreateUser(t, "hlsskip", "pass")
	skipCC := captionedItem(t, env, "hlsskip", true, "WEBVTT\n\n00:00:03.000 --> 00:00:04.000\nA\n\n00:00:09.000 --> 00:00:10.000\nB\n")
	t.Run("HLSSkipCaptions", func(t *testing.T) {
		needFFmpeg(t)
		store := feedStore(t, env, "hlsskip")
		// {5.9,8.1} at 0.5 s is the cut {6.0,8.0}: 6.0 is a segment
		// boundary, and [8,12) an edge.
		require.NoError(t, store.SetSilenceRanges(skipCC, []feed.Range{{Start: 5.9, End: 8.1, Action: "skip"}}))
		prefs, err := store.LoadPlayerPrefs()
		require.NoError(t, err)
		prefs.SkipSilenceMinSec = 0.5
		require.NoError(t, store.SavePlayerPrefs(prefs))
		skip := skipMaster(t, env, "hlsskip", skipCC)
		env.Get(t, skip).Status(http.StatusOK)
		runEdges(t, env, "hlsskip")
		require.NotEqual(t, "", masterEdit(env.Get(t, skip).Status(http.StatusOK).ReadBody()))

		b := eng.Start(t)
		b.Login(t, "hlsskip", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/feed/items/%d", skipCC))
		b.WaitVisible(t, `#feed-player[data-hls-state="loaded"]`)
		b.Eval(t, fmt.Sprintf(`() => {
			const v = document.createElement("video");
			v.id = "skip-el"; v.muted = true;
			document.body.appendChild(v);
			const h = new Hls({ subtitleDisplay: true });
			window.skipHls = h;
			h.loadSource(%q);
			h.attachMedia(v);
			return true;
		}`, skip))
		const probe = `() => {
			const v = document.querySelector("#skip-el");
			// hls.js leaves captions off until a track is chosen.
			if (window.skipHls.subtitleTracks.length && window.skipHls.subtitleTrack < 0) window.skipHls.subtitleTrack = 0;
			const out = { A: -1, B: -1, buf: v.buffered.length ? v.buffered.start(0) : -1 };
			for (const x of v.textTracks) if (x.cues) for (const c of x.cues) out[c.text] = c.startTime;
			return [out.A, out.B, out.buf];
		}`
		read := func(done func(a, b, buf float64) bool) (a, bb, buf float64) {
			for deadline := time.Now().Add(20 * time.Second); ; {
				raw := b.Eval(t, probe)
				got, ok := raw.([]any)
				require.True(t, ok && len(got) == 3, "probe returned %v", raw)
				var okA, okB, okBuf bool
				a, okA = got[0].(float64)
				bb, okB = got[1].(float64)
				buf, okBuf = got[2].(float64)
				require.True(t, okA && okB && okBuf, "probe returned %v", got)
				if done(a, bb, buf) || time.Now().After(deadline) {
					return a, bb, buf
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		a, _, buf := read(func(a, _, buf float64) bool { return a >= 0 && buf >= 0 })
		// Seek past the cut, so the piece holding cue B is fetched.
		b.Eval(t, `() => { document.querySelector("#skip-el").currentTime = 7.5; return true }`)
		_, cueB, _ := read(func(_, bb, _ float64) bool { return bb >= 0 })
		t.Logf("cue A %.3f, cue B %.3f, buffered.start(0) %.3f", a, cueB, buf)
		assert.InDelta(t, 3.0, a-buf, 0.1)
		assert.InDelta(t, 7.0, cueB-buf, 0.1)
	})

	t.Run("EmptyState", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "empty", "pass")
		b.Navigate(t, "/apps/feed/")
		b.WaitText(t, "No articles yet")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Index", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "index", "pass")
		b.Navigate(t, "/apps/feed/")
		b.WaitText(t, "Designing a storage layer")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ExpandedArticle", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "expand", "pass")
		b.Navigate(t, "/apps/feed/")
		b.WaitText(t, "Designing a storage layer")
		// Click the summary line: the middle of the row is the title, a link to the reader.
		b.Click(t, ".feed-item-head .feed-item-summary-line")
		b.WaitVisible(t, ".feed-item-detail .feed-item-content")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("StarUpdatesSidebar", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "star", "pass")
		b.Navigate(t, "/apps/feed/")
		b.WaitText(t, "Designing a storage layer")
		require.Equal(t, "1", b.Text(t, `#feed-sidebar a[href="/apps/feed/starred"] .count`))
		b.Click(t, `#item-list .btn-star:not(.active)`)
		b.WaitVisible(t, `#item-list .btn-star.active ~ *`)
		// The sidebar comes back with the star, out of band; its count follows.
		deadline := time.Now().Add(10 * time.Second)
		for b.Text(t, `#feed-sidebar a[href="/apps/feed/starred"] .count`) != "2" {
			if time.Now().After(deadline) {
				t.Fatal("sidebar starred count did not update")
			}
			time.Sleep(50 * time.Millisecond)
		}
	})

	t.Run("Reader", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "reader", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/feed/items/%d", articleID))
		b.WaitText(t, "The write-ahead log")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Subscription", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "sub", "pass")
		b.Navigate(t, "/apps/feed/")
		b.WaitText(t, "Designing a storage layer")
		b.ClickText(t, "A Subscription With An Extremely Long Title")
		b.WaitText(t, "Last fetch failed")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("SubscribeForm", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "subs", "pass")
		b.Navigate(t, "/apps/feed/settings/subscriptions")
		b.WaitVisible(t, "#add-subscription")
		b.Eval(t, `() => { document.querySelector("#subscription-details").open = true }`)
		b.WaitVisible(t, "#bulk-subscriptions")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Playlists", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "lists", "pass")
		b.Navigate(t, "/apps/feed/settings/playlists")
		b.WaitText(t, "Reshuffle")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}
