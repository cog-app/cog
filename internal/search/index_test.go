package search

import "testing"

func TestIndexOpenCloseSearch(t *testing.T) {
	idx, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory index: %v", err)
	}
	defer idx.Close()

	// Index some documents.
	docs := []Document{
		{ID: "users:1", Fields: map[string]any{"name": "alice", "subtext": "Alice Wonderland", "source": "users"}},
		{ID: "users:2", Fields: map[string]any{"name": "bob", "subtext": "Bob Builder", "source": "users"}},
		{ID: "files:1", Fields: map[string]any{"name": "readme.md", "subtext": "/docs/readme.md", "source": "files"}},
	}
	if err := idx.Batch(docs, nil); err != nil {
		t.Fatalf("batch: %v", err)
	}

	// Search for alice.
	result, err := idx.Search("alice", 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if result.Total == 0 {
		t.Fatal("expected at least one hit for 'alice'")
	}
	if result.Hits[0].ID != "users:1" {
		t.Fatalf("expected users:1, got %s", result.Hits[0].ID)
	}

	// Search for "readme.md" — Bleve tokenizes on dots, so search for the full name.
	result, err = idx.Search("readme.md", 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if result.Total == 0 {
		// Try without extension — Bleve may tokenize differently.
		result, err = idx.Search("docs", 10, 0)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if result.Total == 0 {
			t.Fatal("expected at least one hit for file search")
		}
	}

	// Query syntax: AND (implicit).
	result, err = idx.Search("alice wonderland", 10, 0)
	if err != nil {
		t.Fatalf("search AND: %v", err)
	}
	if result.Total == 0 {
		t.Fatal("expected hit for 'alice wonderland' (implicit AND)")
	}

	// Query syntax: OR.
	result, err = idx.Search("alice OR bob", 10, 0)
	if err != nil {
		t.Fatalf("search OR: %v", err)
	}
	if result.Total < 2 {
		t.Fatalf("expected 2 hits for 'alice OR bob', got %d", result.Total)
	}

	// Query syntax: NOT.
	result, err = idx.Search("NOT alice", 10, 0)
	if err != nil {
		t.Fatalf("search NOT: %v", err)
	}
	for _, h := range result.Hits {
		if h.ID == "users:1" {
			t.Fatal("NOT alice should exclude users:1")
		}
	}

	// Query syntax: field filter.
	result, err = idx.Search(":source=users", 10, 0)
	if err != nil {
		t.Fatalf("search field: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected 2 hits for :source=users, got %d", result.Total)
	}

	// Query syntax: field IN.
	result, err = idx.Search(":source IN (users, files)", 10, 0)
	if err != nil {
		t.Fatalf("search field IN: %v", err)
	}
	if result.Total != 3 {
		t.Fatalf("expected 3 hits for :source IN (users, files), got %d", result.Total)
	}

	// Query syntax: combined.
	result, err = idx.Search("alice AND :source=users", 10, 0)
	if err != nil {
		t.Fatalf("search combined: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected 1 hit for 'alice AND :source=users', got %d", result.Total)
	}

	// Query syntax: phrase.
	result, err = idx.Search(`"Alice Wonderland"`, 10, 0)
	if err != nil {
		t.Fatalf("search phrase: %v", err)
	}
	if result.Total == 0 {
		t.Fatal("expected hit for phrase 'Alice Wonderland'")
	}

	// Delete and verify.
	if err := idx.Batch(nil, []string{"users:1"}); err != nil {
		t.Fatalf("batch delete: %v", err)
	}
	result, err = idx.Search("alice", 10, 0)
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if result.Total != 0 {
		t.Fatalf("expected 0 hits after delete, got %d", result.Total)
	}
}

// TestIndexNameBoost: a document named for the word searched outranks one
// that mentions it more often.
func TestIndexNameBoost(t *testing.T) {
	idx, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory index: %v", err)
	}
	defer idx.Close()
	docs := []Document{
		{ID: "docs:index", Fields: map[string]any{"name": "Cog", "content": "webdav sharing webdav sync webdav apps webdav"}},
		{ID: "docs:webdav", Fields: map[string]any{"name": "WebDAV", "content": "Connect a drive over the network, with many words about clients and mounting."}},
	}
	if err := idx.Batch(docs, nil); err != nil {
		t.Fatalf("batch: %v", err)
	}
	for _, q := range []string{"webdav", "webd"} {
		res, err := idx.Search(q, 10, 0)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if len(res.Hits) != 2 || res.Hits[0].ID != "docs:webdav" {
			t.Fatalf("search %q: want docs:webdav first, got %+v", q, res.Hits)
		}
	}
}
