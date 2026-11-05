package web_test

import (
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestGroupAddNeedsConsent: an owner who is not an admin invites; the user
// joins only by accepting, and nobody else can accept for them.
func TestGroupAddNeedsConsent(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	mallory := env.CreateUser(t, "mallory", "pass")
	g, err := env.Providers.Groups.Create("team", nil, &alice.ID)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, alice.ID, group.Owner))

	cookie, token := sessionAndCSRF(t, env, alice.ID)
	env.PostForm(t, "/settings/groups/"+g.ID+"/members", url.Values{"csrf_token": {token}, "member_id": {bob.ID}},
		cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	shared, err := env.Providers.Groups.ShareAGroup(alice.ID, bob.ID)
	require.NoError(t, err)
	require.False(t, shared, "bob was added without accepting")

	invs, err := seq.Collect(env.Providers.Invitations.ListForInvitee(bob.ID))
	require.NoError(t, err)
	require.Equal(t, 1, len(invs))

	mc, mt := sessionAndCSRF(t, env, mallory.ID)
	env.PostForm(t, "/settings/groups/invitations/"+invs[0].ID+"/accept", url.Values{"csrf_token": {mt}}, mc, cogtest.StopRedirect).Status(http.StatusSeeOther)
	env.PostForm(t, "/invite/"+invs[0].Token, url.Values{"csrf_token": {mt}}, mc, cogtest.StopRedirect).Status(http.StatusForbidden)
	shared, err = env.Providers.Groups.ShareAGroup(alice.ID, mallory.ID)
	require.NoError(t, err)
	require.False(t, shared)

	bc, bt := sessionAndCSRF(t, env, bob.ID)
	env.Get(t, "/settings/groups", bc).Status(200).Contains("invited you to")
	env.PostForm(t, "/settings/groups/invitations/"+invs[0].ID+"/accept", url.Values{"csrf_token": {bt}}, bc, cogtest.StopRedirect).Status(http.StatusSeeOther)
	shared, err = env.Providers.Groups.ShareAGroup(alice.ID, bob.ID)
	require.NoError(t, err)
	require.True(t, shared)
}
