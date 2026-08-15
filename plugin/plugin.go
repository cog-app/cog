// Package plugin defines the public interfaces and types for the Cog plugin system.
// External plugin authors import this package to implement plugins.
package plugin

import (
	"context"
	"database/sql"
	"io"
	"io/fs"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/query"
)

// Permission bitmask values for share records.
const (
	PermRead     uint32 = 1 << iota // 1
	PermWrite                       // 2
	PermReshare                     // 4
	PermFreeBusy                    // 8
	PermSchedule                    // 16
)

// PermissionLevel is a named permission preset that plugins define
// for their shareable resources.
type PermissionLevel struct {
	Label       string // "Read-only", "Read/Write", "Free/Busy"
	Permissions uint32 // bitmask
}

// Plugin is the core interface that all plugins must implement.
type Plugin interface {
	// ID returns the unique identifier for this plugin (e.g. "files", "calendar").
	ID() string
	// Register is called during startup to let the plugin declare routes,
	// resources, and hooks via the plugin context.
	Register(ctx PluginContext) error
	// Start is called after registration to begin any background work.
	Start(ctx context.Context) error
	// Stop is called during shutdown for graceful cleanup.
	Stop() error
}

// Schemaed is an optional interface. When a plugin implements it, the host
// runs the plugin's migrations against every per-user database it opens,
// before handing that database to anything.
//
// Without it, migrating is the plugin's own job, and every path that reaches a
// user database — page handlers via UserDB, widgets, DAV endpoints — has to
// remember to do it first. Missing one only shows up when that path happens to
// run before the others, which is a race, not a bug you can see in review.
type Schemaed interface {
	// SchemaFS returns a filesystem holding sql/schema.sql and any
	// sql/migration*.sql files.
	SchemaFS() fs.FS
}

// Registrar is the host-provided interface that plugins use during registration
// to declare their routes, resources, and hooks.
type Registrar interface {
	// RegisterPage mounts a GET handler at /apps/{plugin_id}/{path}.
	// The handler should render only the page content fragment — the HTML shell
	// (navbar, head, scripts) is automatically wrapped by middleware.
	// Use plugin.CSRFTokenFromContext(r.Context()) for CSRF tokens.
	RegisterPage(path string, handler http.Handler)
	// RegisterAPI mounts a GET handler at /apps/{plugin_id}/{path}.
	// The handler is wrapped with auth (401 on failure) and plugin-enabled checks.
	// Use this for JSON endpoints and HTMX partials fetched from authenticated pages.
	RegisterAPI(path string, handler http.Handler)
	// RegisterAction mounts a state-changing handler (POST/PUT/DELETE) at /apps/{plugin_id}/{path}.
	// The handler is wrapped with auth, CSRF, and plugin-enabled checks.
	RegisterAction(method, path string, handler http.Handler)
	// RegisterResource declares a shareable resource type and its handler.
	RegisterResource(def ResourceDef)
	// RegisterHook subscribes to a named hook event.
	RegisterHook(hookName string, callback HookCallback)
	// RegisterNavItem adds a navigation entry to the app navbar.
	RegisterNavItem(item NavItem)
	// RegisterWidget registers a dashboard widget.
	RegisterWidget(w Widget)
	// RegisterSearchSource declares a search source for full-text indexing.
	RegisterSearchSource(def SearchSourceDef)
	// RegisterDAVHandler mounts a handler under /remote.php/dav/{prefix}/.
	// The handler receives authenticated requests with full path intact.
	RegisterDAVHandler(prefix string, handler http.Handler)
	// RegisterSettingsPage adds a settings page for this plugin.
	RegisterSettingsPage(page SettingsPage)
	// RegisterCommand declares a user-facing CLI command available in the shell.
	RegisterCommand(def CommandDef)
}

// SettingsPage describes a settings page registered by a plugin.
// The Handler renders a content fragment (GET); Action handles form submissions (POST).
// Settings pages are admin-only and wrapped with the HTML shell automatically.
type SettingsPage struct {
	Slug    string       // URL path segment (e.g. "recognition"); empty for the plugin's default page
	Name    string       // display name in sidebar (e.g. "Recognition"); empty uses plugin display name
	Handler http.Handler // GET — renders page content fragment
	Action  http.Handler // POST — handles form submission (optional)
}

// NavItem is a navigation entry registered by a plugin for the app navbar.
type NavItem struct {
	Label string // display label, e.g. "Files"
	URL   string // link target, e.g. "/apps/files"
	Icon  string // optional inline SVG markup (should use class="nav-icon")
}

// Widget is a dashboard widget registered by a plugin.
type Widget struct {
	Title    string
	Priority int // lower = rendered first
	Render   func(ctx context.Context, w io.Writer, userID string) error
}

// HookCallback is a function invoked when a hook event fires.
type HookCallback func(ctx context.Context, data any)

// ResourceDef describes a shareable resource type within a plugin.
// Cog uses this metadata to manage the global shares table.
type ResourceDef struct {
	// Type is the resource type string (e.g. "file", "directory").
	// Combined with plugin ID it forms "files:file", "files:directory", etc.
	Type string
	// Table is the SQLite table where the resource is stored.
	Table string
	// IDColumn is the column that uniquely identifies the resource.
	IDColumn string
	// ParentColumn is the optional column for hierarchical sharing.
	ParentColumn string
	// Handler serves requests for individual resources.
	Handler ResourceHandler
	// PermissionLevels defines the named permission presets available for sharing.
	// If empty, defaults to Read-only and Read/Write.
	PermissionLevels []PermissionLevel
}

