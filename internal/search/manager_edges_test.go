package search

import (
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/plugin"
	_ "github.com/mattn/go-sqlite3"
)

func newMgr(t *testing.T) (*Manager, *sql.DB) {
	t.Helper()
	sysDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sysDB.Close() })

	sysDB.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT, display_name TEXT)`)
	sysDB.Exec(`CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT)`)
	sysDB.Exec(`CREATE TABLE volumes (id TEXT PRIMARY KEY, name TEXT, description TEXT)`)

	mgr := NewManager(slog.New(slog.NewTextHandler(t.Output(), nil)), t.TempDir())
	mgr.SetSystemDB(sysDB)
	if err := mgr.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Stop() })
	return mgr, sysDB
}

// --- SystemHook ---

func TestSystemHook_RoutesIntoSystemCollector(t *testing.T) {
	mgr, sysDB := newMgr(t)

	hook := mgr.SystemHook()
	sysDB.Exec(`INSERT INTO users (id, username, display_name) VALUES ('u1', 'eve', 'Eve')`)
	hook(18 /*SQLITE_INSERT*/, "main", "users", 1)

	time.Sleep(300 * time.Millisecond)
	res, err := mgr.SearchSystem("eve", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total == 0 {
		t.Fatal("expected eve to be searchable after hook fired")
	}
}

func TestSystemHook_NilCollectorSafe(t *testing.T) {
	mgr := NewManager(slog.New(slog.NewTextHandler(t.Output(), nil)), t.TempDir())
	// No Start() — systemCollector is nil. Hook should be a no-op.
	hook := mgr.SystemHook()
	hook(18, "main", "users", 1) // must not panic
}

// --- sqliteOpToOp ---

func TestSqliteOpToOp(t *testing.T) {
	cases := map[int]Op{
		18: OpInsert,
		23: OpUpdate,
		9:  OpDelete,
		99: OpUpdate, // unknown defaults to update
	}
	for in, want := range cases {
		if got := sqliteOpToOp(in); got != want {
			t.Errorf("sqliteOpToOp(%d) = %v, want %v", in, got, want)
		}
	}
}

// --- User index lifecycle: ensureUserIndex / SearchUser /
//     IndexSharedDocument / DeleteSharedDocument ---

func TestUserIndex_LifecycleWithoutPlugin(t *testing.T) {
	mgr, _ := newMgr(t)

	// SearchUser implicitly creates the user index.
	res, err := mgr.SearchUser("alice", "missing", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 {
		t.Fatalf("expected empty result, got %d", res.Total)
	}

	// Index a shared document into alice's index.
	if err := mgr.IndexSharedDocument("alice", Document{
		ID:     "shared:1",
		Fields: map[string]any{"name": "shared-doc", "source": "files"},
	}); err != nil {
		t.Fatal(err)
	}

	// Allow async index commit.
	time.Sleep(100 * time.Millisecond)

	res, err = mgr.SearchUser("alice", "shared-doc", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total == 0 {
		t.Fatal("expected shared doc to be searchable")
	}

	// Delete and confirm gone.
	if err := mgr.DeleteSharedDocument("alice", "shared:1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	res, _ = mgr.SearchUser("alice", "shared-doc", 10, 0)
	if res.Total != 0 {
		t.Fatalf("expected doc deleted, still %d hits", res.Total)
	}
}

func TestEnsureUserIndex_Idempotent(t *testing.T) {
	mgr, _ := newMgr(t)

	us1, err := mgr.ensureUserIndex("alice")
	if err != nil {
		t.Fatal(err)
	}
	us2, err := mgr.ensureUserIndex("alice")
	if err != nil {
		t.Fatal(err)
	}
	if us1 != us2 {
		t.Fatal("expected ensureUserIndex to return the cached entry")
	}
}

// --- UserDBHook (audit-flagged async init path) ---

func TestUserDBHook_AsyncInitWhenIndexMissing(t *testing.T) {
	mgr, _ := newMgr(t)
	hook := mgr.UserDBHook("alice", "files")

	// Pre-condition: alice's index isn't opened.
	mgr.mu.Lock()
	_, ok := mgr.userIndices["alice"]
	mgr.mu.Unlock()
	if ok {
		t.Fatal("setup: alice's index should not exist yet")
	}

	hook(18, "main", "files", 42) // INSERT; must not block or panic

	// Eventually the async goroutine opens the index.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mgr.mu.Lock()
		_, ok := mgr.userIndices["alice"]
		mgr.mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("alice's index never opened")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUserDBHook_FastPathWhenIndexOpen(t *testing.T) {
	mgr, _ := newMgr(t)
	_, err := mgr.ensureUserIndex("alice")
	if err != nil {
		t.Fatal(err)
	}

	hook := mgr.UserDBHook("alice", "files")
	hook(18, "main", "files", 1) // synchronously forwards to collector
}

// --- RegisterPluginSource + SetUserDBFunc + SetFilesDirFunc ---

func TestRegisterPluginSource_StoresDef(t *testing.T) {
	mgr, _ := newMgr(t)
	mgr.RegisterPluginSource("files", plugin.SearchSourceDef{
		PrimaryTable: "files",
		EnrichSQL:    `SELECT rowid, name FROM files WHERE rowid IN (?)`,
	})
	defs := mgr.pluginSources["files"]
	if len(defs) != 1 {
		t.Fatalf("expected 1 source, got %d", len(defs))
	}
}

func TestSetUserDBFunc_SetFilesDirFunc(t *testing.T) {
	mgr, _ := newMgr(t)
	mgr.SetUserDBFunc(func(u, p string) (*sql.DB, error) { return nil, nil })
	mgr.SetFilesDirFunc(func(u string) string { return "/tmp/" + u })
	if mgr.userDBFunc == nil {
		t.Fatal("userDBFunc not set")
	}
	if mgr.filesDirFunc == nil {
		t.Fatal("filesDirFunc not set")
	}
	if got := mgr.filesDirFunc("alice"); got != "/tmp/alice" {
		t.Fatalf("got %q, want /tmp/alice", got)
	}
}

// --- index Delete (the package-level fs cleanup) ---

func TestIndex_Delete(t *testing.T) {
	dir := t.TempDir() + "/idx"
	idx, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	idx.Close()
	if err := Delete(dir); err != nil {
		t.Fatal(err)
	}
}

// --- index GetDocument ---

func TestIndex_GetDocument(t *testing.T) {
	idx, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	require := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	require(t, idx.Batch([]Document{{
		ID:     "id-1",
		Fields: map[string]any{"name": "Hello", "source": "test"},
	}}, nil))

	got, err := idx.GetDocument("id-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "id-1" {
		t.Fatalf("ID = %q, want id-1", got.ID)
	}
}

func TestIndex_GetDocument_Missing(t *testing.T) {
	idx, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	_, err = idx.GetDocument("never-stored")
	if err == nil {
		t.Fatal("expected error for missing document")
	}
}

// --- PluginSearchSource ---

func TestPluginSearchSource_EnrichPositionalFields(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT, body TEXT, tag TEXT)`)
	db.Exec(`INSERT INTO notes (title, body, tag) VALUES ('Title', 'body text', 'work')`)
	db.Exec(`INSERT INTO notes (title, body, tag) VALUES ('Other', 'more body', 'home')`)

	src := NewPluginSearchSource("notes", plugin.SearchSourceDef{
		PrimaryTable: "notes",
		EnrichSQL:    `SELECT rowid, title, body, tag FROM notes WHERE rowid IN (?)`,
	})
	if src.Name() != "notes:notes" {
		t.Fatalf("Name = %q", src.Name())
	}
	if tabs := src.Tables(); len(tabs) != 1 || tabs[0] != "notes" {
		t.Fatalf("Tables = %v", tabs)
	}

	docs, err := src.Enrich(db, []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(docs))
	}
	if name, _ := docs[0].Fields["name"].(string); name != "Title" {
		t.Fatalf("expected name=Title, got %q", name)
	}
	if sub, _ := docs[0].Fields["subtext"].(string); sub != "body text" {
		t.Fatalf("expected subtext=body text, got %q", sub)
	}
	if content, _ := docs[0].Fields["content"].(string); content != "work" {
		t.Fatalf("expected content=work, got %q", content)
	}
}

