package plugin

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/db"
	_ "github.com/mattn/go-sqlite3"
)

var openHooked = db.OpenHooked

// MountResolver resolves user file paths via the mount system.
type MountResolver interface {
	UserFilesDir(username string) (string, error)
	EnsureUserDirs(username string) error
}

// Registry manages plugin lifecycle, per-user database provisioning,
// and HTTP route mounting.
type Registry struct {
	dataDir       string
	plugins       map[string]Plugin
	mux           *http.ServeMux
	mountResolver MountResolver

	// pluginRegistrars tracks registrations made during Register().
	registrars map[string]*pluginRegistrar

	// Per-user plugin DB cache: map[userID+pluginID] -> *sql.DB
	mu    sync.Mutex
	dbs   map[string]*sql.DB
	order []string // plugin IDs in registration order

	// Plugin enable/disable state.
	enabled    map[string]bool // true = enabled (default)
	protected  map[string]bool // plugins that cannot be disabled
	sysDB      *sql.DB         // system database for persisting plugin settings
	csrfSecret []byte          // CSRF secret for plugin-scoped tokens

	// dbHookFactory creates update hooks for per-user plugin databases.
	dbHookFactory func(username, pluginID string) func(int, string, string, int64)
}

// NewRegistry creates a new plugin registry.
// dataDir is the root of user data (e.g. /var/lib/cog/data).
// csrfSecret is used for plugin-scoped CSRF token generation.
func NewRegistry(dataDir string, mux *http.ServeMux, sysDB *sql.DB, csrfSecret []byte) *Registry {
	r := &Registry{
		dataDir:    dataDir,
		plugins:    make(map[string]Plugin),
		mux:        mux,
		registrars: make(map[string]*pluginRegistrar),
		dbs:        make(map[string]*sql.DB),
		enabled:    make(map[string]bool),
		protected:  make(map[string]bool),
		sysDB:      sysDB,
		csrfSecret: csrfSecret,
	}
	r.loadEnabledState()
	return r
}

// Mux returns the ServeMux that plugin routes are registered on.
func (r *Registry) Mux() *http.ServeMux {
	return r.mux
}

// Add registers a plugin with the registry.
// If the plugin embeds Protected, it is automatically marked as always-on.
func (r *Registry) Add(p Plugin) error {
	id := p.ID()
	if _, exists := r.plugins[id]; exists {
		return fmt.Errorf("plugin %q already registered", id)
	}
	r.plugins[id] = p
	r.order = append(r.order, id)

	if IsProtected(p) {
		r.protected[id] = true
	}

	reg := &pluginRegistrar{
		pluginID:   id,
		mux:        r.mux,
		registry:   r,
		csrfSecret: r.csrfSecret,
		resources:  make(map[string]ResourceDef),
		hooks:      make(map[string][]HookCallback),
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
	var pdb *sql.DB
	if r.dbHookFactory != nil {
		hook := r.dbHookFactory(username, pluginID)
		var err error
		pdb, err = openHooked(dbPath, hook)
		if err != nil {
			return nil, fmt.Errorf("open hooked plugin db %s: %w", dbPath, err)
		}
	} else {
		var err error
		pdb, err = sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
		if err != nil {
			return nil, fmt.Errorf("open plugin db %s: %w", dbPath, err)
		}
	}
	r.dbs[key] = pdb
	return pdb, nil
}

// UserDB returns (or opens) the per-user database.
// Path: {dataDir}/{username}/user.db
// Creates user_settings and ssh_keys tables on first access.
func (r *Registry) UserDB(username string) (*sql.DB, error) {
	key := username + ":__user__"
	r.mu.Lock()
	defer r.mu.Unlock()

	if db, ok := r.dbs[key]; ok {
		return db, nil
	}

	dir := filepath.Join(r.dataDir, username)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create user dir: %w", err)
	}

	dbPath := filepath.Join(dir, "user.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open user db %s: %w", dbPath, err)
	}

	if err := migrateUserDB(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate user db: %w", err)
	}

	r.dbs[key] = db
	return db, nil
}

