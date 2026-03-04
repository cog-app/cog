// Package plugin provides the host-side plugin registry and privileged types.
// Public plugin interfaces live in git.duckfam.us/jj-software/cog/plugin;
// this package re-exports them as type aliases so internal callers need only
// one import.
package plugin

import cogplugin "git.duckfam.us/jj-software/cog/plugin"

// Type aliases — internal code can keep using plugin.Plugin, plugin.Registrar, etc.
type Plugin = cogplugin.Plugin
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

// Re-export permission constants (cannot alias constants).
const (
	PermRead    = cogplugin.PermRead
	PermWrite   = cogplugin.PermWrite
	PermReshare = cogplugin.PermReshare
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
