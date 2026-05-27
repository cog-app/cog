package feed

import (
	"net/http/httptest"
	"testing"
)

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