func TestPluginSearchSource_Enrich_EmptyIDs(t *testing.T) {
	src := NewPluginSearchSource("x", plugin.SearchSourceDef{
		PrimaryTable: "t",
		EnrichSQL:    `SELECT rowid FROM t WHERE rowid IN (?)`,
	})
	docs, err := src.Enrich(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if docs != nil {
		t.Fatalf("expected nil, got %v", docs)
	}
}

func TestPluginSearchSource_Enrich_ContentFunc(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE files (id INTEGER PRIMARY KEY, name TEXT, path TEXT)`)
	db.Exec(`INSERT INTO files (name, path) VALUES ('readme.txt', '/readme.txt')`)

	src := NewPluginSearchSource("files", plugin.SearchSourceDef{
		PrimaryTable: "files",
		EnrichSQL:    `SELECT rowid, name, path FROM files WHERE rowid IN (?)`,
		ContentFunc: func(filesDir string, cols map[string]string) string {
			return "fake-content-for-" + cols["name"]
		},
	})
	src.FilesDir = "/tmp/files"

	docs, err := src.Enrich(db, []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc")
	}
	if c, _ := docs[0].Fields["content"].(string); c != "fake-content-for-readme.txt" {
		t.Fatalf("ContentFunc not applied: content=%q", c)
	}
}

func TestPluginSearchSource_Enrich_BadSQL(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	src := NewPluginSearchSource("x", plugin.SearchSourceDef{
		PrimaryTable: "t",
		EnrichSQL:    `SELECT this is not valid SQL WHERE rowid IN (?)`,
	})
	_, err = src.Enrich(db, []int64{1})
	if err == nil {
		t.Fatal("expected error from bad SQL")
	}
}

// --- valString edge cases ---

func TestValString_NilAndTypes(t *testing.T) {
	if got := valString(nil); got != "" {
		t.Fatalf("nil → %q", got)
	}
	if got := valString("plain"); got != "plain" {
		t.Fatalf("string → %q", got)
	}
	if got := valString([]byte{'h', 'i'}); got != "hi" {
		t.Fatalf("bytes → %q", got)
	}
	if got := valString(int64(42)); got != "42" {
		t.Fatalf("int64 → %q", got)
	}
}
