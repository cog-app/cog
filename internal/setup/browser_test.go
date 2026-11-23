package setup

import (
	"net/http"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
)

// TestBrowserWizard: the guided wizard as a person sees it: one question
// a page, the certificate page with only the chosen DNS host's fields
// showing, and the review.
func TestBrowserWizard(t *testing.T) {
	saved := LANAddress
	LANAddress = func() string { return "192.168.1.20" }
	t.Cleanup(func() { LANAddress = saved })
	savedHost := Hostname
	Hostname = func() (string, error) { return "homebox", nil }
	t.Cleanup(func() { Hostname = savedHost })
	fakeTailscale(t, tailscaleRunning)
	_, _, start := wizard(t)
	eng := webtest.New(http.NotFoundHandler())
	t.Cleanup(eng.Close)
	b := eng.Start(t)
	b.NavigateURL(t, start)

	b.WaitText(t, "How should Cog run?")
	b.Snapshot(t, webtest.State("run"))
	b.Submit(t, "form[action='/step/run']")
	b.WaitText(t, "Where do you want to keep your files?")
	b.Submit(t, "form[action='/step/storage']")

	b.WaitText(t, "Where will you use Cog from?")
	b.Click(t, "input[name=access][value=internet]")
	b.Snapshot(t, webtest.State("access"))
	b.Submit(t, "form[action='/step/access']")

	b.WaitText(t, "What is Cog's address on the internet?")
	b.Type(t, "#domain", "cloud.example.com")
	b.Submit(t, "form[action='/step/domain']")

	b.WaitText(t, "How should Let's Encrypt check the name is yours?")
	b.Click(t, "input[name=challenge][value=dns-01]")
	b.Select(t, "#dns_provider", "option[value=duckdns]")
	b.WaitVisible(t, ".when-dns-duckdns")
	b.Snapshot(t, webtest.State("certificate"))
	b.Type(t, "#dns_env\\.DUCKDNS_TOKEN", "token")
	b.Submit(t, "form[action='/step/certificate']")

	b.WaitText(t, "Do you want SFTP and SSH?")
	b.Submit(t, "form[action='/step/ssh']")
	b.WaitText(t, "How should Cog stay up to date?")
	b.Submit(t, "form[action='/step/updates']")

	b.WaitText(t, "Who manages Cog?")
	b.Type(t, "#admin_password", "correct horse battery")
	b.Type(t, "#admin_password_confirm", "correct horse battery")
	b.Submit(t, "form[action='/step/admin']")
	b.WaitText(t, "Ready to set up")
	// The domain check's answer depends on the network the test runs on.
	b.Click(t, "a[href='/step/access']")
	b.WaitText(t, "Where will you use Cog from?")
	b.Click(t, "input[name=access][value=lan]")
	b.Submit(t, "form[action='/step/access']")
	b.WaitText(t, "Encrypt connections on your network?")
	b.Click(t, "input[name=lan_https][value='1']")
	b.Submit(t, "form[action='/step/network']")
	b.WaitText(t, "Ready to set up")
	b.Snapshot(t, webtest.State("review"))
}
