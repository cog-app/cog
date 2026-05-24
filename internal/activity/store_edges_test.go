package activity_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// --- List default limit ---

func TestList_DefaultLimitApplied(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	for range 60 {
		require.NoError(t, env.Providers.Activities.Log(
			alice.ID, "alice", "files", "file_created", "x", "files:file", "1", "n"))
	}

	// limit=0 → defaults to 50 inside Store.
	got, err := env.Providers.Activities.List(alice.ID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 50, len(got))
}

func TestList_PaginationOffset(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	for range 5 {
		require.NoError(t, env.Providers.Activities.Log(
			alice.ID, "alice", "files", "x", "s", "files:file", "1", "n"))
	}

	page1, err := env.Providers.Activities.List(alice.ID, 2, 0)
	require.NoError(t, err)
	require.Equal(t, 2, len(page1))

	page2, err := env.Providers.Activities.List(alice.ID, 2, 2)
	require.NoError(t, err)
	require.Equal(t, 2, len(page2))

	// No overlap between pages.
	require.True(t, page1[0].ID != page2[0].ID)
	require.True(t, page1[1].ID != page2[1].ID)
}

// --- ListByObject default limit ---

func TestListByObject_DefaultLimit(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	for range 60 {
		require.NoError(t, env.Providers.Activities.Log(
			alice.ID, "alice", "files", "x", "s", "files:file", "42", "n"))
	}

	got, err := env.Providers.Activities.ListByObject("files:file", "42", 0)
	require.NoError(t, err)
	require.Equal(t, 50, len(got))
}

// --- ListSince (was 0% coverage) ---

func TestListSince_FiltersByID(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	for range 4 {
		require.NoError(t, env.Providers.Activities.Log(
			alice.ID, "alice", "files", "x", "s", "files:file", "1", "n"))
	}

	// Grab the most recent activity ID.
	all, _ := env.Providers.Activities.List(alice.ID, 10, 0)
	require.True(t, len(all) == 4)
	cutoff := all[len(all)-1].ID // earliest

	got, err := env.Providers.Activities.ListSince(alice.ID, cutoff, 100)
	require.NoError(t, err)
	// 3 newer than the earliest.
	require.Equal(t, 3, len(got))

	// Since the newest → empty.
	newest := all[0].ID
	got, err = env.Providers.Activities.ListSince(alice.ID, newest, 100)
	require.NoError(t, err)
	require.Equal(t, 0, len(got))
}

func TestListSince_DefaultLimit(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	for range 60 {
		require.NoError(t, env.Providers.Activities.Log(
			alice.ID, "alice", "files", "x", "s", "files:file", "1", "n"))
	}

	got, err := env.Providers.Activities.ListSince(alice.ID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 50, len(got))
}

func TestListSince_OtherUserNotIncluded(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	require.NoError(t, env.Providers.Activities.Log(alice.ID, "alice", "files", "x", "s", "files:file", "1", "n"))
	require.NoError(t, env.Providers.Activities.Log(bob.ID, "bob", "files", "x", "s", "files:file", "1", "n"))

	got, err := env.Providers.Activities.ListSince(alice.ID, 0, 100)
	require.NoError(t, err)
	require.Equal(t, 1, len(got))
	require.Equal(t, alice.ID, got[0].UserID)
}

// --- LogRich + param round-trip ---

func TestLogRich_StoresParamsJSON(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	require.NoError(t, env.Providers.Activities.LogRich(
		alice.ID, "alice", "files", "file_created",
		"alice uploaded foo.txt",
		"{user} uploaded {file}",
		map[string]any{"user": "alice", "file": "foo.txt"},
		"files:file", "1", "foo.txt",
	))

	got, err := env.Providers.Activities.List(alice.ID, 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(got))
	require.Equal(t, "{user} uploaded {file}", got[0].SubjectTemplate)
	require.True(t, len(got[0].SubjectParams) > 0)
}

func TestLogRich_NilParams(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	require.NoError(t, env.Providers.Activities.LogRich(
		alice.ID, "alice", "files", "t", "subj", "tmpl", nil,
		"files:file", "1", "n",
	))

	got, _ := env.Providers.Activities.List(alice.ID, 10, 0)
	require.Equal(t, 1, len(got))
	// nil params encodes as JSON "null".
	require.Equal(t, "null", got[0].SubjectParams)
}
