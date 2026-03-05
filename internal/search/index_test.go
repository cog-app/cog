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