// ResourceHandler serves requests for a specific resource instance.
type ResourceHandler interface {
	ServeResource(w http.ResponseWriter, r *http.Request, resourceID string)
}

// ResourceHandlerFunc adapts a function to the ResourceHandler interface.
type ResourceHandlerFunc func(w http.ResponseWriter, r *http.Request, resourceID string)

func (f ResourceHandlerFunc) ServeResource(w http.ResponseWriter, r *http.Request, resourceID string) {
	f(w, r, resourceID)
}

// UserContext provides per-user database and filesystem access to plugins.
// A fresh UserContext is created for each authenticated request.
type UserContext struct {
	UserID   string
	Username string
	PluginDB *sql.DB
	FilesDir fs.FS
}

// SubjectRef identifies a user or group that can receive a share.
type SubjectRef struct {
	Type string // "USER" or "GROUP"
	ID   string // UUID
}

// ResourceRef is a stable pointer to a shareable object.
type ResourceRef struct {
	Type string // e.g. "files:file", "calendar:event"
	ID   string // stable plugin-defined identifier
}

// CommandDef defines a CLI command registered by a plugin.
// Leaf commands have Run set; group commands have Sub set instead.
type CommandDef struct {
	Name  string   // e.g. "scan"
	Short string   // one-line description
	Args  []string // positional arg names for usage (e.g. ["username"])
	Run   func(ctx context.Context, args []string) error
	Sub   []CommandDef // sub-commands (mutually exclusive with Run)
}

// CheckStatus indicates the outcome of a doctor check.
type CheckStatus int

const (
	CheckOK    CheckStatus = iota // everything is fine
	CheckWarn                     // feature degraded but not broken
	CheckError                    // something is broken
)

// CheckResult is a single diagnostic check performed by a plugin.
type CheckResult struct {
	Name    string
	Status  CheckStatus
	Message string // empty for OK
}

// Doctor is an optional interface plugins implement to contribute
// dependency checks to the `cog doctor` command.
type Doctor interface {
	DoctorChecks(ctx context.Context) []CheckResult
}

// FolderIconProvider is an optional interface that plugins implement to provide
// custom folder icons in the file browser. The files plugin discovers providers
// among registered plugins and queries them at render time per user.
type FolderIconProvider interface {
	// FolderIcon returns inline SVG markup for a folder, or ok=false to use the default.
	// folderPath is relative to the user's root (e.g., "Photos", "Notes").
	FolderIcon(username, folderPath string) (icon string, ok bool)
}

// ContentFilter is an optional interface that plugins implement to filter
// text and audio content. Consuming plugins (RSS, media) discover filters
// among registered plugins and apply them at render/fetch time.
type ContentFilter interface {
	// FilterText replaces filtered content in plain text. Returns modified text.
	FilterText(username, text string) string
	// FilterHTML replaces filtered content in HTML text nodes. Returns modified HTML.
	FilterHTML(username, html string) string
	// AudioRanges returns time ranges that should be cleaned in audio/video,
	// given timed caption segments. Each range includes replacement text for TTS.
	AudioRanges(username string, captions []CaptionSegment) []AudioRange
	// CleanAudioFile produces a cleaned version of an audio/video file by
	// replacing profane segments with silence or TTS. Returns the output path.
	// inputURL can be a local path or remote URL. outputPath is where to write.
	CleanAudioFile(ctx context.Context, username, inputURL, outputPath string, captions []CaptionSegment) error
}

// CaptionSegment is a timed text segment from a caption/subtitle file.
type CaptionSegment struct {
	Start, End float64 // seconds
	Text       string
}

// AudioRange is a time range in audio/video that should be cleaned.
type AudioRange struct {
	Start, End      float64 // seconds
	ReplacementText string  // clean text for TTS, empty = mute
}

// ActivityLogger allows plugins to write entries to the centralized activity log.
type ActivityLogger interface {
	// Log records an activity for the given user.
	// userID is the affected user, actorID is who performed the action.
	// app identifies the plugin (e.g., "files"), typ is the event type (e.g., "file_created").
	Log(userID, actorID, app, typ, subject, objectType, objectID, objectName string) error
}

// SearchSourceDef describes how a plugin's data should be indexed for search.
type SearchSourceDef struct {
	// PrimaryTable is the SQLite table name watched via update hook.
	PrimaryTable string
	// EnrichSQL is a SELECT query that must return rowid as the first column,
	// followed by columns in display order: name, subtext, then content columns.
	// Must contain "WHERE rowid IN (?)" as a placeholder.
	// The first non-rowid column becomes "name", the second "subtext",
	// and remaining columns are concatenated as "content".
	EnrichSQL string
	// ContentFunc optionally returns text content to index for a row.
	// Receives the user's files directory and all SQL column values (by column name).
	// Return "" to skip content indexing for this row. Internal plugins only.
	ContentFunc func(filesDir string, cols map[string]string) string
	// Schema, when set together with Enricher, opts into the schema-driven
	// search path. The host builds the bleve mapping from this Schema and
	// calls Enricher.EnrichSchema instead of running EnrichSQL.
	Schema *query.Schema
	// Enricher supplies field values for the documents this source owns at
	// index time. Required when Schema is set; ignored otherwise.
	Enricher SchemaEnricher
}
