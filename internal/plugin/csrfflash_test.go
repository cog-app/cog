package plugin_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

// formPlugin is a page and the action its form posts to, which is the shape
// every plugin form has.
type formPlugin struct{}

func (p *formPlugin) ID() string { return "formtest" }

func (p *formPlugin) Register(ctx cogplugin.PluginContext) error {
	ctx.RegisterPage("{$}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<p id="page-body">the form</p>`)
	}))
	ctx.RegisterAction("POST", "submit", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/apps/formtest/", http.StatusSeeOther)
	}))
	return nil
}

func (p *formPlugin) Start(_ context.Context) error { return nil }
func (p *formPlugin) Stop() error                   { return nil }

// A form posted with a stale token is rejected, and the rejection has to be
// visible on the page it lands on. Before, the redirect carried nothing: no
// message, no log line, indistinguishable from the browser ignoring the click.
func TestCSRFRejectionShowsOnThePluginPage(t *testing.T) {
	env := cogtest.New(t, &formPlugin{})
	alice := env.CreateUser(t, "alice", "pass")

	sess, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)

	resp := env.PostForm(t, "/apps/formtest/submit", url.Values{
		"csrf_token": {"stale-token"},
		"field":      {"whatever"},
	}, cookie, cogtest.StopRedirect)
	resp.Status(http.StatusSeeOther)

	var flash *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == cogplugin.FlashCookieName {
			flash = ck
		}
	}
	require.NotNil(t, flash) // the redirect carries the message

	// The page the browser lands on renders it through the plugin shell. Both
	// cookies go in one header, the way a browser sends them.
	withFlash := cogtest.Header("Cookie",
		auth.SessionCookieName+"="+sess.ID+"; "+flash.Name+"="+flash.Value)
	page := env.Get(t, "/apps/formtest/", withFlash)
	page.Status(200).
		Contains("had expired").
		Contains("alert-error").
		Contains("the form")

	// One-time: the shell clears the cookie as it renders it, so a reload of
	// the page does not show the message again.
	var cleared bool
	for _, ck := range page.Cookies() {
		if ck.Name == cogplugin.FlashCookieName && ck.MaxAge < 0 {
			cleared = true
		}
	}
	assert.True(t, cleared, "shell did not clear the flash cookie")

	// The rejection is attributed to the plugin whose route it was.
	entry := waitForLogEntry(t, env, "invalid CSRF token")
	assert.Equal(t, "formtest", entry.Source)
}
