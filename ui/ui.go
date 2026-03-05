// Package ui provides shared layout, components, and static assets for the Cog web interface.
package ui

import "git.duckfam.us/jj-software/cog/internal/user"

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

// SidebarItem is a navigation link in a page sidebar.
type SidebarItem struct {
	Label  string
	URL    string
	Active bool
}
