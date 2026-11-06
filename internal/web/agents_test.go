package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// D21: an agent is an account its owner issues tokens to. It works like a
// user who received a share, and nothing more: no password sign-in, no
// settings, no sharing of its own.
func TestAgentIsAUserWithoutSettingsOrShares(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, csrf := sessionAndCSRF(t, env, alice.ID)

	env.PostForm(t, "/settings/agents", url.Values{"csrf_token": {csrf}, "name": {"bot"}}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	bot, err := env.Providers.Users.GetByUsername("bot")
	require.NoError(t, err)
	require.Equal(t, alice.ID, bot.OwnerID)

	page := env.PostForm(t, "/settings/agents/"+bot.ID+"/tokens", url.Values{"csrf_token": {csrf}, "name": {"laptop"}}, cookie).Status(200).ReadBody()
	m := regexp.MustCompile(`secret-box[^>]*><code>([^<]+)<`).FindStringSubmatch(page)
	require.True(t, m != nil, "no token shown")
	token := m[1]
	asBot := cogtest.BasicAuth("bot", token)

	// The token signs the agent in; a password never does.
	webdavReq(t, env, "PROPFIND", "/remote.php/dav/files/"+bot.ID+"/", "", asBot, cogtest.Header("Depth", "0")).Status(http.StatusMultiStatus)
	webdavReq(t, env, "PROPFIND", "/remote.php/dav/files/"+bot.ID+"/", "", cogtest.BasicAuth("bot", "anything"), cogtest.Header("Depth", "0")).Status(http.StatusUnauthorized)
	_, err = env.Providers.Users.Authenticate("bot", "")
	require.True(t, err != nil)

	// What is shared with it, it reaches; it shares nothing itself.
	env.Put(t, "/remote.php/dav/files/"+alice.ID+"/doc.txt", nil, alice.Auth).Status(http.StatusCreated)
	fp, ok := env.Providers.Plugins.Plugin(files.PluginID)
	require.True(t, ok)
	meta, err := fp.(*files.Plugin).GetByPath("alice", "/doc.txt")
	require.NoError(t, err)
	fileID := strconv.FormatInt(meta.ID, 10)
	_, err = env.Providers.Shares.Create(alice.ID, "files:file", fileID, "USER", bot.ID, plugin.PermRead)
	require.NoError(t, err)
	env.Get(t, "/api/shares/with-me", asBot).Status(200).Contains("doc.txt")
	_, err = env.Providers.Shares.Create(bot.ID, "files:file", fileID, "USER", alice.ID, plugin.PermRead)
	require.True(t, err != nil, "an agent shared")

	// No settings, no admin, no grants.
	env.Get(t, "/settings/profile", asBot).Status(http.StatusForbidden)
	env.Get(t, "/settings/agents", asBot).Status(http.StatusForbidden)

	// Its owner's deletion takes it along.
	require.NoError(t, env.Providers.DeleteUser(alice.ID))
	_, err = env.Providers.Users.GetByUsername("bot")
	require.True(t, err != nil, "the agent outlived its owner")
}

// The Login Flow grant page can sign an app in as a new agent: the app
// gets the agent's name and a token of the agent's, not the user's.
func TestLoginFlowGrantsAsNewAgent(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, csrf := sessionAndCSRF(t, env, alice.ID)

	var init struct {
		Poll  struct{ Token string } `json:"poll"`
		Login string                 `json:"login"`
	}
	env.Post(t, "/index.php/login/v2").Status(200).JSON(&init)
	loginURL, err := url.Parse(init.Login)
	require.NoError(t, err)
	grantToken := loginURL.Query().Get("token")

	env.PostForm(t, "/index.php/login/v2/grant?token="+grantToken, url.Values{
		"csrf_token": {csrf}, "action": {"grant"}, "grant_as": {"new"}, "agent_name": {"helper"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	var granted struct {
		LoginName   string `json:"loginName"`
		AppPassword string `json:"appPassword"`
	}
	env.PostForm(t, "/index.php/login/v2/poll", url.Values{"token": {init.Poll.Token}}).Status(200).JSON(&granted)
	require.Equal(t, "helper", granted.LoginName)
	helper, err := env.Providers.Users.GetByUsername("helper")
	require.NoError(t, err)
	require.Equal(t, alice.ID, helper.OwnerID)
	webdavReq(t, env, "PROPFIND", "/remote.php/dav/files/"+helper.ID+"/", "", cogtest.BasicAuth("helper", granted.AppPassword), cogtest.Header("Depth", "0")).Status(http.StatusMultiStatus)
}

// Only an agent's owner shares with it; someone who received a share and
// may reshare it reaches their own agents, not anyone else's. Other users'
// agents are not offered as sharees.
func TestOnlyOwnerSharesWithAgent(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	abot, err := env.Providers.Users.CreateAgent(alice.ID, "abot")
	require.NoError(t, err)
	bbot, err := env.Providers.Users.CreateAgent(bob.ID, "bbot")
	require.NoError(t, err)

	env.Put(t, "/remote.php/dav/files/"+alice.ID+"/doc.txt", nil, alice.Auth).Status(http.StatusCreated)
	fp, ok := env.Providers.Plugins.Plugin(files.PluginID)
	require.True(t, ok)
	meta, err := fp.(*files.Plugin).GetByPath("alice", "/doc.txt")
	require.NoError(t, err)
	fileID := strconv.FormatInt(meta.ID, 10)

	_, err = env.Providers.Shares.Create(alice.ID, "files:file", fileID, "USER", abot.ID, plugin.PermRead)
	require.NoError(t, err)
	_, err = env.Providers.Shares.Create(bob.ID, "files:file", fileID, "USER", abot.ID, plugin.PermRead)
	require.True(t, err != nil, "bob shared with alice's agent")
	_, err = env.Providers.Shares.Create(bob.ID, "files:file", fileID, "USER", bbot.ID, plugin.PermRead)
	require.NoError(t, err)

	env.Get(t, "/api/users/search?q=bot", bob.Auth).Status(200).Contains("bbot").NotContains("abot")
	env.Get(t, "/ocs/v2.php/apps/files_sharing/api/v1/sharees?search=bot&itemType=file&format=json", bob.Auth).Status(200).Contains("bbot").NotContains("abot")
}