func migrateUserDB(db *sql.DB) error {
	const schema = `
	CREATE TABLE IF NOT EXISTS user_settings (
		key TEXT PRIMARY KEY,
		value BLOB,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS ssh_keys (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		public_key TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);
	`
	_, err := db.Exec(schema)
	return err
}

// SetDBHookFactory sets a factory function for creating SQLite update hooks
// on per-user plugin databases.
func (r *Registry) SetDBHookFactory(f func(username, pluginID string) func(int, string, string, int64)) {
	r.dbHookFactory = f
}

// SetMountResolver sets an optional mount resolver for path resolution.
func (r *Registry) SetMountResolver(mr MountResolver) {
	r.mountResolver = mr
}

// UserFilesDir returns the path to the user's files directory.
// If a mount resolver is set, it delegates to it; otherwise falls back to the hardcoded path.
func (r *Registry) UserFilesDir(username string) string {
	if r.mountResolver != nil {
		if dir, err := r.mountResolver.UserFilesDir(username); err == nil {
			return dir
		}
	}
	return filepath.Join(r.dataDir, username, "files")
}

// EnsureUserDirs creates the standard directory structure for a user.
// If a mount resolver is set, it delegates to it for the files directory.
func (r *Registry) EnsureUserDirs(username string) error {
	if r.mountResolver != nil {
		if err := r.mountResolver.EnsureUserDirs(username); err != nil {
			log.Printf("mount resolver EnsureUserDirs failed for %q, falling back: %v", username, err)
		}
	}

	// Always ensure app_data directory exists.
	appDataDir := filepath.Join(r.dataDir, username, "app_data")
	if err := os.MkdirAll(appDataDir, 0700); err != nil {
		return fmt.Errorf("create user dir %s: %w", appDataDir, err)
	}

	// Ensure the files directory from the resolved path.
	filesDir := r.UserFilesDir(username)
	if err := os.MkdirAll(filesDir, 0700); err != nil {
		return fmt.Errorf("create user dir %s: %w", filesDir, err)
	}

	return nil
}

// ResourceDefByType looks up a resource definition by its full type string
// (e.g., "calendar:calendar", "contacts:address_book").
// Returns the def and true if found, zero value and false otherwise.
func (r *Registry) ResourceDefByType(fullType string) (ResourceDef, bool) {
	parts := strings.SplitN(fullType, ":", 2)
	if len(parts) != 2 {
		return ResourceDef{}, false
	}
	pluginID, resType := parts[0], parts[1]
	reg, ok := r.registrars[pluginID]
	if !ok {
		return ResourceDef{}, false
	}
	def, ok := reg.resources[resType]
	return def, ok
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
	pluginID      string
	mux           *http.ServeMux
	registry      *Registry
	csrfSecret    []byte
	resources     map[string]ResourceDef
	hooks         map[string][]HookCallback
	navItems      []NavItem
	widgets       []Widget
	searchSources []SearchSourceDef
}

// wrap applies RequirePluginEnabled middleware to a handler.
func (pr *pluginRegistrar) wrap(h http.Handler) http.Handler {
	return pr.registry.RequirePluginEnabled(pr.pluginID)(h)
}

func (pr *pluginRegistrar) RegisterPage(path string, handler http.Handler) {
	pattern := fmt.Sprintf("GET /apps/%s/%s", pr.pluginID, path)
	pr.mux.Handle(pattern, pr.wrap(auth.RequireAuthWeb(handler)))
}

func (pr *pluginRegistrar) RegisterAPI(path string, handler http.Handler) {
	pattern := fmt.Sprintf("GET /apps/%s/%s", pr.pluginID, path)
	pr.mux.Handle(pattern, pr.wrap(auth.RequireAuth(handler)))
}

func (pr *pluginRegistrar) RegisterAction(method, path string, handler http.Handler) {
	pattern := fmt.Sprintf("%s /apps/%s/%s", method, pr.pluginID, path)
	wrapped := auth.RequireAuthWeb(auth.RequireCSRF(pr.csrfSecret, handler))
	pr.mux.Handle(pattern, pr.wrap(wrapped))
}

func (pr *pluginRegistrar) RegisterRoute(pattern string, handler http.Handler) {
	pr.mux.Handle(pattern, pr.wrap(auth.RequireAuth(handler)))
}

