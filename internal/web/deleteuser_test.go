package web_test

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/federation"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/user"
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
	inbound := &federation.FederatedShare{Direction: "inbound", State: "accepted", RemoteUser: "bob", LocalUserID: victim.ID}
	require.NoError(t, p.Federation.Create(inbound))

	require.NoError(t, p.DeleteUser(victim.ID))

	// The name is retired until an admin releases it.
	_, err = p.Users.Create("Victim", "pass")
	require.True(t, errors.Is(err, user.ErrUsernameRetired), "got %v", err)
	require.NoError(t, p.Users.Release("victim"))
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
	_, err = p.Federation.GetByToken(inbound.Token)
	require.True(t, errors.Is(err, federation.ErrNotFound), "the deleted user's federated share is still there: %v", err)
	require.False(t, strings.Contains(env.Get(t, "/apps/files/", again.Auth).ReadBody(), "secret.txt"))
}

// TestRetiredNameAndEmail: an admin can't give out a deleted account's name
// or address until they release the name from the users page.
func TestRetiredNameAndEmail(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)
	form := func(v url.Values) url.Values {
		if v == nil {
			v = url.Values{}
		}
		v.Set("csrf_token", token)
		return v
	}
	gone := env.CreateUser(t, "gone", "pass")
	other := env.CreateUser(t, "other", "pass")
	addr := "gone@example.com"
	require.NoError(t, env.Providers.Users.Update(gone.ID, user.UpdateOpts{Email: &addr}))
	require.NoError(t, env.Providers.DeleteUser(gone.ID))

	env.Get(t, "/admin/users", cookie).Status(http.StatusOK).Contains("Retired names").Contains(addr)
	env.PostForm(t, "/admin/users", form(url.Values{"username": {"gone"}, "password": {"pw"}}), cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	_, err := env.Providers.Users.GetByUsername("gone")
	require.True(t, errors.Is(err, user.ErrNotFound), "a retired name was given out: %v", err)
	env.PostForm(t, "/admin/users/"+other.ID, form(url.Values{"email": {addr}}), cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	o, err := env.Providers.Users.GetByID(other.ID)
	require.NoError(t, err)
	require.Equal(t, "", o.Email)

	env.PostForm(t, "/admin/users/retired/gone/release", form(nil), cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	env.PostForm(t, "/admin/users/"+other.ID, form(url.Values{"email": {addr}}), cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	o, err = env.Providers.Users.GetByID(other.ID)
	require.NoError(t, err)
	require.Equal(t, addr, o.Email)
	env.PostForm(t, "/admin/users", form(url.Values{"username": {"gone"}, "password": {"pw"}}), cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	_, err = env.Providers.Users.GetByUsername("gone")
	require.NoError(t, err)
}
