package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestFeedSettings(t *testing.T) {
	env := cogtest.New(t, feed.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "u", "pass")

	t.Run("AddSubscription", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "u", "pass")
		b.Navigate(t, "/apps/feed/settings/subscriptions")
		b.WaitVisible(t, "#add-subscription")

		b.Type(t, "#sub-url", "https://example.com/feed.xml")
		b.Submit(t, "#sub-url")
		b.WaitVisible(t, "#add-subscription")
		// Subscription URL appears in the (collapsed) list.
		b.Eval(t, `() => { document.querySelector("#subscription-details").open = true }`)
		b.WaitVisible(t, `//code[contains(text(),"https://example.com/feed.xml")]`)
	})

	t.Run("AddFolderAndFilter", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "u", "pass")
		b.Navigate(t, "/apps/feed/settings/subscriptions")
		b.WaitVisible(t, "#add-folder")

		// Add a folder named "Development".
		b.Type(t, "#folder-name", "Development")
		b.Submit(t, "#folder-name")
		b.WaitVisible(t, "#add-folder")
		b.WaitVisible(t, `//ul//span[contains(text(),"Development")]`)

		// Add a filter.
		b.Navigate(t, "/apps/feed/settings/filters")
		b.WaitVisible(t, "#add-filter")
		b.Type(t, "#filter-query", "type:audio")
		b.Eval(t, `() => { document.getElementById("filter-action").value = "tag" }`)
		b.Type(t, "#filter-action-value", "podcast")
		b.Submit(t, "#filter-query")
		b.WaitVisible(t, "#add-filter")
		b.WaitVisible(t, `//ul//code[contains(text(),"type:audio")]`)
	})

	t.Run("RejectsBadFilterQuery", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "u", "pass")
		b.Navigate(t, "/apps/feed/settings/filters")
		b.WaitVisible(t, "#add-filter")

		// Submit a filter with an invalid query — expect a flash error, no filter created.
		b.Type(t, "#filter-query", "(((")
		b.Submit(t, "#filter-query")
		b.WaitVisible(t, "#add-filter")
		// Flash error should be displayed.
		b.WaitVisible(t, `.alert-error`)
	})

	t.Run("SyncFilters", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "u", "pass")
		b.Navigate(t, "/apps/feed/settings/sync")
		b.WaitVisible(t, "#sync-gpodder")

		b.Type(t, "#sync-gpodder", "folder:Podcasts")
		b.Submit(t, "#sync-gpodder")
		b.WaitVisible(t, `.alert-success`)
		b.WaitVisible(t, `//input[@id="sync-gpodder" and @value="folder:Podcasts"]`)

		b.Type(t, "#sync-ncnews", "(((")
		b.Submit(t, "#sync-ncnews")
		b.WaitVisible(t, `.alert-error`)
	})
}

// Moving a filtered selection: podcasts left in no folder by a sync go to a
// new folder in one step, and the articles feed beside them stays.
func TestBulkMoveSubscriptions(t *testing.T) {
	env := cogtest.New(t, feed.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)
	u := env.CreateUser(t, "u", "pass")
	raw, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := raw.(*feed.Plugin).DB(u.Username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	pod, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://pod.example/feed.xml", Title: "A Podcast"})
	require.NoError(t, err)
	_, _, err = store.UpsertItem(feed.Item{SubscriptionID: pod, GUID: "ep1", Type: "audio", MediaURL: "https://pod.example/ep1.mp3"})
	require.NoError(t, err)
	blog, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://blog.example/rss", Title: "A Blog"})
	require.NoError(t, err)

	b := eng.Start(t)
	b.Login(t, "u", "pass")
	b.Navigate(t, "/apps/feed/settings/subscriptions")
	b.WaitVisible(t, "#bulk-subscriptions")
	b.WaitNotVisible(t, "#bulk-bar")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)

	b.Click(t, `input[name="sub-type"][value="podcast"]`)
	b.WaitNotVisible(t, `li[data-type="articles"]`)
	b.Click(t, "[data-group-toggle]")
	b.WaitVisible(t, "#bulk-bar")
	if got := b.Text(t, "#bulk-count"); got != "1" {
		t.Fatalf("selected = %q, want 1", got)
	}
	b.Select(t, "#bulk-folder", `[value="new"]`)
	b.WaitVisible(t, "#bulk-new-folder")
	b.Type(t, "#bulk-new-folder", "Podcasts")
	b.Click(t, `#bulk-bar button[value="move"]`)
	b.WaitVisible(t, ".alert-success")
	b.WaitVisible(t, `//legend[contains(., "Podcasts")]`)

	for id, want := range map[int64]bool{pod: true, blog: false} {
		s, err := store.GetSubscription(id)
		require.NoError(t, err)
		require.Equal(t, want, s.FolderID.Valid)
	}
}
