package feed_test

import (
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

const systemSettingsPath = "/admin/plugins/" + feed.PluginID

// webSession signs u in with a session, and returns its cookie value and
// the CSRF token that goes with it.
func webSession(t *testing.T, env *cogtest.Env, u *cogtest.User) (cookie, csrf string) {
	t.Helper()
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	return auth.SessionCookieName + "=" + sess.ID, auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
}

// saveSystemSettings posts form to the system settings page as the session
// cookie's user, expects the redirect back, and returns the page that
// redirect shows, flash included.
func saveSystemSettings(t *testing.T, env *cogtest.Env, cookie, csrf string, form url.Values) *cogtest.Response {
	t.Helper()
	form.Set("csrf_token", csrf)
	resp := env.PostForm(t, systemSettingsPath, form, cogtest.Header("Cookie", cookie), cogtest.StopRedirect)
	resp.Status(http.StatusSeeOther)
	require.Equal(t, systemSettingsPath, resp.Header.Get("Location"))
	for _, ck := range resp.Cookies() {
		if ck.Name == cogplugin.FlashCookieName {
			cookie += "; " + ck.Name + "=" + ck.Value
		}
	}
	return env.Get(t, systemSettingsPath, cogtest.Header("Cookie", cookie)).Status(http.StatusOK)
}

// systemForm is a complete system settings form: what the page posts.
func systemForm(mode string) url.Values {
	return url.Values{
		"whisper_model":      {string(feed.WhisperBaseEn)},
		"whisper_models_dir": {""},
		"whisper_cli_path":   {""},
		"youtube_mode":       {mode},
	}
}

// setYouTubeMode sets the server's YouTube playback mode as the admin
// setting does.
func setYouTubeMode(t *testing.T, env *cogtest.Env, mode feed.YouTubeMode) {
	t.Helper()
	db, err := feedPlugin(t, env).RootDB()
	require.NoError(t, err)
	require.NoError(t, feed.SettingYouTubeMode.Set(db, mode))
}

func TestYouTubeModeDefault(t *testing.T) {
	env := cogtest.New(t, feed.New())
	mode, err := feed.YouTubeModeOf(feedPlugin(t, env))
	require.NoError(t, err)
	assert.Equal(t, feed.YouTubeDirect, mode)
}

// A stored mode the server does not know is an error, never direct.
func TestYouTubeModeUnknownStored(t *testing.T) {
	env := cogtest.New(t, feed.New())
	cookie, _ := webSession(t, env, env.CreateUser(t, "admin", "pw"))
	setYouTubeMode(t, env, "sideways")
	_, err := feed.YouTubeModeOf(feedPlugin(t, env))
	require.Error(t, err)
	// The admin can still open the page to repair it.
	env.Get(t, systemSettingsPath, cogtest.Header("Cookie", cookie)).
		Status(http.StatusOK).
		NotContains("checked")
}

func TestSystemSettingsYouTubeMode(t *testing.T) {
	env := cogtest.New(t, feed.New())
	cookie, csrf := webSession(t, env, env.CreateUser(t, "admin", "pw"))

	page := saveSystemSettings(t, env, cookie, csrf, systemForm("proxy"))
	page.Contains("Saved.")
	page.Contains(`value="proxy" checked`)
	mode, err := feed.YouTubeModeOf(feedPlugin(t, env))
	require.NoError(t, err)
	assert.Equal(t, feed.YouTubeProxy, mode)
}

func TestSystemSettingsYouTubeModeRejectsUnknown(t *testing.T) {
	env := cogtest.New(t, feed.New())
	cookie, csrf := webSession(t, env, env.CreateUser(t, "admin", "pw"))
	form := systemForm("x")
	form.Set("whisper_model", string(feed.WhisperTinyEn))

	page := saveSystemSettings(t, env, cookie, csrf, form)
	page.Contains("Unknown YouTube playback mode")
	page.NotContains("Saved.")
	page.Contains(`value="direct" checked`)
	mode, err := feed.YouTubeModeOf(feedPlugin(t, env))
	require.NoError(t, err)
	assert.Equal(t, feed.YouTubeDirect, mode)
	// Nothing else in the form was saved either.
	ws, err := feedPlugin(t, env).WhisperSettings()
	require.NoError(t, err)
	assert.Equal(t, feed.WhisperBaseEn, ws.Model)
}

func TestSystemSettingsAdminOnly(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "admin", "pw")
	cookie, csrf := webSession(t, env, env.CreateUser(t, "bob", "pw"))

	env.Get(t, systemSettingsPath, cogtest.Header("Cookie", cookie)).Status(http.StatusForbidden)
	form := systemForm("proxy")
	form.Set("csrf_token", csrf)
	env.PostForm(t, systemSettingsPath, form, cogtest.Header("Cookie", cookie), cogtest.StopRedirect).
		Status(http.StatusForbidden)
	mode, err := feed.YouTubeModeOf(feedPlugin(t, env))
	require.NoError(t, err)
	assert.Equal(t, feed.YouTubeDirect, mode)
}
