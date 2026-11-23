package setup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/certs"
	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/service"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func newPlan(t *testing.T) Plan {
	t.Helper()
	p, err := DefaultPlan(service.User, "/does/not/matter")
	require.NoError(t, err)
	p.Dir = t.TempDir()
	p.AdminPassword = "correct horse"
	return p
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*Plan)
		field string
	}{
		{"defaults", func(*Plan) {}, ""},
		{"relative folder", func(p *Plan) { p.Dir = "cog" }, "dir"},
		{"short password", func(p *Plan) { p.AdminPassword = "short" }, "admin_password"},
		{"no admin", func(p *Plan) { p.AdminUser = "" }, "admin_user"},
		{"internet without a name", func(p *Plan) { p.Access = AccessInternet }, "domain"},
		{"internet", func(p *Plan) { p.Access, p.Domain = AccessInternet, "Cloud.Example.com" }, ""},
		{"wildcard without DNS", func(p *Plan) { p.Access, p.Domain = AccessInternet, "*.example.com" }, "domain"},
		{"DNS without credentials", func(p *Plan) {
			p.Access, p.Domain, p.Challenge, p.DNSProvider = AccessInternet, "*.example.com", certs.DNS01, "duckdns"
		}, "dns_env.DUCKDNS_TOKEN"},
		{"DNS", func(p *Plan) {
			p.Access, p.Domain, p.Challenge, p.DNSProvider = AccessInternet, "*.example.com", certs.DNS01, "duckdns"
			p.DNSEnv = map[string]string{"DUCKDNS_TOKEN": "t"}
		}, ""},
		{"same port twice", func(p *Plan) { p.SSHPort = p.Port }, "ssh_port"},
		{"tailscale not connected", func(p *Plan) { p.Access = AccessTailscale }, "tailscale"},
		{"tailscale", func(p *Plan) { p.Access, p.TailscaleName = AccessTailscale, "box.tail1234.ts.net" }, ""},
		{"self-signed on the plain port", func(p *Plan) { p.Access, p.LANHTTPS, p.HTTPSPort = AccessLAN, true, p.Port }, "https_port"},
		{"proxy over http", func(p *Plan) { p.Access, p.PublicURL = AccessProxy, "http://cloud.example.com" }, "public_url"},
		{"proxy range", func(p *Plan) {
			p.Access, p.PublicURL, p.TrustedProxies = AccessProxy, "https://cloud.example.com", "10.0.0.1"
		}, "trusted_proxies"},
		{"firewall as a user", func(p *Plan) { p.Firewall = true }, "firewall"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPlan(t)
			tc.edit(&p)
			err := p.Validate()
			if tc.field == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var prob Problem
			require.True(t, errors.As(err, &prob), err.Error())
			require.Equal(t, tc.field, prob.Field, err.Error())
		})
	}
}

// TestConfigForInternet: a home server behind a router: HTTPS on 8443 for
// the router's 443, plain HTTP left on this computer only, SSH by key.
func TestConfigForInternet(t *testing.T) {
	p := newPlan(t)
	p.Access, p.Domain, p.Email = AccessInternet, "cloud.example.com", "me@example.com"
	require.NoError(t, p.Validate())
	cfg := p.Config(config.Defaults())
	require.Equal(t, "127.0.0.1:8580", cfg.Addr)
	require.Equal(t, "https://cloud.example.com", cfg.BaseURL)
	require.Equal(t, "acme", cfg.TLS.Mode)
	require.Equal(t, ":8443", cfg.TLS.Addr)
	require.Equal(t, "", cfg.TLS.HTTPAddr, "tls-alpn-01 needs no plain listener")
	require.Equal(t, certs.TLSALPN01, cfg.TLS.Challenge)
	require.Equal(t, filepath.Join(p.Dir, "data"), cfg.DataDir)
	require.Equal(t, "user", cfg.Install.Service)

	fw := p.Forwards()
	require.Equal(t, 443, fw[0].Public)
	require.Equal(t, 8443, fw[0].Local)
}

