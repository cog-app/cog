package plugin

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"net/http/httptest"

	"github.com/a-h/templ"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/ui"
	sqlite3 "github.com/mattn/go-sqlite3"
)

// MountResolver resolves user file paths via the mount system.
type MountResolver interface {
	UserFilesDir(username string) (string, error)
	EnsureUserDirs(username string) error
	UserFS(username string) (vfs.FS, error)
}

// QuotaChecker checks storage quotas for users.
type QuotaChecker interface {
	// QuotaCheck returns nil if the user has sufficient quota, or plugin.ErrQuotaExceeded.
	QuotaCheck(username string, additionalBytes int64) error
}

// RegistryServices holds optional services that are wired into plugin contexts.
type RegistryServices struct {
	ShareStore   ShareStore
	EmailSender  EmailSender
	UserLookup   UserLookup
	GroupStore   GroupStore
	FilesAccess  FilesAccess
	QuotaChecker QuotaChecker
	VolumeAccess VolumeAccess
	ActivityLog  ActivityLogger
	Cookies      CookieConfig
}

// Registry manages plugin lifecycle, per-user database provisioning,
// and HTTP route mounting.
type Registry struct {
	log           *slog.Logger
	dataDir       string
	rootDataDir   string
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
	clock      clock.Clock     // virtual clock for testability

	// dbHookFactory creates update hooks for per-user plugin databases.
	dbHookFactory func(username, pluginID string) func(int, string, string, int64)

	// connectHooks are applied to every new SQLite connection (e.g. clock override).
	connectHooks []db.ConnectHook

	// Optional services available to plugins.
	services RegistryServices

	// discovered tracks process plugins found in PATH that are disabled.
	// Maps plugin ID → binary path. Only populated for disabled plugins
	// (enabled ones are loaded via LoadExternal and appear in plugins map).
	discovered map[string]string
}

// NewRegistry creates a new plugin registry.
// dataDir is the root of user data (e.g. /var/lib/cog/data).
// csrfSecret is used for plugin-scoped CSRF token generation.
func NewRegistry(log *slog.Logger, dataDir string, mux *http.ServeMux, sysDB *sql.DB, csrfSecret []byte, c clock.Clock) *Registry {
	r := &Registry{
		log:         log,
		dataDir:     dataDir,
		rootDataDir: filepath.Join(dataDir, "app_data"),
		plugins:     make(map[string]Plugin),
		mux:         mux,
		registrars:  make(map[string]*pluginRegistrar),
		dbs:         make(map[string]*sql.DB),
		enabled:     make(map[string]bool),
		protected:   make(map[string]bool),
		sysDB:       sysDB,
		csrfSecret:  csrfSecret,
		clock:       c,
	}
	r.discovered = make(map[string]string)
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
// Panics in a plugin's Start are recovered and logged so one bad plugin
// cannot kill the host process; the offending plugin is still considered
// started (its routes are already mounted) but its background goroutines may
// not have launched.
func (r *Registry) Start(ctx context.Context) error {
	for _, id := range r.order {
		if err := r.startOne(ctx, id); err != nil {
			return fmt.Errorf("start plugin %q: %w", id, err)
		}
		r.log.Info("plugin started", "plugin", id)
	}
	return nil
}

func (r *Registry) startOne(ctx context.Context, id string) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("panic in plugin Start", "plugin", id, "panic", rec)
			err = fmt.Errorf("plugin %q panicked in Start: %v", id, rec)
		}
	}()
	return r.plugins[id].Start(ctx)
}

