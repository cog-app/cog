package federation_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/federation"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func allowed(t *testing.T, p *federation.Policy, host string) bool {
	t.Helper()
	ok, err := p.PeerAllowed(host)
	require.NoError(t, err)
	return ok
}

func TestPolicy_OffByDefault(t *testing.T) {
	env := cogtest.New(t)
	p := &federation.Policy{Settings: env.Providers.Settings}

	on, err := p.Enabled()
	require.NoError(t, err)
	require.False(t, on)
	require.False(t, allowed(t, p, "any.example.com"))
}

func TestPolicy_EnabledEmptyListAllowsAny(t *testing.T) {
	env := cogtest.New(t)
	require.NoError(t, env.Providers.Settings.SetBool(settings.KeyFederationEnabled, true))
	p := &federation.Policy{Settings: env.Providers.Settings}

	require.True(t, allowed(t, p, "any.example.com"))
}

func TestPolicy_AllowlistMatchesNormalizedHosts(t *testing.T) {
	env := cogtest.New(t)
	require.NoError(t, env.Providers.Settings.SetMany(map[string]string{
		settings.KeyFederationEnabled: "true",
		settings.KeyFederationPeers:   "GOOD.example.com,https://other.example.com/",
	}))
	p := &federation.Policy{Settings: env.Providers.Settings}

	require.True(t, allowed(t, p, "good.example.com"))
	require.True(t, allowed(t, p, "https://other.example.com"))
	require.True(t, allowed(t, p, "http://Other.Example.com/"))
	require.False(t, allowed(t, p, "evil.example.com"))
}

func TestPolicy_EnvPeersEnableUntilAdminDecides(t *testing.T) {
	env := cogtest.New(t)
	p := &federation.Policy{Settings: env.Providers.Settings, EnvPeers: "cog.example.com"}

	require.True(t, allowed(t, p, "cog.example.com"))
	require.False(t, allowed(t, p, "evil.example.com"))

	require.NoError(t, env.Providers.Settings.SetBool(settings.KeyFederationEnabled, false))
	require.False(t, allowed(t, p, "cog.example.com"))
}

func TestFederationInboundShareRefusedWhenOff(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")
	require.NoError(t, env.Providers.Settings.SetBool(settings.KeyFederationEnabled, false))

	env.PostJSON(t, "/ocm/shares", map[string]any{
		"shareWith": "alice", "name": "x.txt", "owner": "bob@remote.example.com",
		"protocol": map[string]any{"name": "webdav", "options": map[string]any{"sharedSecret": "s"}},
	}).Status(403)

	// An unknown user is refused the same way, so a closed server says
	// nothing about who has an account.
	env.PostJSON(t, "/ocm/shares", map[string]any{
		"shareWith": "nobody", "name": "x.txt", "owner": "bob@remote.example.com",
		"protocol": map[string]any{"name": "webdav", "options": map[string]any{"sharedSecret": "s"}},
	}).Status(403)
}

// enableFederation turns federation on for env, with any server allowed.
func enableFederation(t *testing.T, env *cogtest.Env) {
	t.Helper()
	require.NoError(t, env.Providers.Settings.SetBool(settings.KeyFederationEnabled, true))
}