// TestConfigForLANHTTPS: a certificate Cog makes for its names and
// address on the network, and plain HTTP kept to this computer.
func TestConfigForLANHTTPS(t *testing.T) {
	saved := LANAddress
	LANAddress = func() string { return "192.168.1.20" }
	t.Cleanup(func() { LANAddress = saved })
	p := newPlan(t)
	p.Access, p.LANHTTPS = AccessLAN, true
	require.NoError(t, p.Validate())
	cfg := p.Config(config.Defaults())
	require.Equal(t, "127.0.0.1:8580", cfg.Addr)
	require.Equal(t, "https://192.168.1.20:8443", cfg.BaseURL)
	require.Equal(t, certs.SelfSigned, cfg.TLS.Mode)
	require.Equal(t, "192.168.1.20", cfg.TLS.Domains[0])
	require.Equal(t, 0, len(p.Forwards()))
}

// TestConfigForTailscale: Cog on loopback only, trusting tailscale serve
// in front of it, at the tailnet name.
func TestConfigForTailscale(t *testing.T) {
	p := newPlan(t)
	p.Access, p.TailscaleName = AccessTailscale, "box.tail1234.ts.net"
	require.NoError(t, p.Validate())
	cfg := p.Config(config.Defaults())
	require.Equal(t, "127.0.0.1:8580", cfg.Addr)
	require.Equal(t, "https://box.tail1234.ts.net", cfg.BaseURL)
	require.Equal(t, "127.0.0.1/32,::1/128", cfg.TrustedProxies)
	require.Equal(t, TailscaleServe, cfg.Install.Tailscale)
	require.False(t, cfg.TLS.Enabled())
	require.Equal(t, 0, len(p.reachablePorts())-boolInt(p.SSH))
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// fakeTailscale records the tailscale commands run and answers status
// with st.
func fakeTailscale(t *testing.T, status string) *[]string {
	t.Helper()
	var calls []string
	saved := tailscaleCommand
	t.Cleanup(func() { tailscaleCommand = saved })
	tailscaleCommand = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "status" {
			if status == "" {
				return nil, errNoTailscale
			}
			return []byte(status), nil
		}
		calls = append(calls, strings.Join(args, " "))
		return nil, nil
	}
	return &calls
}

const tailscaleRunning = `{"BackendState":"Running","Self":{"DNSName":"box.tail1234.ts.net."},"CertDomains":["box.tail1234.ts.net"]}`

func TestCheckTailscale(t *testing.T) {
	fakeTailscale(t, "")
	require.Contains(t, CheckTailscale(context.Background()).Problem(), "not installed")

	fakeTailscale(t, `{"BackendState":"Running","Self":{"DNSName":"box.tail1234.ts.net."}}`)
	require.Contains(t, CheckTailscale(context.Background()).Problem(), "HTTPS Certificates")

	fakeTailscale(t, `{"BackendState":"NeedsLogin","Self":{"DNSName":""}}`)
	require.Contains(t, CheckTailscale(context.Background()).Problem(), "sign in")

	fakeTailscale(t, tailscaleRunning)
	st := CheckTailscale(context.Background())
	require.Equal(t, "", st.Problem())
	require.Equal(t, "box.tail1234.ts.net", st.Name)
}

// TestApplyTailscale: serve points the tailnet name at Cog; a change to
// Funnel turns serve off first; leaving Tailscale turns it off.
func TestApplyTailscale(t *testing.T) {
	calls := fakeTailscale(t, tailscaleRunning)
	p := newPlan(t)
	p.Access, p.TailscaleName = AccessTailscale, "box.tail1234.ts.net"
	require.NoError(t, applyTailscale(context.Background(), p, io.Discard))
	require.Equal(t, []string{"serve --bg --https=443 http://127.0.0.1:8580"}, *calls)

	*calls = nil
	p.PrevTailscale, p.Funnel = TailscaleServe, true
	require.NoError(t, applyTailscale(context.Background(), p, io.Discard))
	require.Equal(t, []string{"serve --https=443 off", "funnel --bg --https=443 http://127.0.0.1:8580"}, *calls)

	*calls = nil
	p.PrevTailscale, p.Access = TailscaleFunnel, AccessLocal
	require.NoError(t, applyTailscale(context.Background(), p, io.Discard))
	require.Equal(t, []string{"funnel --https=443 off"}, *calls)
}

