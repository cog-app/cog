package setup

import (
	"bytes"
	"context"
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
		{"same port twice", func(p *Plan) { p.SSHPort = p.Port }, "port"},
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
			require.Equal(t, tc.field, problemOf(err).Field, err.Error())
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

	res, err := client.Get(base + "/plan")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, res.StatusCode, "no token yet")

	res, err = client.Get(base + "/start?token=wrong")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, res.StatusCode)

	res, err = client.Get(start)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, res.StatusCode)
	res, err = client.Get(base + "/plan")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)

	// The stylesheet the pages link, without the cookie: a browser fetches
	// it like any other.
	res, err = http.Get(base + "/static/style.css")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)

	// DNS rebinding: a page on another name that resolves here.
	req, err := http.NewRequest(http.MethodGet, base+"/plan", nil)
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

// TestWizardFlow: the form's answers become a plan, a problem sends the
// person back to the field, and an apply runs it.
func TestWizardFlow(t *testing.T) {
	w, client, start := wizard(t)
	base := "http://" + w.addr
	_, err := client.Get(start)
	require.NoError(t, err)
	post := func(path string, form url.Values) *http.Response {
		req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(form.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", base)
		res, err := client.Do(req)
		require.NoError(t, err)
		return res
	}
	form := url.Values{
		"scope": {"user"}, "dir": {w.draft.Dir}, "admin_user": {"admin"},
		"admin_password": {"correct horse"}, "admin_password_confirm": {"different"},
		"access": {"local"}, "port": {strconv.Itoa(freePort(t))}, "ssh": {"1"}, "ssh_port": {"8522"},
	}
	res := post("/plan", form)
	require.Equal(t, "/plan#admin_password_confirm", res.Header.Get("Location"))
	page, err := client.Get(base + "/plan")
	require.NoError(t, err)
	body, err := io.ReadAll(page.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "The passwords do not match.")
	require.NotContains(t, string(body), "correct horse", "a password is not shown again")

	form.Set("admin_password_confirm", "correct horse")
	res = post("/plan", form)
	require.Equal(t, "/review", res.Header.Get("Location"))
	page, err = client.Get(base + "/review")
	require.NoError(t, err)
	body, err = io.ReadAll(page.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "Ready to set up")
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
