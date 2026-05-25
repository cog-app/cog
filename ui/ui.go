// Package ui provides shared layout, components, and static assets for the Cog web interface.
package ui

import (
	"encoding/base64"
	"strings"
	"sync"

	"git.duckfam.us/jj-software/cog/internal/user"
	"github.com/microcosm-cc/bluemonday"
)

// svgPolicy sanitizes plugin-supplied SVG icons before they're rendered as
// raw HTML in the nav (audit security.md #50). Plugins are typically trusted
// but external gRPC plugins should not be able to inject <script> or event
// handlers through their declared icon.
var svgPolicy = sync.OnceValue(func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("svg", "g", "path", "circle", "rect", "line", "polyline",
		"polygon", "ellipse", "defs", "use", "title", "desc")
	p.AllowAttrs("xmlns", "viewBox", "width", "height", "fill", "stroke",
		"stroke-width", "stroke-linecap", "stroke-linejoin", "stroke-dasharray",
		"stroke-miterlimit", "class", "transform", "d", "cx", "cy", "r", "rx",
		"ry", "x", "y", "x1", "y1", "x2", "y2", "points", "id", "href",
		"opacity", "fill-rule", "clip-rule", "fill-opacity", "stroke-opacity").
		Globally()
	return p
})

// SanitizeIcon strips unsafe constructs (scripts, event handlers, foreign
// elements) from a plugin-supplied SVG icon string.
func SanitizeIcon(icon string) string {
	if icon == "" {
		return ""
	}
	return svgPolicy().Sanitize(icon)
}

// PageData carries data needed by the layout template.
type PageData struct {
	Title             string
	User              *user.User
	Nav               []NavItem
	CSRFToken         string
	RequestPath       string // current request path for active-state highlighting
	IsAdmin           bool
	PluginID          string        // owning plugin; rendered as data-plugin on <main> for web components
	PluginSettingsNav []SidebarItem // plugin-registered admin settings items (shown below divider)
	Favicon           string        // favicon URL; empty uses default /static/favicon.svg
}

// FaviconURL returns the favicon URL, defaulting to /static/favicon.svg.
func (pd PageData) FaviconURL() string {
	if pd.Favicon != "" {
		return pd.Favicon
	}
	return "/static/favicon.svg"
}

// NavItem is a navigation link in the navbar.
type NavItem struct {
	Label  string
	URL    string
	Active bool
	Icon   string
}

// IconToFavicon converts a nav icon SVG string into a data URI suitable for a favicon.
// It adds xmlns, fill="none", and stroke color so the icon renders correctly standalone.
func IconToFavicon(icon string) string {
	if icon == "" {
		return ""
	}
	svg := strings.ReplaceAll(icon, "currentColor", "#0070f3")
	svg = strings.Replace(svg, `class="nav-icon"`, "", 1)
	// Add attributes needed for standalone SVG rendering.
	attrs := ""
	if !strings.Contains(svg, "xmlns") {
		attrs += `xmlns="http://www.w3.org/2000/svg" `
	}
	if !strings.Contains(svg, "fill=") {
		attrs += `fill="none" `
	}
	if !strings.Contains(svg, "stroke=") {
		attrs += `stroke="#0070f3" `
	}
	if !strings.Contains(svg, "stroke-width=") {
		attrs += `stroke-width="2" stroke-linecap="round" stroke-linejoin="round" `
	}
	if attrs != "" {
		svg = strings.Replace(svg, "<svg ", "<svg "+attrs, 1)
	}
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg))
}

// PageLink is a page/app that can appear in search results.
type PageLink struct {
	Label    string
	URL      string
	Keywords string // extra search terms (space-separated)
	Admin    bool   // only shown to admins
}

// AllPages is the static list of navigable pages.
var AllPages = []PageLink{
	{Label: "Files", URL: "/apps/files/", Keywords: "documents uploads"},
	{Label: "Calendar", URL: "/apps/calendar/", Keywords: "events schedule caldav"},
	{Label: "Contacts", URL: "/apps/contacts/", Keywords: "address book carddav vcard people"},
	{Label: "News", URL: "/apps/rss/", Keywords: "rss atom feed reader articles"},
	{Label: "Profile", URL: "/settings/profile", Keywords: "account name email avatar"},
	{Label: "Security", URL: "/settings/security", Keywords: "password app passwords"},
	{Label: "Storage", URL: "/settings/storage", Keywords: "mounts volumes disk"},
	{Label: "Groups", URL: "/settings/groups", Keywords: "teams members"},
	{Label: "Admin", URL: "/admin/", Keywords: "administration dashboard", Admin: true},
	{Label: "Admin Users", URL: "/admin/users", Keywords: "accounts manage", Admin: true},
	{Label: "Admin Groups", URL: "/admin/groups", Keywords: "teams manage", Admin: true},
	{Label: "Admin Volumes", URL: "/admin/volumes", Keywords: "storage disks", Admin: true},
	{Label: "Admin Settings", URL: "/admin/settings", Keywords: "configuration", Admin: true},
	{Label: "Admin Plugins", URL: "/admin/plugins", Keywords: "extensions", Admin: true},
}

// MatchPages returns pages whose label or keywords match the query (case-insensitive).
func MatchPages(query string, isAdmin bool) []PageLink {
	q := strings.ToLower(query)
	var matches []PageLink
	for _, p := range AllPages {
		if p.Admin && !isAdmin {
			continue
		}
		if strings.Contains(strings.ToLower(p.Label), q) ||
			strings.Contains(p.Keywords, q) {
			matches = append(matches, p)
		}
	}
	return matches
}

// SidebarItem is a navigation link in a page sidebar.
type SidebarItem struct {
	Label    string
	URL      string
	Active   bool
	Children []SidebarItem
}

// ShareDisplayItem is a share record enriched with display info for the share panel.
type ShareDisplayItem struct {
	ID          string
	SubjectType string // "USER", "GROUP", "PUBLIC"
	SubjectName string
	PermLabel   string
	Token       string // for public links
}

// PermissionLevelChoice is a named permission level for the share form dropdown.
type PermissionLevelChoice struct {
	Label       string
	Permissions uint32
}
