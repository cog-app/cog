package search

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"

	cogdb "git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/query"
	"git.duckfam.us/jj-software/cog/plugin"
)

type userSearch struct {
	index     *Index
	collector *Collector
}

// Manager orchestrates system and per-user search indices.
type Manager struct {
	log     *slog.Logger
	dataDir string

	systemIndex     *Index
	systemCollector *Collector
	sysDB           *sql.DB

	mu            sync.Mutex
	userIndices   map[string]*userSearch // username -> {index, collector}
	pluginSources map[string][]plugin.SearchSourceDef

	// userDBFunc returns the per-user plugin DB for a given username+pluginID.
	userDBFunc func(username, pluginID string) (*sql.DB, error)
	// filesDirFunc returns the files directory for a given username.
	filesDirFunc func(username string) string
	ctx          context.Context
	cancel       context.CancelFunc

	// Propagator handles indexing shared resources into recipients' indices.
	Propagator *Propagator

	// SharedAccess reports which shared resources a user can read now. A
	// recipient's index holds copies of what was shared with them, and a
	// copy outlives the access it came from whenever no event removes it —
	// a share expiring, a member leaving a group — so every shared hit is
	// checked when it is found. Without it, shared hits are dropped.
	SharedAccess func(username string, refs []SharedRef) (map[SharedRef]bool, error)

	// drainer moves captured row changes into the per-user collectors.
	drainer *drainer

	// opening collapses concurrent opens of the same user index into one.
	opening singleflight.Group

	// background tracks work started on the manager's behalf — currently the
	// lazy index opens kicked off by write hooks. Stop waits for it: an
	// untracked open goes on creating index files and querying databases
	// after everything it depends on has been torn down.
	background sync.WaitGroup
	stopped    bool
}

// goBackground runs f unless the manager is stopping, and keeps Stop waiting
// for it if it is not.
func (m *Manager) goBackground(f func()) {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.background.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.background.Done()
		f()
	}()
}

// NewManager creates a manager. Call SetSystemDB and Start to initialize.
func NewManager(log *slog.Logger, dataDir string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		log:           log.With("component", "search"),
		dataDir:       dataDir,
		userIndices:   make(map[string]*userSearch),
		pluginSources: make(map[string][]plugin.SearchSourceDef),
		ctx:           ctx,
		cancel:        cancel,
	}
}

// SetSystemDB resolves the chicken-and-egg: manager is created before DB is opened.
func (m *Manager) SetSystemDB(db *sql.DB) {
	m.sysDB = db
}

// SetUserDBFunc sets the function to get per-user plugin databases.
func (m *Manager) SetUserDBFunc(f func(username, pluginID string) (*sql.DB, error)) {
	m.userDBFunc = f
}

// SetFilesDirFunc sets the function to resolve a user's files directory.
func (m *Manager) SetFilesDirFunc(f func(username string) string) {
	m.filesDirFunc = f
}

// RegisterPluginSource records a plugin's search source definition.
func (m *Manager) RegisterPluginSource(pluginID string, def plugin.SearchSourceDef) {
	m.pluginSources[pluginID] = append(m.pluginSources[pluginID], def)
}

// pluginSchemas extracts the per-plugin search schemas. Keys are plugin IDs;
// nil entries are dropped. The system also registers internal sources
// (UserSource/GroupSource/VolumeSource) — those have no Schema and are
// served by the legacy bleve mapping path, so we only fan schemas in for
// plugin defs that set one.
func (m *Manager) pluginSchemas() map[string]*query.Schema {
	out := map[string]*query.Schema{}
	for id, defs := range m.pluginSources {
		for _, def := range defs {
			if def.Schema != nil {
				out[id] = def.Schema
			}
		}
	}
	return out
}

// SystemHook returns a hook function for the system database.
//
// Like the per-user hook it only nudges: the change itself is captured by a
// trigger inside the writing transaction, and the drainer moves it.
func (m *Manager) SystemHook() func(op int, db, table string, rowid int64) {
	return func(op int, dbName, table string, rowid int64) {
		if m.drainer != nil {
			m.drainer.nudgeSystem()
		}
	}
}

