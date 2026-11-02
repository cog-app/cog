package profanity_test

import (
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
)

// Saving either settings page says so through the flash cookie the page shell
// renders, not a ?flash= query parameter anyone can put in a link.
func TestSettingsSaveFlashes(t *testing.T) {
	env := cogtest.New(t, profanity.New())
	admin := env.CreateUser(t, "admin", "pass")
	sess, err := env.Providers.Sessions.Create(admin.ID)
	require.NoError(t, err)
	session := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := cogtest.Header("X-CSRF-Token", auth.CSRFToken(sess.ID, env.Providers.CSRFSecret))

	for _, tc := range []struct{ path, want string }{
		{"/apps/profanity/settings", "Word list saved."},
		{"/admin/plugins/profanity", "Settings saved."},
	} {
		resp := env.PostForm(t, tc.path, url.Values{"wordlist": {"darn=dang"}, "tts_engine": {""}},
			session, csrf, cogtest.StopRedirect)
		resp.Status(303)
		require.Equal(t, tc.path, resp.Header.Get("Location"))

		var flash string
		for _, ck := range resp.Cookies() {
			if ck.Name == cogplugin.FlashCookieName {
				flash = ck.Name + "=" + ck.Value
			}
		}
		require.True(t, flash != "", "no flash cookie from POST "+tc.path)

		env.Get(t, tc.path, cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID+"; "+flash)).
			Status(200).
			Contains(tc.want)
		env.Get(t, tc.path+"?flash=Injected", session).
			Status(200).
			NotContains("Injected")
	}
}