func (pr *pluginRegistrar) RegisterResource(def ResourceDef) {
	pr.resources[def.Type] = def
	if def.Handler != nil {
		// Mount the resource handler at /apps/{plugin_id}/{type}/{id}
		pattern := fmt.Sprintf("/apps/%s/%s/", pr.pluginID, def.Type)
		pr.mux.Handle(pattern, pr.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resourceID := r.PathValue("id")
			if resourceID == "" {
				// Extract from URL path
				resourceID = filepath.Base(r.URL.Path)
			}
			def.Handler.ServeResource(w, r, resourceID)
		})))
	}
}

func (pr *pluginRegistrar) RegisterHook(hookName string, callback HookCallback) {
	pr.hooks[hookName] = append(pr.hooks[hookName], callback)
}

func (pr *pluginRegistrar) RegisterNavItem(item NavItem) {
	pr.navItems = append(pr.navItems, item)
}

func (pr *pluginRegistrar) RegisterWidget(w Widget) {
	pr.widgets = append(pr.widgets, w)
}

func (pr *pluginRegistrar) RegisterSearchSource(def SearchSourceDef) {
	pr.searchSources = append(pr.searchSources, def)
}

// SearchSourceDefs returns all registered search source definitions, keyed by plugin ID.
func (r *Registry) SearchSourceDefs() map[string][]SearchSourceDef {
	defs := make(map[string][]SearchSourceDef)
	for id, reg := range r.registrars {
		if len(reg.searchSources) > 0 {
			defs[id] = reg.searchSources
		}
	}
	return defs
}

// --- Plugin enable/disable ---

// IsPluginProtected returns true if a plugin cannot be disabled.
func (r *Registry) IsPluginProtected(pluginID string) bool {
	return r.protected[pluginID]
}

// IsEnabled returns true if the plugin is currently enabled.
func (r *Registry) IsEnabled(pluginID string) bool {
	if r.IsPluginProtected(pluginID) {
		return true
	}
	enabled, ok := r.enabled[pluginID]
	if !ok {
		return true // default enabled
	}
	return enabled
}

// SetEnabled enables or disables a plugin and persists the state.
func (r *Registry) SetEnabled(pluginID string, enabled bool) {
	if r.IsPluginProtected(pluginID) {
		return
	}
	r.enabled[pluginID] = enabled
	if r.sysDB != nil {
		r.sysDB.Exec(
			`INSERT INTO plugin_settings (plugin_id, enabled) VALUES (?, ?) ON CONFLICT(plugin_id) DO UPDATE SET enabled = ?`,
			pluginID, boolToInt(enabled), boolToInt(enabled),
		)
	}
}

// PluginIDs returns all registered plugin IDs in registration order.
func (r *Registry) PluginIDs() []string {
	return r.order
}

func (r *Registry) loadEnabledState() {
	if r.sysDB == nil {
		return
	}
	rows, err := r.sysDB.Query(`SELECT plugin_id, enabled FROM plugin_settings`)
	if err != nil {
		return // table may not exist yet
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var enabled int
		if rows.Scan(&id, &enabled) == nil {
			r.enabled[id] = enabled != 0
		}
	}
}

// NavItems returns nav items from all enabled plugins.
func (r *Registry) NavItems() []NavItem {
	var items []NavItem
	for _, id := range r.order {
		if !r.IsEnabled(id) {
			continue
		}
		reg := r.registrars[id]
		items = append(items, reg.navItems...)
	}
	return items
}

// Widgets returns widgets from all enabled plugins, sorted by priority.
func (r *Registry) Widgets() []Widget {
	var widgets []Widget
	for _, id := range r.order {
		if !r.IsEnabled(id) {
			continue
		}
		reg := r.registrars[id]
		widgets = append(widgets, reg.widgets...)
	}
	sort.Slice(widgets, func(i, j int) bool {
		return widgets[i].Priority < widgets[j].Priority
	})
	return widgets
}

// RequirePluginEnabled returns middleware that 404s if the plugin is disabled.
func (r *Registry) RequirePluginEnabled(pluginID string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if !r.IsEnabled(pluginID) {
				http.NotFound(w, req)
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