// TestFromConfigRoundTrip: running setup again starts from what the last
// run chose.
func TestFromConfigRoundTrip(t *testing.T) {
	for _, edit := range []func(*Plan){
		func(p *Plan) { p.Access = AccessLocal },
		func(p *Plan) { p.Access = AccessLAN; p.Port = 9000 },
		func(p *Plan) {
			p.Access, p.Domain, p.Challenge, p.HTTPPort = AccessInternet, "cloud.example.com", certs.HTTP01, 8081
			p.SSHAccountPasswords, p.UpdateAuto, p.Staging = false, true, true
		},
		func(p *Plan) {
			p.Access, p.PublicURL, p.TrustedProxies = AccessProxy, "https://cloud.example.com", "127.0.0.1/32"
		},
		func(p *Plan) { p.Access, p.LANHTTPS, p.HTTPSPort = AccessLAN, true, 9443 },
		func(p *Plan) { p.Access, p.TailscaleName, p.Funnel = AccessTailscale, "box.tail1234.ts.net", true },
	} {
		p := newPlan(t)
		edit(&p)
		require.NoError(t, p.Validate())
		cfg := p.Config(config.Defaults())
		cfg.File = filepath.Join(p.Dir, config.FileName)
		got, err := FromConfig(cfg, p.Source)
		require.NoError(t, err)
		require.Equal(t, ModeReconfig, got.Mode)
		got.Mode, got.AdminUser, got.AdminPassword = p.Mode, p.AdminUser, p.AdminPassword
		got.DNSEnv, p.DNSEnv = nil, nil
		if p.Access == AccessTailscale {
			require.Equal(t, TailscaleFunnel, got.PrevTailscale, "what to turn off when it changes")
			got.PrevTailscale = ""
		}
		require.Equal(t, p, got)
	}
}

// fakeOps stands in for the operating system: Install starts a server at
// the plan's port, as the real service would.
type fakeOps struct {
	mu       sync.Mutex
	calls    []string
	finished []FinishRequest
	srv      *http.Server
}

func (f *fakeOps) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func useFakeOps(t *testing.T, port int) *fakeOps {
	t.Helper()
	f := &fakeOps{}
	saved := ops
	t.Cleanup(func() {
		ops = saved
		if f.srv != nil {
			f.srv.Close()
		}
	})
	ops.IsAdmin = func() bool { return true }
	ops.Query = func(service.Spec) (service.Status, error) { return service.Status{}, nil }
	ops.Stop = func(service.Spec) error { f.record("stop"); return nil }
	ops.Uninstall = func(service.Spec) error { f.record("uninstall"); return nil }
	ops.CreateAccount = func(name, home string) error { f.record("account " + name); return nil }
	ops.GiveDir = func(dir, account string) error { f.record("give " + account); return nil }
	ops.OpenFirewall = func(ports []service.Port) error { f.record("firewall " + portList(ports)); return nil }
	ops.Finish = func(_ context.Context, _ Plan, req FinishRequest, _ io.Writer) error {
		f.mu.Lock()
		f.finished = append(f.finished, req)
		f.mu.Unlock()
		return nil
	}
	ops.Install = func(s service.Spec) error {
		f.record("install " + string(s.Scope))
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			return err
		}
		f.srv = &http.Server{Handler: http.NotFoundHandler()}
		go f.srv.Serve(ln)
		return nil
	}
	return f
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// TestApplyInstalls: an install copies the binary into the folder, writes
// the configuration, makes the admin and starts the service; a second run
// keeps settings the wizard does not ask about.
func TestApplyInstalls(t *testing.T) {
	p := newPlan(t)
	p.Port = freePort(t)
	f := useFakeOps(t, p.Port)
	src := filepath.Join(t.TempDir(), "cog-download")
	require.NoError(t, os.WriteFile(src, []byte("binary"), 0o755))
	p.Source = src

	var log bytes.Buffer
	res, err := Apply(context.Background(), p, &log)
	require.NoError(t, err, log.String())
	require.Equal(t, 0, len(res.Warnings), strings.Join(res.Warnings, "; "))
	require.Equal(t, "http://localhost:"+strconv.Itoa(p.Port), res.URL)

	l := p.Layout()
	bin, err := os.ReadFile(l.Bin)
	require.NoError(t, err)
	require.Equal(t, "binary", string(bin))
	cfg, err := config.Load(l.Config)
	require.NoError(t, err)
	require.Equal(t, l.DB, cfg.DBPath)
	require.Equal(t, []string{"install user"}, f.calls)
	require.Equal(t, "admin", f.finished[0].AdminUser)

	// An admin's own setting survives setup.
	cfg.FederationPeers = "peer.example.com"
	require.NoError(t, config.Save(l.Config, cfg))
	f.srv.Close()
	p.Mode, p.AdminUser, p.AdminPassword = ModeReconfig, "", ""
	_, err = Apply(context.Background(), p, &log)
	require.NoError(t, err, log.String())
	cfg, err = config.Load(l.Config)
	require.NoError(t, err)
	require.Equal(t, "peer.example.com", cfg.FederationPeers)
}