// Stop stops all plugins in reverse registration order.
func (r *Registry) Stop() {
	for i := len(r.order) - 1; i >= 0; i-- {
		id := r.order[i]
		if err := r.plugins[id].Stop(); err != nil {
			r.log.Error("plugin stop failed", "plugin", id, "err", err)
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
	pdb, err := r.openDB(dbPath, username, pluginID)
	if err != nil {
		return nil, fmt.Errorf("open plugin db %s: %w", dbPath, err)
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
	db, err := r.openDB(dbPath, "", "")
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

// openDB opens a SQLite database, composing connect hooks (clock override etc.)
// with the optional update hook from dbHookFactory.
func (r *Registry) openDB(path, username, pluginID string) (*sql.DB, error) {
	var hooks []db.ConnectHook
	hooks = append(hooks, r.connectHooks...)
	if r.dbHookFactory != nil && username != "" {
		hook := r.dbHookFactory(username, pluginID)
		hooks = append(hooks, func(conn *sqlite3.SQLiteConn) error {
			conn.RegisterUpdateHook(hook)
			return nil
		})
	}
	if len(hooks) > 0 {
		return db.OpenWithHooks(path, hooks...)
	}
	return sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
}

// SetDBHookFactory sets a factory function for creating SQLite update hooks
// on per-user plugin databases.
func (r *Registry) SetDBHookFactory(f func(username, pluginID string) func(int, string, string, int64)) {
	r.dbHookFactory = f
}

// SetConnectHooks sets hooks applied to every new SQLite connection opened
// by the registry (e.g. clock override for tests).
func (r *Registry) SetConnectHooks(hooks ...db.ConnectHook) {
	r.connectHooks = hooks
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
			r.log.Warn("mount resolver EnsureUserDirs failed, falling back", "user", username, "err", err)
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

// UserVFS returns a mount-aware VFS for the given user, or nil on error.
func (r *Registry) UserVFS(username string) vfs.FS {
	if r.mountResolver != nil {
		if fs, err := r.mountResolver.UserFS(username); err == nil {
			return fs
		}
	}
	return nil
}

// UserFS returns a mount-aware VFS for the given user.
func (r *Registry) UserFS(username string) (vfs.FS, error) {
	if r.mountResolver != nil {
		return r.mountResolver.UserFS(username)
	}
	return nil, fmt.Errorf("no mount resolver configured")
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
// Each callback runs under a recover; a panicking subscriber must not take
// down the request handler that fired the hook.
func (r *Registry) FireHook(ctx context.Context, hookName string, data any) {
	for pluginID, reg := range r.registrars {
		for _, cb := range reg.hooks[hookName] {
			r.safeCallback(pluginID, hookName, cb, ctx, data)
		}
	}
}

func (r *Registry) safeCallback(pluginID, hookName string, cb HookCallback, ctx context.Context, data any) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("panic in hook subscriber",
				"plugin", pluginID, "hook", hookName, "panic", rec)
		}
	}()
	cb(ctx, data)
}

// Plugin returns a registered plugin by ID.
func (r *Registry) Plugin(id string) (Plugin, bool) {
	p, ok := r.plugins[id]
	return p, ok
}

// Plugins returns all registered plugins.
func (r *Registry) Plugins() iter.Seq[Plugin] {
	return func(yield func(Plugin) bool) {
		for _, id := range r.order {
			if !yield(r.plugins[id]) {
				return
			}
		}
	}
}

// SetServices sets optional services available to plugins via PluginContext.
// Must be called before Add() to ensure plugins have access during Register().
func (r *Registry) SetServices(s RegistryServices) {
	r.services = s
}

// RootDB returns (or opens) a plugin-global database (not per-user).
// Path: {rootDataDir}/{pluginID}/root.db
func (r *Registry) RootDB(pluginID string) (*sql.DB, error) {
	key := "__root__:" + pluginID
	r.mu.Lock()
	defer r.mu.Unlock()

	if db, ok := r.dbs[key]; ok {
		return db, nil
	}

	dir := filepath.Join(r.rootDataDir, pluginID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create root data dir: %w", err)
	}

	dbPath := filepath.Join(dir, "root.db")
	rdb, err := r.openDB(dbPath, "", "")
	if err != nil {
		return nil, fmt.Errorf("open root db %s: %w", dbPath, err)
	}
	r.dbs[key] = rdb
	return rdb, nil
}

// SettingsItems returns sidebar items for plugins that have settings pages.
// pluginDisplayName returns the display name for a plugin.
// It uses the first nav item label if available, otherwise the plugin ID.
func (r *Registry) pluginDisplayName(id string) string {
	if reg, ok := r.registrars[id]; ok && len(reg.navItems) > 0 {
		return reg.navItems[0].Label
	}
	return id
}

// SettingsItems returns sidebar items for plugins that have settings pages.
func (r *Registry) SettingsItems() []ui.SidebarItem {
	var items []ui.SidebarItem
	for _, id := range r.order {
		if !r.IsEnabled(id) {
			continue
		}
		reg := r.registrars[id]
		// Collect only pages with a GET handler (skip action-only pages).
		var visible []SettingsPage
		for _, sp := range reg.settingsPages {
			if sp.Handler != nil {
				visible = append(visible, sp)
			}
		}
		if len(visible) == 0 {
			continue
		}
		displayName := r.pluginDisplayName(id)
		if len(visible) == 1 {
			sp := visible[0]
			label := displayName
			if sp.Name != "" {
				label = displayName + ": " + sp.Name
			}
			url := "/admin/plugins/" + id
			if sp.Slug != "" {
				url += "/" + sp.Slug
			}
			items = append(items, ui.SidebarItem{
				Label: label,
				URL:   url,
			})
		} else {
			parent := ui.SidebarItem{Label: displayName}
			for _, sp := range visible {
				url := "/admin/plugins/" + id
				if sp.Slug != "" {
					url += "/" + sp.Slug
				}
				child := ui.SidebarItem{
					Label: sp.Name,
					URL:   url,
				}
				parent.Children = append(parent.Children, child)
			}
			items = append(items, parent)
		}
	}
	return items
}

// pluginRegistrar implements PluginContext and PrivilegedContext for a specific plugin.
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
	commands      []CommandDef
	adminCommands []CommandDef
	settingsPages []SettingsPage
}

// --- PluginContext service methods ---

func (pr *pluginRegistrar) PluginDB(username string) (*sql.DB, error) {
	return pr.registry.PluginDB(username, pr.pluginID)
}

func (pr *pluginRegistrar) UserFilesDir(username string) string {
	return pr.registry.UserFilesDir(username)
}

func (pr *pluginRegistrar) EnsureUserDirs(username string) error {
	return pr.registry.EnsureUserDirs(username)
}

func (pr *pluginRegistrar) DataDir() string {
	return pr.registry.dataDir
}

func (pr *pluginRegistrar) NavItems() []NavItem {
	return pr.registry.NavItems()
}

func (pr *pluginRegistrar) Widgets() []Widget {
	return pr.registry.Widgets()
}

func (pr *pluginRegistrar) CSRFSecret() []byte {
	return pr.csrfSecret
}

func (pr *pluginRegistrar) Logger() *slog.Logger {
	return pr.registry.log.With("plugin", pr.pluginID)
}

func (pr *pluginRegistrar) Now() time.Time {
	return pr.registry.clock()
}

func (pr *pluginRegistrar) ShareStore() ShareStore {
	return pr.registry.services.ShareStore
}

func (pr *pluginRegistrar) EmailSender() EmailSender {
	return pr.registry.services.EmailSender
}

func (pr *pluginRegistrar) UserLookup() UserLookup {
	return pr.registry.services.UserLookup
}

func (pr *pluginRegistrar) GroupStore() GroupStore {
	return pr.registry.services.GroupStore
}

func (pr *pluginRegistrar) FilesAccess() FilesAccess {
	return pr.registry.services.FilesAccess
}

func (pr *pluginRegistrar) VolumeAccess() VolumeAccess {
	return pr.registry.services.VolumeAccess
}

func (pr *pluginRegistrar) ActivityLog() ActivityLogger {
	return pr.registry.services.ActivityLog
}

func (pr *pluginRegistrar) Cookies() CookieConfig {
	return pr.registry.services.Cookies
}

func (pr *pluginRegistrar) QuotaCheck(username string, additionalBytes int64) error {
	if pr.registry.services.QuotaChecker == nil {
		return nil
	}
	return pr.registry.services.QuotaChecker.QuotaCheck(username, additionalBytes)
}

func (pr *pluginRegistrar) UserVFS(username string) vfs.FS {
	return pr.registry.UserVFS(username)
}

// wrap applies RequirePluginEnabled + panic recovery to a handler.
// Every plugin-registered HTTP handler eventually flows through here.
func (pr *pluginRegistrar) wrap(h http.Handler) http.Handler {
	return pr.registry.RequirePluginEnabled(pr.pluginID)(pr.recover(h))
}

// recover wraps a plugin handler with panic recovery. A panic logs the
// plugin ID + request path and returns 500 to the client instead of killing
// the host process.
func (pr *pluginRegistrar) recover(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				pr.registry.log.Error("panic in plugin handler",
					"plugin", pr.pluginID, "path", r.URL.Path, "method", r.Method, "panic", rec)
				// Best-effort 500. If headers already sent, the client sees a
				// truncated response — better than the whole server dying.
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		h.ServeHTTP(w, r)
	})
}

