package notes_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/notes"
)

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, notes.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "empty", "pass")
	env.CreateUser(t, "create", "pass")
	env.CreateUser(t, "edit", "pass")
	env.CreateUser(t, "star", "pass")
	env.CreateUser(t, "delete", "pass")
	env.CreateUser(t, "starred", "pass")
	env.CreateUser(t, "category", "pass")
	env.CreateUser(t, "markdown", "pass")
	env.CreateUser(t, "editor", "pass")
	env.CreateUser(t, "many", "pass")
	env.CreateUser(t, "viewdelete", "pass")

	t.Run("EmptyState", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "empty", "pass")
		b.Navigate(t, "/apps/notes/")
		b.WaitText(t, "No notes yet")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Create", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "create", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "My Test Note")
		b.Type(t, "#content", "Hello from browser test")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "My Test Note")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Edit", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "edit", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Original Title")
		b.Type(t, "#content", "Original content")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Original Title")
		b.Click(t, `a[href$="/edit"]`)
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Edited Title")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Edited Title")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Star", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "star", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Star Me")
		b.Type(t, "#content", "Star content")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Star Me")
		// Star button is on the list page, not the detail page.
		b.Navigate(t, "/apps/notes/")
		b.WaitText(t, "Star Me")
		b.Click(t, `[title="Toggle star"]`)
		b.WaitVisible(t, `.btn-star.active`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Delete", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "delete", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Delete Me")
		b.Type(t, "#content", "Delete content")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Delete Me")
		// Delete button is on the list page.
		b.Navigate(t, "/apps/notes/")
		b.WaitText(t, "Delete Me")
		b.AcceptConfirms(t)
		b.Click(t, `[hx-delete]`)
		b.WaitText(t, "No notes yet")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("DeleteFromView", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "viewdelete", "pass")
		createNote(t, b, "Keep Me", "", "kept")
		env.Clock.Advance(time.Minute)
		createNote(t, b, "Delete From View", "", "gone")
		b.AcceptConfirms(t)
		b.Click(t, `.page-actions [hx-delete]`)
		b.WaitText(t, "Deleted “Delete From View”.")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Preview", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "editor", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Previewed")
		js, err := json.Marshal(longMarkdown)
		require.NoError(t, err)
		b.Eval(t, fmt.Sprintf(`() => { document.querySelector("#content").value = %s }`, js))
		b.ClickText(t, "Preview")
		b.WaitText(t, "Section heading")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("CategoryView", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "category", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Work Note")
		b.Type(t, "#content", "Important work stuff")
		b.Type(t, "#category", "Work")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Work Note")
		// Navigate to the category page via sidebar.
		b.Navigate(t, "/apps/notes/category/Work")
		b.WaitText(t, "Work Note")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Starred", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "starred", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Starred Note")
		b.Type(t, "#content", "Content")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Starred Note")
		// Star button is on the list page.
		b.Navigate(t, "/apps/notes/")
		b.WaitText(t, "Starred Note")
		b.Click(t, `[title="Toggle star"]`)
		b.WaitVisible(t, `[title="Toggle star"]`)
		b.Navigate(t, "/apps/notes/starred")
		b.WaitText(t, "Starred Note")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("LongMarkdown", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "markdown", "pass")
		createNote(t, b, "Markdown Showcase", "Reference", longMarkdown)
		b.WaitText(t, "Section heading")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("EditorForm", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "editor", "pass")
		createNote(t, b, "Note Being Edited", "Work", longMarkdown)
		b.WaitText(t, "Section heading")
		b.Click(t, `a[href$="/edit"]`)
		b.WaitVisible(t, "#content")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("NewForm", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "editor", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ManyNotes", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "many", "pass")
		// Notes list by modification time, which the virtual clock would
		// otherwise give all four, leaving their order to SQLite.
		createNote(t, b, "Groceries", "Home", "milk")
		env.Clock.Advance(time.Minute)
		createNote(t, b, "A note whose title is long enough that it has to wrap or truncate in the list on a phone", "A Rather Long Category Name", "body")
		env.Clock.Advance(time.Minute)
		createNote(t, b, "Meeting notes 2026-03-14", "Work", "agenda")
		env.Clock.Advance(time.Minute)
		createNote(t, b, "Uncategorised thought", "", "hmm")
		b.Navigate(t, "/apps/notes/")
		b.WaitText(t, "Uncategorised thought")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

// longMarkdown exercises every block the renderer emits, so the note view's
// typography (headings, lists, quotes, code, tables, long links) is visible.
const longMarkdown = "Intro paragraph with **bold**, *italic*, `inline code` and a [link](https://example.com).\n\n" +
	"## Section heading\n\nA paragraph under the section heading that runs long enough to wrap across more than one line at desktop width, so line-height and measure are visible.\n\n" +
	"### Checklist\n\n- first item\n- second item with a much longer description that wraps onto another line\n  - nested item\n- third item\n\n" +
	"1. step one\n2. step two\n\n" +
	"> A block quote that should look different from body text.\n\n" +
	"```go\nfunc main() {\n\tfmt.Println(\"a fairly long line of code that will not fit on a phone screen without scrolling\")\n}\n```\n\n" +
	"| Column A | Column B | Column C |\n|---|---|---|\n| one | two | three |\n| four | five | six |\n\n" +
	"---\n\nhttps://example.com/a/very/long/url/without/any/natural/break/points/that/should/wrap/rather/than/overflow\n"

// createNote fills the new-note form and submits it. The body is assigned
// rather than typed so multi-line markdown arrives exactly as written.
func createNote(t *testing.T, b *webtest.Browser, title, category, body string) {
	t.Helper()
	b.Navigate(t, "/apps/notes/new")
	b.WaitVisible(t, "#title")
	b.Type(t, "#title", title)
	if category != "" {
		b.Type(t, "#category", category)
	}
	js, err := json.Marshal(body)
	require.NoError(t, err)
	b.Eval(t, fmt.Sprintf(`() => { document.querySelector("#content").value = %s }`, js))
	b.Click(t, `main button[type="submit"]`)
	b.WaitText(t, title)
}
