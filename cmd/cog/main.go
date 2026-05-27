package main

import (
	"git.duckfam.us/jj-software/cog"
	"git.duckfam.us/jj-software/cog/plugin/bookmarks"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
	"git.duckfam.us/jj-software/cog/plugin/dashboard"
	"git.duckfam.us/jj-software/cog/plugin/feed/orchestrator"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
	"git.duckfam.us/jj-software/cog/plugin/notes"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
	"git.duckfam.us/jj-software/cog/plugin/tasks"
)

func main() {
	cog.Main(
		dashboard.New(),
		files.New(),
		calendar.New(),
		contacts.New(),
		notes.New(),
		orchestrator.NewFeedPlugin(),
		gallery.New(),
		bookmarks.New(),
		tasks.New(),
		profanity.New(),
	)
}