// UserDBHook returns a hook function for a per-user plugin database.
//
// The hook carries no data: it only tells the drainer there is something worth
// draining now. What changed is already recorded in the database's own queue
// by a trigger, inside the writing transaction. That makes the hook an
// optimisation on drain latency, so indexing still works for a writer in
// another process or a backend with no hooks at all.
//
// It runs synchronously inside the SQLite write path and must not touch the
// database: the change it is about has not committed yet, and querying the
// same connection would deadlock.
func (m *Manager) UserDBHook(username, pluginID string) func(op int, db, table string, rowid int64) {
	return func(op int, dbName, table string, rowid int64) {
		m.mu.Lock()
		_, ok := m.userIndices[username]
		m.mu.Unlock()

		if !ok {
			// Index not open yet; open it in the background. Its full sync
			// covers whatever is queued, so nothing is lost by returning here.
			m.goBackground(func() {
				if _, err := m.ensureUserIndex(username); err != nil {
					m.log.Error("lazy open user index failed", "user", username, "err", err)
				}
			})
			return
		}

		if m.drainer != nil {
			m.drainer.nudge(username, pluginID)
		}
	}
}

// Start opens the system index and starts the system collector.
func (m *Manager) Start(ctx context.Context) error {
	if m.sysDB == nil {
		return nil // no DB, nothing to index
	}

	indexPath := filepath.Join(m.dataDir, ".search", "system.bleve")
	var err error
	schemas := m.pluginSchemas()
	m.systemIndex, err = OpenWithSchemas(indexPath, schemas)
	if err != nil {
		return fmt.Errorf("open system search index: %w", err)
	}

	m.systemCollector = NewCollector(m.log, m.systemIndex, func() *sql.DB { return m.sysDB })
	sysSources := []SearchSource{UserSource{}, GroupSource{}, VolumeSource{}}
	var sysTables []string
	for _, src := range sysSources {
		m.systemCollector.RegisterSource(src)
		sysTables = append(sysTables, src.Tables()...)
	}
	m.systemCollector.Start(ctx)

	// The system database goes through the same queue as the per-user ones, so
	// its index survives a crash between write and index, and a second replica
	// can pick up writes this one did not make.
	if err := cogdb.EnsureChangeCapture(m.sysDB, sysTables); err != nil {
		return fmt.Errorf("system change capture: %w", err)
	}

	// Full sync on startup.
	if err := m.systemCollector.FullSync(); err != nil {
		m.log.Error("system full sync failed", "err", err)
	}

	if m.Propagator != nil {
		m.Propagator.Start()
	}

	m.drainer = newDrainer(m)
	go m.drainer.run(m.ctx)

	return nil
}