func (pr *pluginRegistrar) RegisterPage(path string, handler http.Handler) {
	pattern := fmt.Sprintf("GET /apps/%s/%s", pr.pluginID, path)
	wrapped := auth.RequireAuthWeb(pr.injectPluginContext(pr.shellMiddleware(handler)))
	pr.mux.Handle(pattern, pr.wrap(wrapped))
}

// injectPluginContext injects plugin.UserFromContext and plugin.CSRFTokenFromContext
// into the request context so plugins can use them from any handler type.
func (pr *pluginRegistrar) injectPluginContext(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		sessID := auth.SessionIDFromContext(r.Context())
		var csrf string
		if sessID != "" {
			csrf = auth.CSRFToken(sessID, pr.csrfSecret)
		}

		ctx := cogplugin.WithCSRFToken(r.Context(), csrf)
		ctx = cogplugin.WithPluginDB(ctx, func(username string) (*sql.DB, error) {
			return pr.registry.PluginDB(username, pr.pluginID)
		})
		if u != nil {
			ctx = cogplugin.WithUserInfo(ctx, &cogplugin.UserInfo{
				ID:          u.ID,
				Username:    u.Username,
				DisplayName: u.DisplayName,
				IsAdmin:     u.IsAdmin,
			})
		}
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

// shellMiddleware wraps a fragment handler with the full HTML shell.
// It builds PageData from the request context, captures handler output,
// and renders ui.Shell around it. Plugin context is injected by RegisterPage.
// HTMX partial requests (HX-Request: true) skip the shell entirely.
func (pr *pluginRegistrar) shellMiddleware(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HTMX partials: serve the fragment directly, no shell.
		if r.Header.Get("HX-Request") == "true" {
			handler.ServeHTTP(w, r)
			return
		}

		u := auth.UserFromContext(r.Context())
		csrf := cogplugin.CSRFTokenFromContext(r.Context())

		// Capture the fragment handler's output.
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)

		// If handler issued a redirect or error, pass through directly.
		if rec.Code >= 300 {
			maps.Copy(w.Header(), rec.Header())
			w.WriteHeader(rec.Code)
			w.Write(rec.Body.Bytes())
			return
		}

		// Build nav items with active state.
		var nav []ui.NavItem
		for _, item := range pr.registry.NavItems() {
			active := r.URL.Path == item.URL || (item.URL != "/" && len(r.URL.Path) >= len(item.URL) && r.URL.Path[:len(item.URL)] == item.URL)
			nav = append(nav, ui.NavItem{
				Label:  item.Label,
				URL:    item.URL,
				Active: active,
				Icon:   item.Icon,
			})
		}

		// Read page title from handler header, fallback to "Cog".
		title := rec.Header().Get("X-Page-Title")
		if title == "" {
			title = "Cog"
		}

		var favicon string
		for _, item := range nav {
			if item.Active {
				favicon = ui.IconToFavicon(item.Icon)
				break
			}
		}

		pd := ui.PageData{
			Title:       title,
			User:        u,
			CSRFToken:   csrf,
			Nav:         nav,
			RequestPath: r.URL.Path,
			IsAdmin:     u != nil && u.IsAdmin,
			PluginID:    pr.pluginID,
			Favicon:     favicon,
		}

		// Copy any content headers set by the fragment (except those we override).
		for k, v := range rec.Header() {
			if k == "X-Page-Title" || k == "Content-Length" || k == "Content-Type" {
				continue
			}
			w.Header()[k] = v
		}

		ui.Shell(pd, templ.Raw(rec.Body.String())).Render(r.Context(), w)
	})
}