// TestApplySystemService: a system service gets an account of its own,
// which is given the folder, and only the ports others reach are opened.
func TestApplySystemService(t *testing.T) {
	p := newPlan(t)
	p.Scope, p.Port, p.Firewall, p.Access = service.System, freePort(t), true, AccessLAN
	f := useFakeOps(t, p.Port)
	p.Source = filepath.Join(t.TempDir(), "cog")
	require.NoError(t, os.WriteFile(p.Source, nil, 0o755))
	var log bytes.Buffer
	_, err := Apply(context.Background(), p, &log)
	require.NoError(t, err, log.String())
	want := []string{
		"account " + service.DefaultAccount,
		"give " + service.DefaultAccount,
		"firewall " + strconv.Itoa(p.Port) + " (web (HTTP)), 8522 (SSH)",
		"install system",
	}
	require.Equal(t, want, f.calls)
}

// TestRunNeedsElevationForSystem: without administrator rights a system
// plan goes to an elevated process, which is refused here.
func TestApplyRefusesSystemWithoutAdmin(t *testing.T) {
	p := newPlan(t)
	p.Scope = service.System
	useFakeOps(t, freePort(t))
	ops.IsAdmin = func() bool { return false }
	_, err := Apply(context.Background(), p, io.Discard)
	require.Error(t, err)
}

// wizard is a Wizard serving on a test server at the address it expects.
func wizard(t *testing.T) (*Wizard, *http.Client, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	useFakeOps(t, freePort(t))
	w := &Wizard{Exe: filepath.Join(t.TempDir(), "cog")}
	ln, start, err := w.Listen()
	require.NoError(t, err)
	srv := &http.Server{Handler: w.Handler()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	jar := &cookieJar{}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return w, client, start
}

type cookieJar struct {
	mu      sync.Mutex
	cookies []*http.Cookie
}

func (j *cookieJar) SetCookies(_ *url.URL, cs []*http.Cookie) {
	j.mu.Lock()
	j.cookies = append(j.cookies, cs...)
	j.mu.Unlock()
}

func (j *cookieJar) Cookies(*url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cookies
}

// TestWizardGuard: only the address setup opened gets in, only at the
// loopback address it listens on, and forms only from its own pages.
func TestWizardGuard(t *testing.T) {
	w, client, start := wizard(t)
	base := "http://" + w.addr

	res, err := client.Get(base + "/step/run")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, res.StatusCode, "no token yet")

	res, err = client.Get(base + "/start?token=wrong")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, res.StatusCode)

	res, err = client.Get(start)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, res.StatusCode)
	res, err = client.Get(base + "/step/run")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)

	// The stylesheet the pages link, without the cookie: a browser fetches
	// it like any other.
	res, err = http.Get(base + "/static/style.css")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)

	// DNS rebinding: a page on another name that resolves here.
	req, err := http.NewRequest(http.MethodGet, base+"/step/run", nil)
	require.NoError(t, err)
	req.Host = "evil.example.com"
	res, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusMisdirectedRequest, res.StatusCode)

	// A form posted from another page, even with the cookie.
	req, err = http.NewRequest(http.MethodPost, base+"/apply", nil)
	require.NoError(t, err)
	req.Header.Set("Origin", "http://evil.example.com")
	res, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, res.StatusCode)
}

