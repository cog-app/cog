package plugin

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

// Registry manages plugin lifecycle, per-user database provisioning,
// and HTTP route mounting.
type Registry struct {
	dataDir string
	plugins map[string]Plugin
	mux     *http.ServeMux

	// pluginRegistrars tracks registrations made during Register().
	registrars map[string]*pluginRegistrar

	// Per-user plugin DB cache: map[userID+pluginID] -> *sql.DB
	mu    sync.Mutex
	dbs   map[string]*sql.DB
	order []string // plugin IDs in registration order
}

// NewRegistry creates a new plugin registry.
// dataDir is the root of user data (e.g. /var/lib/cog/data).
func NewRegistry(dataDir string, mux *http.ServeMux) *Registry {
	return &Registry{
		dataDir:    dataDir,
		plugins:    make(map[string]Plugin),
		mux:        mux,
		registrars: make(map[string]*pluginRegistrar),
		dbs:        make(map[string]*sql.DB),
	}
}

// Add registers a plugin with the registry.
func (r *Registry) Add(p Plugin) error {
	id := p.ID()
	if _, exists := r.plugins[id]; exists {
		return fmt.Errorf("plugin %q already registered", id)
	}
	r.plugins[id] = p
	r.order = append(r.order, id)

	reg := &pluginRegistrar{
		pluginID:  id,
		mux:       r.mux,
		resources: make(map[string]ResourceDef),
		hooks:     make(map[string][]HookCallback),
	}
	r.registrars[id] = reg

	return p.Register(reg)
}

// Start starts all registered plugins in registration order.
func (r *Registry) Start(ctx context.Context) error {
	for _, id := range r.order {
		if err := r.plugins[id].Start(ctx); err != nil {
			return fmt.Errorf("start plugin %q: %w", id, err)
		}
		log.Printf("plugin %q started", id)
	}
	return nil
}

// Stop stops all plugins in reverse registration order.
func (r *Registry) Stop() {
	for i := len(r.order) - 1; i >= 0; i-- {
		id := r.order[i]
		if err := r.plugins[id].Stop(); err != nil {
			log.Printf("plugin %q stop error: %v", id, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, db := range r.dbs {
		db.Close()
	}
}

// PluginDB returns (or opens) the per-user plugin database.
// Path: {dataDir}/{username}/app_data/{pluginID}/plugin.db
func (r *Registry) PluginDB(username, pluginID string) (*sql.DB, error) {
	key := username + ":" + pluginID
	r.mu.Lock()
	defer r.mu.Unlock()

	if db, ok := r.dbs[key]; ok {
		return db, nil
	}

	dir := filepath.Join(r.dataDir, username, "app_data", pluginID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create plugin data dir: %w", err)
	}

	dbPath := filepath.Join(dir, "plugin.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open plugin db %s: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	r.dbs[key] = db
	return db, nil
}

// UserFilesDir returns the path to the user's files directory.
func (r *Registry) UserFilesDir(username string) string {
	return filepath.Join(r.dataDir, username, "files")
}

// EnsureUserDirs creates the standard directory structure for a user.
func (r *Registry) EnsureUserDirs(username string) error {
	dirs := []string{
		filepath.Join(r.dataDir, username, "files"),
		filepath.Join(r.dataDir, username, "app_data"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create user dir %s: %w", dir, err)
		}
	}
	return nil
}

// ResourceDefs returns all resource definitions across all plugins.
func (r *Registry) ResourceDefs() map[string][]ResourceDef {
	defs := make(map[string][]ResourceDef)
	for id, reg := range r.registrars {
		for _, def := range reg.resources {
			defs[id] = append(defs[id], def)
		}
	}
	return defs
}

// FireHook dispatches a hook event to all subscribers.
func (r *Registry) FireHook(ctx context.Context, hookName string, data any) {
	for _, reg := range r.registrars {
		for _, cb := range reg.hooks[hookName] {
			cb(ctx, data)
		}
	}
}

// Plugin returns a registered plugin by ID.
func (r *Registry) Plugin(id string) (Plugin, bool) {
	p, ok := r.plugins[id]
	return p, ok
}

// pluginRegistrar implements Registrar for a specific plugin.
type pluginRegistrar struct {
	pluginID  string
	mux       *http.ServeMux
	resources map[string]ResourceDef
	hooks     map[string][]HookCallback
}

func (pr *pluginRegistrar) RegisterAPI(path string, handler http.Handler) {
	pattern := fmt.Sprintf("/api/%s/%s", pr.pluginID, path)
	pr.mux.Handle(pattern, handler)
}

func (pr *pluginRegistrar) RegisterPage(path string, handler http.Handler) {
	pattern := fmt.Sprintf("/apps/%s/%s", pr.pluginID, path)
	pr.mux.Handle(pattern, handler)
}

func (pr *pluginRegistrar) RegisterAction(method, path string, handler http.Handler) {
	pattern := fmt.Sprintf("%s /apps/%s/%s", method, pr.pluginID, path)
	pr.mux.Handle(pattern, handler)
}

func (pr *pluginRegistrar) RegisterResource(def ResourceDef) {
	pr.resources[def.Type] = def
	// Mount the resource handler at /apps/{plugin_id}/{type}/{id}
	pattern := fmt.Sprintf("/apps/%s/%s/", pr.pluginID, def.Type)
	pr.mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resourceID := r.PathValue("id")
		if resourceID == "" {
			// Extract from URL path
			resourceID = filepath.Base(r.URL.Path)
		}
		def.Handler.ServeResource(w, r, resourceID)
	}))
}

func (pr *pluginRegistrar) RegisterHook(hookName string, callback HookCallback) {
	pr.hooks[hookName] = append(pr.hooks[hookName], callback)
}
