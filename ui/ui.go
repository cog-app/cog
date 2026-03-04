// Package ui provides shared layout, components, and static assets for the Cog web interface.
package ui

import "git.duckfam.us/jj-software/cog/internal/user"

//go:generate go tool templ generate

// PageData carries data needed by the layout template.
type PageData struct {
	Title     string
	User      *user.User
	Nav       []NavItem
	CSRFToken string
}

// NavItem is a navigation link in the navbar.
type NavItem struct {
	Label  string
	URL    string
	Active bool
	Icon   string
}
