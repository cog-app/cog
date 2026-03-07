package main

import (
	"git.duckfam.us/jj-software/cog"
	"git.duckfam.us/jj-software/cog/plugin/bookmarks"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
	"git.duckfam.us/jj-software/cog/plugin/tasks"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
	"git.duckfam.us/jj-software/cog/plugin/dashboard"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
	"git.duckfam.us/jj-software/cog/plugin/notes"
	"git.duckfam.us/jj-software/cog/plugin/rss"
)

func main() {
	cog.Main(
		dashboard.New(),
		files.New(),
		calendar.New(),
		contacts.New(),
		notes.New(),
		rss.New(),
		gallery.New(),
		bookmarks.New(),
		tasks.New(),
	)
}
