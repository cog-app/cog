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
	bot, err := env.Providers.Users.GetByUsername("alice+bot")
	require.NoError(t, err)
	require.Equal(t, alice.ID, bot.OwnerID)

	token := createAgentToken(t, env, bot.ID, "laptop", cookie, csrf)
	asBot := cogtest.BasicAuth("alice+bot", token)

	// The token signs the agent in; a password never does.
	webdavReq(t, env, "PROPFIND", "/remote.php/dav/files/"+bot.ID+"/", "", asBot, cogtest.Header("Depth", "0")).Status(http.StatusMultiStatus)
	webdavReq(t, env, "PROPFIND", "/remote.php/dav/files/"+bot.ID+"/", "", cogtest.BasicAuth("alice+bot", "anything"), cogtest.Header("Depth", "0")).Status(http.StatusUnauthorized)
	_, err = env.Providers.Users.Authenticate("alice+bot", "")
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
	_, err = env.Providers.Users.GetByUsername("alice+bot")
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
	require.Equal(t, "alice+helper", granted.LoginName)
	helper, err := env.Providers.Users.GetByUsername("alice+helper")
	require.NoError(t, err)
	require.Equal(t, alice.ID, helper.OwnerID)
	webdavReq(t, env, "PROPFIND", "/remote.php/dav/files/"+helper.ID+"/", "", cogtest.BasicAuth("alice+helper", granted.AppPassword), cogtest.Header("Depth", "0")).Status(http.StatusMultiStatus)
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

// createAgentToken issues a token through the page, as its HTMX form does,
// and returns the token the response shows.
func createAgentToken(t *testing.T, env *cogtest.Env, agentID, name string, cookie cogtest.HTTPOpt, csrf string) string {
	t.Helper()
	body := env.PostForm(t, "/settings/agents/"+agentID+"/tokens", url.Values{"csrf_token": {csrf}, "name": {name}}, cookie, cogtest.Header("HX-Request", "true")).Status(200).ReadBody()
	m := regexp.MustCompile(`(?s)class="secret-box".*?<code>([^<]+)</code>`).FindStringSubmatch(body)
	require.True(t, m != nil, "no token shown: %s", body)
	require.Contains(t, body, `id="agent-tokens-`+agentID+`"`)
	return m[1]
}

// A plain form post would make a token nobody could see; it makes none.
func TestAgentTokenNeedsHTMX(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, csrf := sessionAndCSRF(t, env, alice.ID)
	bot, err := env.Providers.Users.CreateAgent(alice.ID, "bot")
	require.NoError(t, err)

	env.PostForm(t, "/settings/agents/"+bot.ID+"/tokens", url.Values{"csrf_token": {csrf}, "name": {"laptop"}}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	toks, err := env.Providers.AppPasswords.ListByUser(bot.ID)
	require.NoError(t, err)
	require.Equal(t, 0, len(toks))

	// Nor does a nameless one.
	env.PostForm(t, "/settings/agents/"+bot.ID+"/tokens", url.Values{"csrf_token": {csrf}}, cookie, cogtest.Header("HX-Request", "true")).Status(200).Contains("needs a name")
	toks, err = env.Providers.AppPasswords.ListByUser(bot.ID)
	require.NoError(t, err)
	require.Equal(t, 0, len(toks))
}

// Revoking a token signs it out; deleting an agent removes it. Neither
// reaches an agent someone else owns, nor a token of another agent.
func TestAgentRevokeAndDelete(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	mallory := env.CreateUser(t, "mallory", "pass")
	aliceCookie, aliceCSRF := sessionAndCSRF(t, env, alice.ID)
	malCookie, malCSRF := sessionAndCSRF(t, env, mallory.ID)
	bot, err := env.Providers.Users.CreateAgent(alice.ID, "bot")
	require.NoError(t, err)
	other, err := env.Providers.Users.CreateAgent(alice.ID, "other")
	require.NoError(t, err)
	token := createAgentToken(t, env, bot.ID, "laptop", aliceCookie, aliceCSRF)
	asBot := cogtest.BasicAuth("alice+bot", token)
	toks, err := env.Providers.AppPasswords.ListByUser(bot.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(toks))
	revoke := "/settings/agents/" + bot.ID + "/tokens/" + toks[0].ID + "/delete"
	del := "/settings/agents/" + bot.ID + "/delete"

	// Someone else's agent is not found, to revoke or to delete.
	env.PostForm(t, revoke, url.Values{"csrf_token": {malCSRF}}, malCookie, cogtest.StopRedirect).Status(http.StatusNotFound)
	env.PostForm(t, del, url.Values{"csrf_token": {malCSRF}}, malCookie, cogtest.StopRedirect).Status(http.StatusNotFound)
	// A token is revoked only through the agent it belongs to.
	env.PostForm(t, "/settings/agents/"+other.ID+"/tokens/"+toks[0].ID+"/delete", url.Values{"csrf_token": {aliceCSRF}}, aliceCookie, cogtest.StopRedirect).Status(http.StatusNotFound)
	env.Get(t, "/api/shares/with-me", asBot).Status(200)

	env.PostForm(t, revoke, url.Values{"csrf_token": {aliceCSRF}}, aliceCookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	env.Get(t, "/api/shares/with-me", asBot).Status(http.StatusUnauthorized)

	env.PostForm(t, del, url.Values{"csrf_token": {aliceCSRF}}, aliceCookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	_, err = env.Providers.Users.GetByID(bot.ID)
	require.True(t, err != nil, "agent still there")
	_, err = env.Providers.Users.GetByID(other.ID)
	require.NoError(t, err)
}
