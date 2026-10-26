package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
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
		b.Eval(t, `() => { document.querySelector(".subscription-list-collapse").open = true }`)
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
}
