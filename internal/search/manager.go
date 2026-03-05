package search

import (
	"context"
	"database/sql"
	"fmt"
	"log"
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
}

// NewManager creates a manager. Call SetSystemDB and Start to initialize.
func NewManager(dataDir string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
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
func (m *Manager) UserDBHook(username, pluginID string) func(op int, db, table string, rowid int64) {
	return func(op int, dbName, table string, rowid int64) {
		m.mu.Lock()
		us, ok := m.userIndices[username]
		m.mu.Unlock()

		if !ok {
			// Lazily open user index on first hook.
			var err error
			us, err = m.ensureUserIndex(username)
			if err != nil {
				log.Printf("search: lazy open user index for %s: %v", username, err)
				return
			}
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

	m.systemCollector = NewCollector(m.systemIndex, func() *sql.DB { return m.sysDB })
	m.systemCollector.RegisterSource(UserSource{})
	m.systemCollector.RegisterSource(GroupSource{})
	m.systemCollector.RegisterSource(VolumeSource{})
	m.systemCollector.Start(ctx)

	// Full sync on startup.
	if err := m.systemCollector.FullSync(); err != nil {
		log.Printf("search: system full sync: %v", err)
	}

	return nil
}

// Stop stops all collectors and closes all indices.
func (m *Manager) Stop() {
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

func (m *Manager) ensureUserIndex(username string) (*userSearch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if us, ok := m.userIndices[username]; ok {
		return us, nil
	}

	indexPath := filepath.Join(m.dataDir, username, ".search", "user.bleve")
	idx, err := Open(indexPath)
	if err != nil {
		return nil, fmt.Errorf("open user search index for %s: %w", username, err)
	}

	// Create a collector that uses the user's plugin DB for enrichment.
	us := &userSearch{index: idx}

	// For per-user indices, we need a dbFunc that returns the right DB.
	// We'll register plugin sources and each one gets its own dbFunc call.
	collector := NewCollector(idx, func() *sql.DB {
		// Return first available plugin DB for this user.
		// Each plugin source will use its own DB via Enrich.
		return nil
	})

	// Register plugin sources for this user.
	for pluginID, defs := range m.pluginSources {
		for _, def := range defs {
			src := newUserPluginSource(username, pluginID, def, m.userDBFunc, m.filesDirFunc)
			collector.RegisterSource(src)
		}
	}

	collector.Start(m.ctx)

	// Full sync on first open.
	if err := collector.FullSync(); err != nil {
		log.Printf("search: user %s full sync: %v", username, err)
	}

	us.collector = collector
	m.userIndices[username] = us
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
			log.Printf("search: self-sync query %s: %v", table, err)
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
