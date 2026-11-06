package notes_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/notes"
)

// TestNoteBodyIsSearchable covers content indexing, which reads the note off
// disk rather than out of the database.
//
// The source only calls ContentFunc when it has the user's files,
// so a harness that does not wire one indexes titles and silently skips
// bodies. The search term here appears only in the body: a hit means the file
// was read, and no hit means content indexing is off.
func TestNoteBodyIsSearchable(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.PostJSON(t, notesAPI, map[string]any{
		"title":   "Shopping",
		"content": "remember the QUINCEPASTE for the cheeseboard",
	}, alice.Auth).Status(200)

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := env.Providers.Search.SearchUser(alice.User.Username, "QUINCEPASTE", 10, 0)
		require.NoError(t, err)
		if res != nil && len(res.Hits) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("a word that appears only in the note body was never indexed")
}