// Stop stops all collectors and closes all indices.
func (m *Manager) Stop() {
	if m.Propagator != nil {
		m.Propagator.Stop()
	}
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
	m.cancel()

	// The drainer feeds the collectors and queries the plugin databases, and a
	// lazy open creates index files and runs a full sync. Both have to be
	// finished before any of that is torn down.
	if m.drainer != nil {
		m.drainer.wait()
	}
	m.background.Wait()

	if m.systemCollector != nil {
		m.systemCollector.Stop()
	}
	if m.systemIndex != nil {
		if err := m.systemIndex.Close(); err != nil {
			m.log.Error("close system index", "err", err)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for username, us := range m.userIndices {
		us.collector.Stop()
		if err := us.index.Close(); err != nil {
			m.log.Error("close user index", "user", username, "err", err)
		}
	}
}

// CloseUser stops indexing for username and closes their index, so their
// files can be removed and a later account of the same name starts empty.
func (m *Manager) CloseUser(username string) error {
	m.mu.Lock()
	us, ok := m.userIndices[username]
	delete(m.userIndices, username)
	m.mu.Unlock()
	if !ok {
		return nil
	}
	us.collector.Stop()
	if err := us.index.Close(); err != nil {
		return fmt.Errorf("close search index of %s: %w", username, err)
	}
	return nil
}

// SearchSystem queries the system index.
func (m *Manager) SearchSystem(query string, limit, offset int) (*Result, error) {
	if m.systemIndex == nil {
		return &Result{}, nil
	}
	return m.systemIndex.Search(query, limit, offset)
}

// SearchUser queries a user's per-user index. Copies of shared resources
// the user can no longer read are left out, and removed from the index.
func (m *Manager) SearchUser(username, query string, limit, offset int) (*Result, error) {
	us, err := m.ensureUserIndex(username)
	if err != nil {
		return nil, err
	}
	res, err := us.index.Search(query, limit, offset)
	if err != nil {
		return nil, err
	}
	return m.filterShared(username, us, res)
}

// SharedRef names a shared resource by its owner's username, its type and
// its ID: what a copy in a recipient's index was made from.
type SharedRef struct {
	Owner        string
	ResourceType string
	ResourceID   string
}

// parseSharedDocID reverses sharedDocID. A username holds no colon and a
// resource type exactly one, so the ID is everything after the type.
func parseSharedDocID(id string) (SharedRef, bool) {
	rest, ok := strings.CutPrefix(id, "shared:")
	if !ok {
		return SharedRef{}, false
	}
	parts := strings.SplitN(rest, ":", 4)
	if len(parts) != 4 {
		return SharedRef{}, false
	}
	return SharedRef{Owner: parts[0], ResourceType: parts[1] + ":" + parts[2], ResourceID: parts[3]}, true
}

func (m *Manager) filterShared(username string, us *userSearch, res *Result) (*Result, error) {
	var refs []SharedRef
	for _, h := range res.Hits {
		if ref, ok := parseSharedDocID(h.ID); ok {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return res, nil
	}
	readable := map[SharedRef]bool{}
	if m.SharedAccess != nil {
		var err error
		if readable, err = m.SharedAccess(username, refs); err != nil {
			return nil, fmt.Errorf("check shared results: %w", err)
		}
	}
	kept := res.Hits[:0]
	var stale []string
	for _, h := range res.Hits {
		if ref, ok := parseSharedDocID(h.ID); ok && !readable[ref] {
			stale = append(stale, h.ID)
			continue
		}
		kept = append(kept, h)
	}
	res.Hits = kept
	res.Total -= len(stale)
	if len(stale) > 0 {
		if err := us.index.Batch(nil, stale); err != nil {
			m.log.Error("remove stale shared results", "user", username, "err", err)
		}
	}
	return res, nil
}

// IndexSharedDocument indexes a document into a user's search index.
func (m *Manager) IndexSharedDocument(username string, doc Document) error {
	us, err := m.ensureUserIndex(username)
	if err != nil {
		return err
	}
	return us.index.Batch([]Document{doc}, nil)
}

// DeleteSharedDocument removes a document from a user's search index.
func (m *Manager) DeleteSharedDocument(username, docID string) error {
	us, err := m.ensureUserIndex(username)
	if err != nil {
		return err
	}
	return us.index.Batch(nil, []string{docID})
}

// ensureUserIndex opens a user's index, or returns the open one.
//
// Opening is done one caller at a time per user. Bleve takes a file lock on
// the index and holds it, so two goroutines opening the same index do not race
// and lose — the loser blocks on the lock forever, and so does everything that
// asks for that index afterwards. It is easy to arrive here concurrently: a
// burst of writes to a user with no open index gives every write's hook its
// own goroutine, all headed for the same file.
func (m *Manager) ensureUserIndex(username string) (*userSearch, error) {
	m.mu.Lock()
	if us, ok := m.userIndices[username]; ok {
		m.mu.Unlock()
		return us, nil
	}
	m.mu.Unlock()

	us, err, _ := m.opening.Do(username, func() (any, error) {
		return m.openUserIndex(username)
	})
	if err != nil {
		return nil, err
	}

	return us.(*userSearch), nil
}

func (m *Manager) openUserIndex(username string) (*userSearch, error) {
	// Re-check: a caller that shared an in-flight open returns above, but a
	// caller that arrives just after one finishes gets its own turn here.
	m.mu.Lock()
	if us, ok := m.userIndices[username]; ok {
		m.mu.Unlock()
		return us, nil
	}
	stopped := m.stopped
	m.mu.Unlock()

	if stopped {
		return nil, fmt.Errorf("search manager is stopped")
	}

	indexPath := filepath.Join(m.dataDir, username, ".search", "user.bleve")
	schemas := m.pluginSchemas()
	idx, err := OpenWithSchemas(indexPath, schemas)
	if err != nil {
		return nil, fmt.Errorf("open user search index for %s: %w", username, err)
	}

	collector := NewCollector(m.log, idx, func() *sql.DB { return nil })

	// Register plugin sources for this user.
	for pluginID, defs := range m.pluginSources {
		for _, def := range defs {
			src := newUserPluginSource(username, pluginID, def, m.userDBFunc, m.filesDirFunc)
			collector.RegisterSource(src)
		}
	}

	collector.Start(m.ctx)

	// Insert into map under lock. If someone else won the race, discard ours.
	m.mu.Lock()
	if existing, ok := m.userIndices[username]; ok {
		m.mu.Unlock()
		// Lost the race: shut down our duplicate and return the winner.
		collector.Stop()
		if err := idx.Close(); err != nil {
			m.log.Error("close duplicate user index", "user", username, "err", err)
		}
		return existing, nil
	}
	us := &userSearch{index: idx, collector: collector}
	m.userIndices[username] = us
	m.mu.Unlock()

	// FullSync runs without the manager lock — it issues SELECTs against the
	// per-user plugin DB and would otherwise block all other user-index opens.
	if err := collector.FullSync(); err != nil {
		m.log.Error("user full sync failed", "user", username, "err", err)
	}
	return us, nil
}

// FullSyncUser does a one-shot indexing pass for a user's per-plugin sources.
// Used by tests and during initial setup.
func (m *Manager) FullSyncUser(username string) error {
	us, err := m.ensureUserIndex(username)
	if err != nil {
		return err
	}
	return us.collector.FullSync()
}

// userPluginSource wraps PluginSearchSource but fetches the DB per-user.
type userPluginSource struct {
	username     string
	pluginID     string
	inner        *PluginSearchSource
	dbFunc       func(username, pluginID string) (*sql.DB, error)
	filesDirFunc func(username string) string
}

func newUserPluginSource(username, pluginID string, def plugin.SearchSourceDef, dbFunc func(string, string) (*sql.DB, error), filesDirFunc func(string) string) *userPluginSource {
	return &userPluginSource{
		username:     username,
		pluginID:     pluginID,
		inner:        NewPluginSearchSource(pluginID, def),
		dbFunc:       dbFunc,
		filesDirFunc: filesDirFunc,
	}
}

func (s *userPluginSource) Name() string     { return s.inner.Name() }
func (s *userPluginSource) Tables() []string { return s.inner.Tables() }

func (s *userPluginSource) SelfSync() ([]Document, error) {
	if s.dbFunc == nil {
		return nil, fmt.Errorf("no db func for user %s plugin %s", s.username, s.pluginID)
	}
	db, err := s.dbFunc(s.username, s.pluginID)
	if err != nil {
		return nil, err
	}

	var allDocs []Document
	for _, table := range s.inner.Tables() {
		rowIDs, err := tableRowIDs(db, table)
		if err != nil {
			slog.Error("self-sync query failed", "table", table, "err", err)
			continue
		}

		if len(rowIDs) == 0 {
			continue
		}

		// Enrich via the normal path (which uses its own DB).
		docs, err := s.Enrich(nil, rowIDs)
		if err != nil {
			return nil, err
		}
		allDocs = append(allDocs, docs...)
	}
	return allDocs, nil
}

func (s *userPluginSource) Enrich(_ *sql.DB, rowIDs []int64) ([]Document, error) {
	if s.dbFunc == nil {
		return nil, fmt.Errorf("no db func for user %s plugin %s", s.username, s.pluginID)
	}
	db, err := s.dbFunc(s.username, s.pluginID)
	if err != nil {
		return nil, err
	}
	if s.filesDirFunc != nil {
		s.inner.FilesDir = s.filesDirFunc(s.username)
	}
	return s.inner.Enrich(db, rowIDs)
}

// sqliteOpToOp converts sqlite3 update hook operation codes to search.Op.
// sqlite3 constants: SQLITE_INSERT=18, SQLITE_UPDATE=23, SQLITE_DELETE=9.
func sqliteOpToOp(op int) Op {
	switch op {
	case 18:
		return OpInsert
	case 23:
		return OpUpdate
	case 9:
		return OpDelete
	default:
		return OpUpdate
	}
}