// adminSettingsShellMiddleware wraps a fragment handler with the HTML shell and admin sidebar.
// Plugin settings items appear below a divider after the built-in admin items.
func (pr *pluginRegistrar) adminSettingsShellMiddleware(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		csrf := cogplugin.CSRFTokenFromContext(r.Context())

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)

		if rec.Code >= 300 {
			maps.Copy(w.Header(), rec.Header())
			w.WriteHeader(rec.Code)
			w.Write(rec.Body.Bytes())
			return
		}

		var nav []ui.NavItem
		for _, item := range pr.registry.NavItems() {
			active := r.URL.Path == item.URL || (item.URL != "/" && len(r.URL.Path) >= len(item.URL) && r.URL.Path[:len(item.URL)] == item.URL)
			nav = append(nav, ui.NavItem{
				Label:  item.Label,
				URL:    item.URL,
				Active: active,
				Icon:   item.Icon,
			})
		}

		title := rec.Header().Get("X-Page-Title")
		if title == "" {
			title = "Cog"
		}

		// Mark plugin settings nav items active based on current URL.
		pluginItems := pr.registry.SettingsItems()
		for i := range pluginItems {
			if pluginItems[i].URL == r.URL.Path {
				pluginItems[i].Active = true
			}
		}

		pd := ui.PageData{
			Title:             title,
			User:              u,
			CSRFToken:         csrf,
			Nav:               nav,
			RequestPath:       r.URL.Path,
			IsAdmin:           u != nil && u.IsAdmin,
			PluginSettingsNav: pluginItems,
		}

		for k, v := range rec.Header() {
			if k == "X-Page-Title" || k == "Content-Length" || k == "Content-Type" {
				continue
			}
			w.Header()[k] = v
		}

		fragment := templ.Raw(rec.Body.String())
		ui.AdminSidebarPage(pd, "").Render(templ.WithChildren(r.Context(), fragment), w)
	})
}

