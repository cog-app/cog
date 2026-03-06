// Package plugin provides the host-side plugin registry and privileged types.
// Public plugin interfaces live in git.duckfam.us/jj-software/cog/plugin;
// this package re-exports them as type aliases so internal callers need only
// one import.
package plugin

import (
	"net/http"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
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

// Re-export permission constants (cannot alias constants).
const (
	PermRead     = cogplugin.PermRead
	PermWrite    = cogplugin.PermWrite
	PermReshare  = cogplugin.PermReshare
	PermFreeBusy = cogplugin.PermFreeBusy
	PermSchedule = cogplugin.PermSchedule
)

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
}