// TestWizardGuided: one question a page, the pages a plan needs and no
// others, a problem kept on its page, and a change from the review
// asking the questions the change brings.
func TestWizardGuided(t *testing.T) {
	fakeTailscale(t, tailscaleRunning)
	w, client, start := wizard(t)
	base := "http://" + w.addr
	res, err := client.Get(start)
	require.NoError(t, err)
	res, err = client.Get(base + "/")
	require.NoError(t, err)
	require.Equal(t, "/step/run", res.Header.Get("Location"))
	post := func(path string, form url.Values) string {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(form.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", base)
		res, err := client.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusSeeOther, res.StatusCode)
		return res.Header.Get("Location")
	}
	get := func(path string) string {
		t.Helper()
		res, err := client.Get(base + path)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, res.StatusCode, path)
		body, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		return string(body)
	}

	require.Contains(t, get("/step/run"), "Step 1 of 6", "only this computer: no detail page")
	require.Equal(t, "/step/storage", post("/step/run", url.Values{"scope": {"user"}}))
	require.Equal(t, "/step/storage", post("/step/storage", url.Values{"dir": {"relative"}}))
	require.Contains(t, get("/step/storage"), "must be a full path")
	require.Equal(t, "/step/access", post("/step/storage", url.Values{"dir": {t.TempDir()}}))
	require.Equal(t, "/step/tailscale", post("/step/access", url.Values{"access": {"tailscale"}}))
	page := get("/step/tailscale")
	require.Contains(t, page, "Step 4 of 7")
	require.Contains(t, page, "https://box.tail1234.ts.net")
	require.Equal(t, "/step/ssh", post("/step/tailscale", url.Values{"funnel": {"1"}}))
	require.Equal(t, "/step/updates", post("/step/ssh", url.Values{"ssh": {"1"}, "ssh_port": {"8522"}}))
	require.Equal(t, "/step/admin", post("/step/updates", url.Values{"update_check": {"1"}}))
	// A problem only this page can find keeps the person here.
	require.Equal(t, "/step/admin", post("/step/admin", url.Values{
		"admin_user": {"admin"}, "admin_password": {"correct horse battery"}, "admin_password_confirm": {"other"},
	}))
	page = get("/step/admin")
	require.Contains(t, page, "The passwords do not match.")
	require.NotContains(t, page, "correct horse battery")
	require.Equal(t, "/review", post("/step/admin", url.Values{
		"admin_user": {"admin"}, "admin_password": {"correct horse battery"}, "admin_password_confirm": {"correct horse battery"},
	}))
	page = get("/review")
	require.Contains(t, page, "Ready to set up")
	require.Contains(t, page, "anyone through Tailscale Funnel")

	// From the review, a new way in asks its own questions, then returns.
	require.Equal(t, "/step/domain", post("/step/access", url.Values{"access": {"internet"}}))
	require.Equal(t, "/step/domain", post("/step/domain", url.Values{"domain": {"not a name"}}))
	require.Equal(t, "/review", post("/step/domain", url.Values{"domain": {"cloud.example.com"}}))
	require.Contains(t, get("/review"), "your router")
	require.Equal(t, "/step/network", post("/step/access", url.Values{"access": {"lan"}}))
	require.Equal(t, "/review", post("/step/network", url.Values{"lan_https": {"1"}, "https_port": {"8443"}}))
	require.Contains(t, get("/review"), "with a certificate Cog makes")
}

// TestCLIStopsAtEndOfInput: a script that runs out of answers installs
// nothing on defaults.
func TestCLIStopsAtEndOfInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	f := useFakeOps(t, freePort(t))
	in, err := os.CreateTemp(t.TempDir(), "in")
	require.NoError(t, err)
	_, err = in.WriteString("1\n\nadmin\n")
	require.NoError(t, err)
	_, err = in.Seek(0, io.SeekStart)
	require.NoError(t, err)
	c := &CLI{Exe: filepath.Join(t.TempDir(), "cog"), In: in, Out: io.Discard}
	err = c.Run(context.Background())
	require.Error(t, err)
	require.Equal(t, 0, len(f.calls))
}

// TestCLIInstalls: the same answers a person would type.
func TestCLIInstalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	port := freePort(t)
	f := useFakeOps(t, port)
	src := filepath.Join(t.TempDir(), "cog")
	require.NoError(t, os.WriteFile(src, nil, 0o755))
	answers := strings.Join([]string{
		"1",                // for me
		"",                 // default folder
		"",                 // admin
		"correct horse",    // password
		"correct horse",    // again
		"1",                // only this computer
		strconv.Itoa(port), // port
		"n",                // no SSH
		"y",                // check for updates
		"n",                // not automatically
		"y",                // go ahead
	}, "\n") + "\n"
	in, err := os.CreateTemp(t.TempDir(), "in")
	require.NoError(t, err)
	_, err = in.WriteString(answers)
	require.NoError(t, err)
	_, err = in.Seek(0, io.SeekStart)
	require.NoError(t, err)
	var out bytes.Buffer
	c := &CLI{Exe: src, In: in, Out: &out}
	require.NoError(t, c.Run(context.Background()), out.String())
	require.Equal(t, []string{"install user"}, f.calls)
	cfg, err := config.Load(filepath.Join(dataHome, "cog", config.FileName))
	require.NoError(t, err)
	require.False(t, cfg.SSHEnabled)
	require.Equal(t, "127.0.0.1:"+strconv.Itoa(port), cfg.Addr)
}