func (pr *pluginRegistrar) RegisterAPI(path string, handler http.Handler) {
	pattern := fmt.Sprintf("GET /apps/%s/%s", pr.pluginID, path)
	pr.mux.Handle(pattern, pr.wrap(auth.RequireAuth(pr.injectPluginContext(handler))))
}

func (pr *pluginRegistrar) RegisterAction(method, path string, handler http.Handler) {
	pattern := fmt.Sprintf("%s /apps/%s/%s", method, pr.pluginID, path)
	wrapped := auth.RequireAuthWeb(pr.injectPluginContext(auth.RequireCSRF(pr.csrfSecret, handler)))
	pr.mux.Handle(pattern, pr.wrap(wrapped))
}

func (pr *pluginRegistrar) RegisterRoute(pattern string, handler http.Handler) {
	pr.mux.Handle(pattern, pr.wrap(handler))
}

func (pr *pluginRegistrar) PluginByID(id string) (Plugin, bool) {
	return pr.registry.Plugin(id)
}

func (pr *pluginRegistrar) Plugins() iter.Seq[Plugin] {
	return pr.registry.Plugins()
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
	// Sanitize plugin-supplied SVG icon once at registration so
	// templ.Raw(item.Icon) downstream can't smuggle <script> or event
	// handlers from a hostile external plugin (audit security.md #50).
	item.Icon = ui.SanitizeIcon(item.Icon)
	pr.navItems = append(pr.navItems, item)
}

