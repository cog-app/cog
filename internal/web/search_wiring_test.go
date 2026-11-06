package web_test

import (
	"net/url"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/notes"
)

// waitForHit blocks until a query returns something in a user's index.
func waitForHit(t *testing.T, env *cogtest.Env, username, query string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := env.Providers.Search.SearchUser(username, query, 10, 0)
		require.NoError(t, err)
		if res != nil && len(res.Hits) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%q never appeared in %s's index", query, username)
}

// TestSystemIndexPicksUpNewUser covers the system database's update hook.
//
// The full sync at startup runs against an empty users table, so this user can
// only reach the index through the hook that fires on the insert.
func TestSystemIndexPicksUpNewUser(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "quintessa", "pass")

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := env.Providers.Search.SearchSystem("quintessa", 10, 0)
		require.NoError(t, err)
		if res != nil && len(res.Hits) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("a user created after startup never reached the system index")
}

// TestSharePropagatesIntoRecipientIndex covers the propagator, which copies a
// shared resource into the recipient's own index so their search can find it.
//
// It also covers the closures that resolve a share's subject to usernames:
// without them the propagator has a recipient id and no idea whose index to
// write to.
func TestSharePropagatesIntoRecipientIndex(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	var created map[string]any
	env.PostJSON(t, "/index.php/apps/notes/api/v1/notes", map[string]any{
		"title":   "PORTABLERECIPE",
		"content": "shared with bob",
	}, alice.Auth).Status(200).JSON(&created)
	noteID := created["id"].(float64)

	// Propagation copies the document out of the owner's index, so wait until
	// it is there. Sharing before then is a separate problem, noted below.
	waitForHit(t, env, alice.User.Username, "PORTABLERECIPE")

	// Bob cannot see it yet.
	res, err := env.Providers.Search.SearchUser(bob.User.Username, "PORTABLERECIPE", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 0, len(res.Hits))

	cookie, token := sessionAndCSRF(t, env, alice.ID)
	env.PostForm(t, "/api/shares/create", url.Values{
		"csrf_token":    {token},
		"resource_type": {"notes:note"},
		"resource_id":   {itoa(int64(noteID))},
		"subject_type":  {"USER"},
		"subject_id":    {bob.ID},
		"permissions":   {"1"},
	}, cookie).Status(200)

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := env.Providers.Search.SearchUser(bob.User.Username, "PORTABLERECIPE", 10, 0)
		require.NoError(t, err)
		if res != nil && len(res.Hits) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("a note shared with bob never reached bob's index")
}

// TestShareImmediatelyAfterCreateReachesRecipient shares a note the instant it
// is created, with the session prepared in advance so the share request
// follows as closely as possible.
//
// Indexing wins that race every time here, so this does not exercise the retry
// in the propagator — it covers the ordinary case of sharing something you
// just made, which is worth having either way.
func TestShareImmediatelyAfterCreateReachesRecipient(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	// Everything the share needs is prepared first, so the share request
	// follows the note as closely as the harness allows.
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	var created map[string]any
	env.PostJSON(t, "/index.php/apps/notes/api/v1/notes", map[string]any{
		"title":   "HASTYSHARE",
		"content": "shared immediately",
	}, alice.Auth).Status(200).JSON(&created)

	env.PostForm(t, "/api/shares/create", url.Values{
		"csrf_token":    {token},
		"resource_type": {"notes:note"},
		"resource_id":   {itoa(int64(created["id"].(float64)))},
		"subject_type":  {"USER"},
		"subject_id":    {bob.ID},
		"permissions":   {"1"},
	}, cookie).Status(200)

	waitForHit(t, env, bob.User.Username, "HASTYSHARE")
}

// searchHits returns how many results a query finds in a user's index now.
func searchHits(t *testing.T, env *cogtest.Env, username, query string) int {
	t.Helper()
	res, err := env.Providers.Search.SearchUser(username, query, 10, 0)
	require.NoError(t, err)
	return len(res.Hits)
}

// TestSharedResultsFollowAccess: a share made through the store reaches the
// recipient's search, and leaving the group it went to — which changes no
// share — or the share expiring takes it out of their results at once (M4).
func TestSharedResultsFollowAccess(t *testing.T) {
	env := cogtest.New(t, notes.New())
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	p := env.Providers

	var created map[string]any
	env.PostJSON(t, "/index.php/apps/notes/api/v1/notes", map[string]any{
		"title":   "GROUPSECRET",
		"content": "for the team",
	}, alice.Auth).Status(200).JSON(&created)
	noteID := itoa(int64(created["id"].(float64)))
	waitForHit(t, env, alice.User.Username, "GROUPSECRET")

	g, err := p.Groups.Create("team", nil, &alice.ID)
	require.NoError(t, err)
	require.NoError(t, p.Groups.AddMember(g.ID, bob.ID, group.User))
	rec, err := p.Shares.Create(alice.ID, "notes:note", noteID, "GROUP", g.ID, plugin.PermRead)
	require.NoError(t, err)
	waitForHit(t, env, bob.User.Username, "GROUPSECRET")

	require.NoError(t, p.Groups.RemoveMember(g.ID, bob.ID))
	require.Equal(t, 0, searchHits(t, env, bob.User.Username, "GROUPSECRET"))

	require.NoError(t, p.Groups.AddMember(g.ID, bob.ID, group.User))
	require.NoError(t, p.Shares.Update(rec.ID, nil, nil))
	waitForHit(t, env, bob.User.Username, "GROUPSECRET")
	past := env.Clock.Now().Add(-time.Hour).Unix()
	pastp := &past
	require.NoError(t, p.Shares.Update(rec.ID, nil, &pastp))
	require.Equal(t, 0, searchHits(t, env, bob.User.Username, "GROUPSECRET"))
}
