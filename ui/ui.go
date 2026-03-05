// Package ui provides shared layout, components, and static assets for the Cog web interface.
package ui

import (
	"strings"

	"git.duckfam.us/jj-software/cog/internal/user"
)

//go:generate go tool templ generate -log-level warn

// PageData carries data needed by the layout template.
type PageData struct {
	Title       string
	User        *user.User
	Nav         []NavItem
	CSRFToken   string
	RequestPath string // current request path for active-state highlighting
	IsAdmin     bool
}

// NavItem is a navigation link in the navbar.
type NavItem struct {
	Label  string
	URL    string
	Active bool
	Icon   string
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
	Label  string
	URL    string
	Active bool
}
