package search

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

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

// SystemHook returns a hook function for the system database that feeds
// changes into the system collector.
func (m *Manager) SystemHook() func(op int, db, table string, rowid int64) {
	return func(op int, dbName, table string, rowid int64) {
		if m.systemCollector == nil {
			return
		}
		m.systemCollector.Send(Change{
			Op:    sqliteOpToOp(op),
			Table: table,
			RowID: rowid,
		})
	}
}

// UserDBHook returns a hook function for a per-user plugin database.
//
// The hook is invoked synchronously inside the SQLite write path; doing any
// SQL here (via FullSync etc.) would deadlock the per-user DB. So this hook
// only forwards changes when the user's index is already open. If it is not,
// initialization is kicked off in a background goroutine — that init's
// FullSync sweeps the same row we just dropped, so no change is lost.
func (m *Manager) UserDBHook(username, pluginID string) func(op int, db, table string, rowid int64) {
	return func(op int, dbName, table string, rowid int64) {
		m.mu.Lock()
		us, ok := m.userIndices[username]
		m.mu.Unlock()

		if !ok {
			// Index not opened yet; init asynchronously and drop this change.
			// ensureUserIndex performs a FullSync on first open, which will
			// pick up the row we just saw.
			go func() {
				if _, err := m.ensureUserIndex(username); err != nil {
					m.log.Error("lazy open user index failed", "user", username, "err", err)
				}
			}()
			return
		}

		us.collector.Send(Change{
			Op:    sqliteOpToOp(op),
			Table: table,
			RowID: rowid,
		})
	}
}

// Start opens the system index and starts the system collector.
func (m *Manager) Start(ctx context.Context) error {
	if m.sysDB == nil {
		return nil // no DB, nothing to index
	}

	indexPath := filepath.Join(m.dataDir, ".search", "system.bleve")
	var err error
	m.systemIndex, err = Open(indexPath)
	if err != nil {
		return fmt.Errorf("open system search index: %w", err)
	}

	m.systemCollector = NewCollector(m.log, m.systemIndex, func() *sql.DB { return m.sysDB })
	m.systemCollector.RegisterSource(UserSource{})
	m.systemCollector.RegisterSource(GroupSource{})
	m.systemCollector.RegisterSource(VolumeSource{})
	m.systemCollector.Start(ctx)

	// Full sync on startup.
	if err := m.systemCollector.FullSync(); err != nil {
		m.log.Error("system full sync failed", "err", err)
	}

	if m.Propagator != nil {
		m.Propagator.Start()
	}

	return nil
}

// Stop stops all collectors and closes all indices.
func (m *Manager) Stop() {
	if m.Propagator != nil {
		m.Propagator.Stop()
	}
	m.cancel()

	if m.systemCollector != nil {
		m.systemCollector.Stop()
	}
	if m.systemIndex != nil {
		m.systemIndex.Close()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, us := range m.userIndices {
		us.collector.Stop()
		us.index.Close()
	}
}

// SearchSystem queries the system index.
func (m *Manager) SearchSystem(query string, limit, offset int) (*Result, error) {
	if m.systemIndex == nil {
		return &Result{}, nil
	}
	return m.systemIndex.Search(query, limit, offset)
}

// SearchUser queries a user's per-user index.
func (m *Manager) SearchUser(username, query string, limit, offset int) (*Result, error) {
	us, err := m.ensureUserIndex(username)
	if err != nil {
		return nil, err
	}
	return us.index.Search(query, limit, offset)
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

func (m *Manager) ensureUserIndex(username string) (*userSearch, error) {
	// Fast path: lock just to read.
	m.mu.Lock()
	if us, ok := m.userIndices[username]; ok {
		m.mu.Unlock()
		return us, nil
	}
	m.mu.Unlock()

	indexPath := filepath.Join(m.dataDir, username, ".search", "user.bleve")
	idx, err := Open(indexPath)
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
		idx.Close()
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
		rows, err := db.Query(fmt.Sprintf("SELECT rowid FROM %s", table))
		if err != nil {
			slog.Error("self-sync query failed", "table", table, "err", err)
			continue
		}

		var rowIDs []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				continue
			}
			rowIDs = append(rowIDs, id)
		}
		rows.Close()

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