func (pr *pluginRegistrar) RegisterWidget(w Widget) {
	pr.widgets = append(pr.widgets, w)
}

func (pr *pluginRegistrar) RegisterSearchSource(def SearchSourceDef) {
	pr.searchSources = append(pr.searchSources, def)
}

func (pr *pluginRegistrar) RegisterCommand(def CommandDef) {
	pr.commands = append(pr.commands, def)
}

func (pr *pluginRegistrar) RegisterAdminCommand(def CommandDef) {
	pr.adminCommands = append(pr.adminCommands, def)
}

func (pr *pluginRegistrar) RootDB() (*sql.DB, error) {
	return pr.registry.RootDB(pr.pluginID)
}

func (pr *pluginRegistrar) RegisterDAVHandler(prefix string, handler http.Handler) {
	pattern := "/remote.php/dav/" + prefix + "/"
	wrapped := auth.RequireAuth(pr.wrap(handler))
	pr.mux.Handle(pattern, wrapped)
}

func (pr *pluginRegistrar) RegisterSettingsPage(page SettingsPage) {
	pr.settingsPages = append(pr.settingsPages, page)
	var basePath string
	if page.Slug == "" {
		basePath = fmt.Sprintf("/admin/plugins/%s", pr.pluginID)
	} else {
		basePath = fmt.Sprintf("/admin/plugins/%s/%s", pr.pluginID, page.Slug)
	}
	if page.Handler != nil {
		getHandler := auth.RequireAuthWeb(auth.RequireAdmin(
			pr.injectPluginContext(pr.adminSettingsShellMiddleware(page.Handler))))
		pr.mux.Handle("GET "+basePath, pr.wrap(getHandler))
	}
	if page.Action != nil {
		postHandler := auth.RequireAuthWeb(auth.RequireAdmin(
			pr.injectPluginContext(auth.RequireCSRF(pr.csrfSecret, page.Action))))
		pr.mux.Handle("POST "+basePath, pr.wrap(postHandler))
	}
}

// AdminCommands returns admin-only CLI commands, keyed by plugin ID.
func (r *Registry) AdminCommands() map[string][]CommandDef {
	cmds := make(map[string][]CommandDef)
	for id, reg := range r.registrars {
		if len(reg.adminCommands) > 0 {
			cmds[id] = reg.adminCommands
		}
	}
	return cmds
}

// UserCommands returns user-facing shell commands, keyed by plugin ID.
func (r *Registry) UserCommands() map[string][]CommandDef {
	cmds := make(map[string][]CommandDef)
	for id, reg := range r.registrars {
		if len(reg.commands) > 0 {
			cmds[id] = reg.commands
		}
	}
	return cmds
}

// DoctorChecks runs doctor checks on all plugins that implement the Doctor interface.
func (r *Registry) DoctorChecks(ctx context.Context) map[string][]CheckResult {
	results := make(map[string][]CheckResult)
	for _, id := range r.order {
		if d, ok := r.plugins[id].(Doctor); ok {
			results[id] = d.DoctorChecks(ctx)
		}
	}
	return results
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
	r.mu.Lock()
	enabled, ok := r.enabled[pluginID]
	r.mu.Unlock()
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
	r.mu.Lock()
	r.enabled[pluginID] = enabled
	r.mu.Unlock()
	if r.sysDB != nil {
		if _, err := r.sysDB.Exec(
			`INSERT INTO plugin_settings (plugin_id, enabled) VALUES (?, ?) ON CONFLICT(plugin_id) DO UPDATE SET enabled = ?`,
			pluginID, boolToInt(enabled), boolToInt(enabled),
		); err != nil {
			r.log.Error("persist plugin enabled state", "plugin", pluginID, "enabled", enabled, "err", err)
		}
	}
}

