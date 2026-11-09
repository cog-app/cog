package feed

import (
	"net/http/httptest"
	"strconv"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
)

func newStoreInternal(t *testing.T) *Store {
	t.Helper()
	_, s := newPagesInternal(t)
	return s
}

// newPagesInternal is a user's store and the plugin's pages, registered with
// a server so they have its clock.
func newPagesInternal(t *testing.T) (*Pages, *Store) {
	t.Helper()
	env := cogtest.New(t, New())
	u := env.CreateUser(t, "u", "pass")
	raw, ok := env.Providers.Plugins.Plugin(PluginID)
	if !ok {
		t.Fatal("feed plugin not registered")
	}
	p := raw.(*Plugin)
	db, err := p.DB(u.User.Username)
	if err != nil {
		t.Fatal(err)
	}
	return NewPages(p), NewStore(db)
}

func TestParseViewCtxAndEncode(t *testing.T) {
	r := httptest.NewRequest("GET", "/apps/feed/items/5?ctx=folder&cid=3", nil)
	c := parseViewCtx(r)
	if c.Kind != "folder" || c.ID != 3 {
		t.Fatalf("got %+v", c)
	}
	if got := c.encode(); got != "ctx=folder&cid=3" {
		t.Fatalf("encode = %q", got)
	}
}

func TestParseViewCtxDefaultsToAll(t *testing.T) {
	r := httptest.NewRequest("GET", "/apps/feed/items/5", nil)
	if c := parseViewCtx(r); c.Kind != "all" {
		t.Fatalf("got %+v", c)
	}
}

func TestNeighborsAnyType(t *testing.T) {
	items := []ItemWithSubscription{
		{Item: Item{ID: 1, Type: "text"}},
		{Item: Item{ID: 2, Type: "audio"}},
		{Item: Item{ID: 3, Type: "text"}},
	}
	prev, next := neighbors(items, 2, false)
	if prev == nil || prev.ID != 1 || next == nil || next.ID != 3 {
		t.Fatalf("prev=%v next=%v", prev, next)
	}
}

func TestNeighborsMediaOnlySkipsText(t *testing.T) {
	items := []ItemWithSubscription{
		{Item: Item{ID: 1, Type: "audio"}},
		{Item: Item{ID: 2, Type: "text"}},
		{Item: Item{ID: 3, Type: "video"}},
	}
	prev, next := neighbors(items, 2, true)
	if prev == nil || prev.ID != 1 || next == nil || next.ID != 3 {
		t.Fatalf("prev=%v next=%v", prev, next)
	}
}

func TestNeighborsBoundariesAndAbsent(t *testing.T) {
	items := []ItemWithSubscription{{Item: Item{ID: 1, Type: "audio"}}}
	if p, n := neighbors(items, 1, false); p != nil || n != nil {
		t.Fatal("single item should have no neighbors")
	}
	if p, n := neighbors(items, 99, false); p != nil || n != nil {
		t.Fatal("absent id should have no neighbors")
	}
}

func TestResolveViewItemsAllMatchesListItemsForView(t *testing.T) {
	h, s := newPagesInternal(t)
	subID, err := s.CreateSubscription(Subscription{Kind: "rss", URL: "https://x.test/f"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, _, err := s.UpsertItem(Item{
			SubscriptionID: subID,
			GUID:           "g" + strconv.Itoa(i),
			Type:           "text",
			Title:          "t" + strconv.Itoa(i),
			PublishedAt:    int64(100 + i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := h.resolveViewItems(s, viewCtx{Kind: "all"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.ListItemsForView(ViewSpec{All: true}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || len(got) != 3 {
		t.Fatalf("got %d want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].ID != want[i].ID {
			t.Fatalf("order mismatch at %d", i)
		}
	}
}
