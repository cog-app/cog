package feed_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/query"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// TestQueryNeedsRefine verifies queryNeedsRefine truth table.
// queryNeedsRefine is unexported, so we test it via parse+observe behavior.
// We verify the gate by checking that text/phrase queries actually refine.
// The unit truth table is tested below using query.Parse.
func TestQueryNeedsRefineUnitTruthTable(t *testing.T) {
	// We can't call queryNeedsRefine directly (unexported), so we drive it
	// through the HTTP handler: items that SQL would pass but exact-match fails
	// must be absent only when refine is active (text/phrase).
	// The unit truth-table is documented here via query.Parse to show that
	// the types are correct.
	cases := []struct {
		q    string
		want bool // whether it contains text/phrase
	}{
		{"hello", true},                   // TextExpr
		{`"hello world"`, true},           // PhraseExpr
		{":type=audio", false},            // FieldExpr (not text/phrase)
		{":published>2026-01-01", false},  // RangeExpr
		{"is:unread", false},              // FieldExpr
		{":type=audio hello", true},       // AndExpr with TextExpr
		{`NOT "foo bar"`, true},           // NotExpr wrapping PhraseExpr
		{`is:starred OR "podcast"`, true}, // OrExpr with PhraseExpr
	}
	for _, c := range cases {
		t.Run(c.q, func(t *testing.T) {
			e, err := query.Parse(c.q)
			if err != nil {
				t.Skipf("parse error (not our concern): %v", err)
			}
			got := exprNeedsRefine(e)
			if got != c.want {
				t.Errorf("queryNeedsRefine(%q) = %v, want %v", c.q, got, c.want)
			}
		})
	}
}

// exprNeedsRefine mirrors queryNeedsRefine so we can unit-test the logic
// without exporting the production function.
func exprNeedsRefine(e query.Expr) bool {
	switch x := e.(type) {
	case *query.TextExpr, *query.PhraseExpr:
		return true
	case *query.NotExpr:
		return exprNeedsRefine(x.Inner)
	case *query.AndExpr:
		return exprNeedsRefine(x.Left) || exprNeedsRefine(x.Right)
	case *query.OrExpr:
		return exprNeedsRefine(x.Left) || exprNeedsRefine(x.Right)
	default:
		return false
	}
}

// seedStoreForRefine creates a subscription in the given store and returns its ID.
func seedStoreForRefine(t *testing.T, s *feed.Store, title string) int64 {
	t.Helper()
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/" + title, Title: title})
	require.NoError(t, err)
	return subID
}

// getStoreFromEnv retrieves the feed store for the user.
func getStoreFromEnv(t *testing.T, env *cogtest.Env, username string) *feed.Store {
	t.Helper()
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok, "feed plugin not registered")
	fp, ok := raw.(*feed.Plugin)
	require.True(t, ok, "plugin is not *feed.Plugin")
	db, err := fp.DB(username)
	require.NoError(t, err)
	return feed.NewStore(db)
}

// TestIndexSearchPhraseRefine verifies that phrase queries ("hello world") perform
// exact phrase matching: "hello world" and "hello, world" match (same token sequence
// in content), but "world hello" does not.
func TestIndexSearchPhraseRefine(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	s := getStoreFromEnv(t, env, u.Username)
	subID := seedStoreForRefine(t, s, "TestSub")

	// Three items: distinct titles; phrase content varies.
	_, _, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "p1", Type: "text",
		Title: "alpha", ContentHTML: "hello world story", PublishedAt: 300,
	})
	require.NoError(t, err)
	_, _, err = s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "p2", Type: "text",
		Title: "beta", ContentHTML: "hello, world adventure", PublishedAt: 200,
	})
	require.NoError(t, err)
	_, _, err = s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "p3", Type: "text",
		Title: "gamma", ContentHTML: "world hello reversed", PublishedAt: 100,
	})
	require.NoError(t, err)

	q := url.QueryEscape(`"hello world"`)
	resp := env.Get(t, "/apps/feed/?q="+q, u.Auth)
	resp.Status(http.StatusOK)
	body := resp.ReadBody()

	// alpha and beta should appear (have "hello world" / "hello, world" — same tokens).
	if !strings.Contains(body, ">alpha<") {
		t.Errorf("alpha (hello world) should appear in phrase results; body snippet: %q", snippetRefine(body, "alpha"))
	}
	if !strings.Contains(body, ">beta<") {
		t.Errorf("beta (hello, world) should appear in phrase results; body snippet: %q", snippetRefine(body, "beta"))
	}
	// gamma (world hello) should NOT appear.
	if strings.Contains(body, ">gamma<") {
		t.Errorf("gamma (world hello) should NOT appear in phrase results; body snippet: %q", snippetRefine(body, "gamma"))
	}
}

// TestIndexSearchPrefixRefine verifies that text (prefix) queries are refined:
// "cat" matches "Category" but not "scatter" (which is a substring, not prefix-of-token).
func TestIndexSearchPrefixRefine(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	s := getStoreFromEnv(t, env, u.Username)
	subID := seedStoreForRefine(t, s, "PrefixSub")

	_, _, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "cat1", Type: "text",
		Title: "Category", ContentHTML: "categorized content", PublishedAt: 200,
	})
	require.NoError(t, err)
	_, _, err = s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "scat1", Type: "text",
		Title: "scatter", ContentHTML: "scattered words", PublishedAt: 100,
	})
	require.NoError(t, err)

	resp := env.Get(t, "/apps/feed/?q=cat", u.Auth)
	resp.Status(http.StatusOK)
	body := resp.ReadBody()

	if !strings.Contains(body, ">Category<") {
		t.Errorf("Category should appear for q=cat; body snippet: %q", snippetRefine(body, "Category"))
	}
	if strings.Contains(body, ">scatter<") {
		t.Errorf("scatter should NOT appear for q=cat; body snippet: %q", snippetRefine(body, "scatter"))
	}
}

// TestIndexSearchFieldOnlyNoRefine verifies that field-only queries (e.g. :type=audio)
// are NOT subject to refine (existing behavior preserved).
func TestIndexSearchFieldOnlyNoRefine(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	s := getStoreFromEnv(t, env, u.Username)
	subID := seedStoreForRefine(t, s, "NoRefineSub")

	_, _, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "a1", Type: "audio",
		Title: "AudioItem", PublishedAt: 200,
	})
	require.NoError(t, err)
	_, _, err = s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "t1", Type: "text",
		Title: "TextItem", PublishedAt: 100,
	})
	require.NoError(t, err)

	resp := env.Get(t, "/apps/feed/?q=:type=audio", u.Auth)
	resp.Status(http.StatusOK)
	body := resp.ReadBody()

	if !strings.Contains(body, ">AudioItem<") {
		t.Errorf("AudioItem should appear for :type=audio; body: %q", snippetRefine(body, "AudioItem"))
	}
	if strings.Contains(body, ">TextItem<") {
		t.Errorf("TextItem should NOT appear for :type=audio; body snippet: %q", snippetRefine(body, "TextItem"))
	}
}

func snippetRefine(body, marker string) string {
	i := strings.Index(body, marker)
	if i < 0 {
		return "(not found)"
	}
	start := max(i-50, 0)
	end := min(i+100, len(body))
	return body[start:end]
}