// PluginIDs returns all registered plugin IDs in registration order,
// followed by discovered-but-disabled process plugin IDs.
func (r *Registry) PluginIDs() []string {
	ids := append([]string(nil), r.order...)
	for id := range r.discovered {
		// Only add if not already in order (disabled plugins aren't loaded).
		if _, loaded := r.plugins[id]; !loaded {
			ids = append(ids, id)
		}
	}
	return ids
}

// IsDiscovered returns true if the plugin was discovered as a process plugin
// from PATH (whether loaded or not).
func (r *Registry) IsDiscovered(pluginID string) bool {
	if _, ok := r.discovered[pluginID]; ok {
		return true
	}
	// Also check loaded plugins that came from discovery.
	// We track this via the discovered map at discovery time.
	return false
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
	r.mu.Lock()
	defer r.mu.Unlock()
	for rows.Next() {
		var id string
		var enabled int
		if rows.Scan(&id, &enabled) == nil {
			r.enabled[id] = enabled != 0
		}
	}
}

// DiscoverProcessPlugins scans PATH for executables matching "cog-plugin-*",
// loads enabled ones via LoadExternalTrusted, and records disabled ones for
// display in the admin UI.
func (r *Registry) DiscoverProcessPlugins() {
	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		return
	}

	seen := make(map[string]bool) // avoid duplicates from multiple PATH entries
	for _, dir := range filepath.SplitList(pathEnv) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasPrefix(name, "cog-plugin-") {
				continue
			}
			pluginID := strings.TrimPrefix(name, "cog-plugin-")
			if pluginID == "" || seen[pluginID] {
				continue
			}
			seen[pluginID] = true

			// Skip if a built-in plugin already has this ID.
			if _, exists := r.plugins[pluginID]; exists {
				continue
			}

			binaryPath := filepath.Join(dir, name)

			// Check if the binary is executable.
			info, err := entry.Info()
			if err != nil || info.Mode()&0o111 == 0 {
				continue
			}

			if !r.IsEnabled(pluginID) {
				r.discovered[pluginID] = binaryPath
				r.log.Info("discovered process plugin (disabled)", "plugin", pluginID, "path", binaryPath)
				continue
			}

			r.log.Info("loading process plugin", "plugin", pluginID, "path", binaryPath)
			if isTrustedProcessPlugin(pluginID) {
				r.log.Warn("loading process plugin as TRUSTED (host secrets exposed)",
					"plugin", pluginID, "path", binaryPath)
				err = r.LoadExternalTrusted(binaryPath)
			} else {
				err = r.LoadExternal(binaryPath)
			}
			if err != nil {
				r.log.Error("failed to load process plugin", "plugin", pluginID, "path", binaryPath, "err", err)
				continue
			}
			// Track that this loaded plugin came from discovery.
			r.discovered[pluginID] = binaryPath
		}
	}
}

// trustedProcessPlugins is the parsed COG_TRUSTED_PROCESS_PLUGINS allowlist.
// Set via SetTrustedProcessPlugins at startup.
var trustedProcessPlugins map[string]bool

// SetTrustedProcessPlugins configures which discovered process plugin IDs are
// loaded as trusted (handed the host CSRF secret and direct DB access).
// Comma-separated list of plugin IDs.
func SetTrustedProcessPlugins(spec string) {
	out := make(map[string]bool)
	for raw := range strings.SplitSeq(spec, ",") {
		id := strings.TrimSpace(raw)
		if id != "" {
			out[id] = true
		}
	}
	trustedProcessPlugins = out
}

func isTrustedProcessPlugin(id string) bool {
	return trustedProcessPlugins[id]
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
