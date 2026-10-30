// Package ui provides shared layout, components, and static assets for the Cog web interface.
package ui

import (
	"encoding/base64"
	"strings"
	"sync"

	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin"
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
	Flash             *plugin.Flash // message carried by a redirect; rendered above the page body
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
	Section  string // where the page lives, shown beside it: "Settings", "Admin", "App"
	Keywords string // extra search terms (space-separated)
	Admin    bool   // only shown to admins
}

// AllPages is the static list of navigable core pages. Apps are added from
// the navbar at search time, so only installed and enabled apps are found.
var AllPages = []PageLink{
	{Label: "Dashboard", URL: "/", Section: "Home", Keywords: "home start widgets"},
	{Label: "Profile", URL: "/settings/profile", Section: "Settings", Keywords: "account name email avatar"},
	{Label: "Security", URL: "/settings/security", Section: "Settings", Keywords: "password app passwords two-factor mfa totp passkeys"},
	{Label: "Storage", URL: "/settings/storage", Section: "Settings", Keywords: "mounts volumes disk quota"},
	{Label: "Groups", URL: "/settings/groups", Section: "Settings", Keywords: "teams members invitations"},
	{Label: "Import and export", URL: "/settings/data", Section: "Settings", Keywords: "backup download upload ics vcf"},
	{Label: "General", URL: "/admin/settings", Section: "Admin", Keywords: "administration configuration registration", Admin: true},
	{Label: "Email", URL: "/admin/email", Section: "Admin", Keywords: "smtp templates mail", Admin: true},
	{Label: "Users", URL: "/admin/users", Section: "Admin", Keywords: "accounts manage", Admin: true},
	{Label: "Groups", URL: "/admin/groups", Section: "Admin", Keywords: "teams manage", Admin: true},
	{Label: "Volumes", URL: "/admin/volumes", Section: "Admin", Keywords: "storage disks", Admin: true},
	{Label: "Plugins", URL: "/admin/plugins", Section: "Admin", Keywords: "apps extensions", Admin: true},
	{Label: "Logs", URL: "/admin/logs", Section: "Admin", Keywords: "server log errors", Admin: true},
}

// MatchPages returns the pages whose label, section or keywords contain a
// word starting with the query (case-insensitive): "se" finds every Settings
// page and Security, "adm" every Admin page. apps are the navbar's app links.
func MatchPages(query string, isAdmin bool, apps []NavItem) []PageLink {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	pages := make([]PageLink, 0, len(apps)+len(AllPages))
	for _, a := range apps {
		pages = append(pages, PageLink{Label: a.Label, URL: a.URL, Section: "App"})
	}
	pages = append(pages, AllPages...)

	var matches []PageLink
	for _, p := range pages {
		if p.Admin && !isAdmin {
			continue
		}
		if wordPrefix(p.Label+" "+p.Section+" "+p.Keywords, q) {
			matches = append(matches, p)
		}
	}
	return matches
}

// wordPrefix reports whether some word of text starts with q, or q spans
// words ("app pass").
func wordPrefix(text, q string) bool {
	t := strings.ToLower(text)
	if strings.HasPrefix(t, q) || strings.Contains(t, " "+q) {
		return true
	}
	return false
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
	LinkPath    string // where a public link is served: /s/<token>, or a calendar's feed
}

// PermissionLevelChoice is a named permission level for the share form dropdown.
type PermissionLevelChoice struct {
	Label       string
	Permissions uint32
}
