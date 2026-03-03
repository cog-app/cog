// Package plugin defines the interfaces and types for the Cog plugin system.
package plugin

import (
	"context"
	"database/sql"
	"io/fs"
	"net/http"
)

// Permission bitmask values for share records.
const (
	PermRead    uint32 = 1 << iota // 1
	PermWrite                      // 2
	PermReshare                    // 4
)

// Plugin is the core interface that all plugins must implement.
type Plugin interface {
	// ID returns the unique identifier for this plugin (e.g. "files", "calendar").
	ID() string
	// Register is called during startup to let the plugin declare routes,
	// resources, and hooks via the registrar.
	Register(registrar Registrar) error
	// Start is called after registration to begin any background work.
	Start(ctx context.Context) error
	// Stop is called during shutdown for graceful cleanup.
	Stop() error
}

// Registrar is the host-provided interface that plugins use during registration
// to declare their routes, resources, and hooks.
type Registrar interface {
	// RegisterAPI mounts a handler at /api/{plugin_id}/{path}.
	RegisterAPI(path string, handler http.Handler)
	// RegisterPage mounts a handler at /apps/{plugin_id}/{path}.
	RegisterPage(path string, handler http.Handler)
	// RegisterAction mounts a state-changing handler (POST/PUT/DELETE).
	RegisterAction(method, path string, handler http.Handler)
	// RegisterResource declares a shareable resource type and its handler.
	RegisterResource(def ResourceDef)
	// RegisterHook subscribes to a named hook event.
	RegisterHook(hookName string, callback HookCallback)
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
