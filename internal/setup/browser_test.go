package setup

import (
	"net/http"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
)

// TestBrowserWizard: the wizard's pages as a person sees them: the form
// with the HTTPS and DNS parts showing, and the review of a plan for the
// home network.
func TestBrowserWizard(t *testing.T) {
	saved := LANAddress
	LANAddress = func() string { return "192.168.1.20" }
	t.Cleanup(func() { LANAddress = saved })
	_, _, start := wizard(t)
	eng := webtest.New(http.NotFoundHandler())
	t.Cleanup(eng.Close)
	b := eng.Start(t)
	b.NavigateURL(t, start)
	b.WaitVisible(t, "form[action='/plan']")

	b.Click(t, "input[name=access][value=internet]")
	b.Click(t, "input[name=challenge][value=dns-01]")
	b.Select(t, "#dns_provider", "option[value=duckdns]")
	b.WaitVisible(t, ".when-dns-duckdns")
	b.Snapshot(t, webtest.State("form"))

	b.Click(t, "input[name=access][value=lan]")
	b.Type(t, "#admin_password", "correct horse")
	b.Type(t, "#admin_password_confirm", "correct horse")
	b.Submit(t, "form[action='/plan']")
	b.WaitText(t, "Ready to set up")
	b.Snapshot(t, webtest.State("review"))
}
