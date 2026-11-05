package web_test

import (
	"net/http"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

// TestDeleteUserWithHistory: a user who shared, owned a group and a volume,
// and invited someone can be deleted, and a new account of the same name
// inherits nothing of theirs (audit H9).
func TestDeleteUserWithHistory(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	victim := env.CreateUser(t, "victim", "pass")
	other := env.CreateUser(t, "other", "pass")
	p := env.Providers

	id := uploadFileViaAPI(t, env, victim, "/secret.txt", "old data")
	_, err := p.Shares.Create(victim.ID, "files:file", id, "USER", other.ID, plugin.PermRead)
	require.NoError(t, err)
	g, err := p.Groups.Create("victims", nil, &victim.ID)
	require.NoError(t, err)
	require.NoError(t, p.Groups.AddMember(g.ID, other.ID, group.User))
	_, err = p.Invitations.Create(g.ID, "x@example.com", victim.ID, 24, group.User)
	require.NoError(t, err)
	_, err = p.Volumes.Create("mine", "", "mem://", &victim.ID, "")
	require.NoError(t, err)

	require.NoError(t, p.DeleteUser(victim.ID))

	again := env.CreateUser(t, "victim", "pass")
	resp := env.Get(t, "/remote.php/dav/files/victim/secret.txt", again.Auth)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	gg, err := p.Groups.Get(g.ID)
	require.NoError(t, err)
	require.True(t, gg.OwnerID == nil, "the group kept its deleted owner")
	n := 0
	for _, err := range p.Shares.ListBySubject(other.ID) {
		require.NoError(t, err)
		n++
	}
	require.Equal(t, 0, n)
	require.False(t, strings.Contains(env.Get(t, "/apps/files/", again.Auth).ReadBody(), "secret.txt"))
}
