package share_test

import (
	"errors"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

// A share reaches its audience without their consent, so a user may share
// with everyone only as an admin, and with a group only one of their own.
func TestShareAudience(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass") // the first user is an admin
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	shares := env.Providers.Shares

	_, err := shares.Create(alice.ID, "files:file", "1", share.SubjectInstance, "", plugin.PermRead)
	require.True(t, errors.Is(err, share.ErrSubjectNotAllowed), "got %v", err)
	_, err = shares.Create(admin.ID, "files:file", "1", share.SubjectInstance, "", plugin.PermRead)
	require.NoError(t, err)

	g, err := env.Providers.Groups.Create("bobs", nil, &bob.ID)
	require.NoError(t, err)
	_, err = shares.Create(alice.ID, "files:file", "1", "GROUP", g.ID, plugin.PermRead)
	require.True(t, errors.Is(err, share.ErrSubjectNotAllowed), "got %v", err)
	_, err = shares.Create(bob.ID, "files:file", "2", "GROUP", g.ID, plugin.PermRead)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, alice.ID, group.User))
	_, err = shares.Create(alice.ID, "files:file", "1", "GROUP", g.ID, plugin.PermRead)
	require.NoError(t, err)
}
