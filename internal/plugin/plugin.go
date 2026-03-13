// Package plugin provides the host-side plugin registry and privileged types.
// Public plugin interfaces live in git.duckfam.us/jj-software/cog/plugin;
// this package re-exports them as type aliases so internal callers need only
// one import.
package plugin

import (
	"context"
	"database/sql"
	"io/fs"
	"net/http"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"
)

// Type aliases — internal code can keep using plugin.Plugin, plugin.Registrar, etc.
type Plugin = cogplugin.Plugin
type PluginContext = cogplugin.PluginContext
type Registrar = cogplugin.Registrar
type NavItem = cogplugin.NavItem
type Widget = cogplugin.Widget
type HookCallback = cogplugin.HookCallback
type ResourceDef = cogplugin.ResourceDef
type ResourceHandler = cogplugin.ResourceHandler
type ResourceHandlerFunc = cogplugin.ResourceHandlerFunc
type UserContext = cogplugin.UserContext
type SubjectRef = cogplugin.SubjectRef
type ResourceRef = cogplugin.ResourceRef
type SearchSourceDef = cogplugin.SearchSourceDef
type PermissionLevel = cogplugin.PermissionLevel
type ShareStore = cogplugin.ShareStore
type ShareRecord = cogplugin.ShareRecord
type AccessDecision = cogplugin.AccessDecision
type EmailSender = cogplugin.EmailSender
type EmailAttachment = cogplugin.EmailAttachment
type UserLookup = cogplugin.UserLookup
type UserInfo = cogplugin.UserInfo
type GroupStore = cogplugin.GroupStore
type GroupInfo = cogplugin.GroupInfo
type FilesAccess = cogplugin.FilesAccess
type FileInfo = cogplugin.FileInfo
type VolumeAccess = cogplugin.VolumeAccess
type VolumeInfo = cogplugin.VolumeInfo
type CommandDef = cogplugin.CommandDef
type SettingsPage = cogplugin.SettingsPage
type VFS = vfs.FS

// Re-export permission constants (cannot alias constants).
const (
	PermRead     = cogplugin.PermRead
	PermWrite    = cogplugin.PermWrite
	PermReshare  = cogplugin.PermReshare
	PermFreeBusy = cogplugin.PermFreeBusy
	PermSchedule = cogplugin.PermSchedule
)

// Re-export functions from the public plugin package.
var (
	Migrate              = cogplugin.Migrate
	CSRFTokenFromContext = cogplugin.CSRFTokenFromContext
	UserFromContext      = cogplugin.UserFromContext
	WithCSRFToken        = cogplugin.WithCSRFToken
	WithUserInfo         = cogplugin.WithUserInfo
	WithPluginDB         = cogplugin.WithPluginDB
	CSRFToken            = cogplugin.CSRFToken
	UserDB               = cogplugin.UserDB
	SetTitle             = cogplugin.SetTitle
	Username             = cogplugin.Username
	WriteJSON            = cogplugin.WriteJSON
	PathID               = cogplugin.PathID
	PathIDSuffix         = cogplugin.PathIDSuffix
)

// Ensure function signatures stay in sync at compile time.
var _ func(*sql.DB, fs.FS) error = Migrate
var _ func(context.Context) string = CSRFTokenFromContext
var _ func(context.Context) *UserInfo = UserFromContext
var _ func(context.Context, string) context.Context = WithCSRFToken
var _ func(context.Context, *UserInfo) context.Context = WithUserInfo

// protectedPlugin is unexported — only embeddable from within internal/plugin.
type protectedPlugin interface {
	protected()
}

// Protected marks a plugin as always-on. Embed in privileged plugin structs
// so the registry auto-detects them.
type Protected struct{}

func (Protected) protected() {}

// IsProtected reports whether p embeds the Protected marker.
func IsProtected(p cogplugin.Plugin) bool {
	_, ok := p.(protectedPlugin)
	return ok
}

// PrivilegedContext extends PluginContext with methods only available to
// first-party (in-process) plugins. Plugins type-assert to get access.
type PrivilegedContext interface {
	cogplugin.PluginContext
	// RegisterRoute mounts a handler at an arbitrary pattern (Go 1.22 format).
	// The handler is wrapped with auth (401) and plugin-enabled checks.
	RegisterRoute(pattern string, handler http.Handler)
	// PluginByID returns a registered plugin by its ID.
	// Used by internal plugins that need direct access to other plugins.
	PluginByID(id string) (cogplugin.Plugin, bool)
	// RegisterAdminCommand declares an admin-only CLI command for the plugin.
	RegisterAdminCommand(def cogplugin.CommandDef)
}
