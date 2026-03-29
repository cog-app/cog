package notes_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/notes"
)

const notesAPI = "/index.php/apps/notes/api/v1/notes"

func TestNotesAPI_CreateAndGet(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Create a note via the NC Notes API.
	var created map[string]any
	env.PostJSON(t, notesAPI, map[string]any{
		"title":   "My First Note",
		"content": "Hello from notes",
	}, alice.Auth).Status(200).JSON(&created)

	require.Equal(t, "My First Note", created["title"])
	require.Equal(t, "Hello from notes", created["content"])

	// Get it back.
	id := created["id"].(float64)
	var got map[string]any
	env.Get(t, notesAPI+"/"+itoa(int64(id)), alice.Auth).
		Status(200).JSON(&got)
	require.Equal(t, "My First Note", got["title"])
	require.Equal(t, "Hello from notes", got["content"])
}

func TestNotesAPI_List(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.PostJSON(t, notesAPI, map[string]any{"title": "Alpha"}, alice.Auth).Status(200)
	env.PostJSON(t, notesAPI, map[string]any{"title": "Beta"}, alice.Auth).Status(200)

	var list []map[string]any
	env.Get(t, notesAPI, alice.Auth).Status(200).JSON(&list)
	require.Equal(t, 2, len(list))
}

func TestNotesAPI_Update(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")

	var created map[string]any
	env.PostJSON(t, notesAPI, map[string]any{
		"title":   "Draft",
		"content": "v1",
	}, alice.Auth).Status(200).JSON(&created)
	id := itoa(int64(created["id"].(float64)))

	// Update content via PUT.
	var updated map[string]any
	env.Do(t, cogtest.NewRequest(t, "PUT", env.Providers.Config.BaseURL+notesAPI+"/"+id,
		jsonReader(map[string]any{"content": "v2"})),
		alice.Auth, cogtest.Header("Content-Type", "application/json"),
	).Status(200).JSON(&updated)

	require.Equal(t, "v2", updated["content"])
}

func TestNotesAPI_Delete(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")

	var created map[string]any
	env.PostJSON(t, notesAPI, map[string]any{"title": "Ephemeral"}, alice.Auth).
		Status(200).JSON(&created)
	id := itoa(int64(created["id"].(float64)))

	env.Delete(t, notesAPI+"/"+id, alice.Auth).Status(200)

	// Should be gone.
	env.Get(t, notesAPI+"/"+id, alice.Auth).Status(404)
}

func TestNotesAPI_Favorite(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")

	var created map[string]any
	env.PostJSON(t, notesAPI, map[string]any{
		"title":    "Star Me",
		"favorite": true,
	}, alice.Auth).Status(200).JSON(&created)

	require.Equal(t, true, created["favorite"])
}

func TestNotesAPI_Category(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.PostJSON(t, notesAPI, map[string]any{
		"title":    "Work Note",
		"category": "work",
	}, alice.Auth).Status(200)
	env.PostJSON(t, notesAPI, map[string]any{
		"title":    "Personal Note",
		"category": "personal",
	}, alice.Auth).Status(200)

	// Filter by category.
	var list []map[string]any
	env.Get(t, notesAPI+"?category=work", alice.Auth).Status(200).JSON(&list)
	require.Equal(t, 1, len(list))
	require.Equal(t, "Work Note", list[0]["title"])
}

func TestNotesAPI_Settings(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")

	var settings map[string]any
	env.Get(t, "/index.php/apps/notes/api/v1/settings", alice.Auth).
		Status(200).JSON(&settings)
	require.Equal(t, "Notes", settings["notesPath"])
	require.Equal(t, ".md", settings["fileSuffix"])
}

func TestNotesAPI_PutSettings(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")

	var settings map[string]any
	req := cogtest.NewRequest(t, "PUT", env.Providers.Config.BaseURL+"/index.php/apps/notes/api/v1/settings",
		jsonReader(map[string]any{"notesPath": "MyNotes", "fileSuffix": ".txt"}))
	req.Header.Set("Content-Type", "application/json")
	env.Do(t, req, alice.Auth).Status(200).JSON(&settings)

	// The server ignores custom values and always returns defaults.
	require.Equal(t, "Notes", settings["notesPath"])
	require.Equal(t, ".md", settings["fileSuffix"])
}

func TestNotesCategoryPage(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Create a note with a category via API so the DB is migrated.
	env.PostJSON(t, notesAPI, map[string]any{
		"title":    "Work Note",
		"content":  "work stuff",
		"category": "work",
	}, alice.Auth).Status(200)

	// GET the category page.
	env.Get(t, "/apps/notes/category/work", alice.Auth).
		Status(200).Contains("work")
}

func TestNotesPage(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/notes/", alice.Auth).Status(200)
}
