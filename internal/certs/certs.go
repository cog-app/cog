// Package certs serves the server's own TLS certificate: from files an
// admin manages, or from an ACME CA (Let's Encrypt by default) through lego,
// renewed while the server runs.
//
// Challenges are answered by the server's own listeners rather than by
// servers lego starts: tls-alpn-01 by the HTTPS listener's GetCertificate,
// http-01 by the plain listener's handler. So a home server behind a
// router that forwards 443 to :8443 proves its name without a privileged
// port, and renewal never needs the port the server already holds.
package certs

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/challenge/tlsalpn01"
	"github.com/go-acme/lego/v4/lego"
	legolog "github.com/go-acme/lego/v4/log"
	"github.com/go-acme/lego/v4/registration"

	"git.duckfam.us/jj-software/cog/internal/config"
)

// Challenge types, as config.TLS.Challenge names them.
const (
	TLSALPN01 = "tls-alpn-01"
	HTTP01    = "http-01"
	DNS01     = "dns-01"
)

// StagingCA is Let's Encrypt's staging directory: what config.TLS.CA
// "staging" means. Its certificates are not trusted by browsers, and its
// limits are generous, so it is for trying a setup out.
const StagingCA = lego.LEDirectoryStaging

// renewBefore is how long before expiry a certificate is renewed. Let's
// Encrypt's last 90 days; renewing with a third left gives weeks of retries.
const renewBefore = 30 * 24 * time.Hour

// Manager holds the server's certificate and answers ACME challenges.
type Manager struct {
	cfg config.TLS
	dir string // where the account and certificates are kept
	log *slog.Logger

	mu   sync.RWMutex
	cert *tls.Certificate

	alpn  sync.Map // domain -> *tls.Certificate for tls-alpn-01
	token sync.Map // token -> key authorization for http-01

	// httpClient talks to the CA; nil is lego's default. Tests set it to
	// trust a test CA.
	httpClient *http.Client
}

// New returns the manager for cfg, keeping ACME state under dataDir/acme,
// with the certificate it already has loaded. A missing certificate is not
// an error: Run obtains it.
func New(cfg config.TLS, dataDir string, log *slog.Logger) (*Manager, error) {
	if cfg.Challenge == "" {
		cfg.Challenge = TLSALPN01
	}
	m := &Manager{cfg: cfg, dir: filepath.Join(dataDir, "acme"), log: log.With("component", "certs")}
	// lego logs through a package logger of its own; send it where the
	// server's log goes. There is one server, so one destination.
	legolog.Logger = slog.NewLogLogger(m.log.Handler(), slog.LevelDebug)
	switch cfg.Mode {
	case "files":
		if cfg.CertFile == "" || cfg.KeyFile == "" {
			return nil, errors.New("tls mode files needs cert_file and key_file")
		}
		if err := m.loadFiles(); err != nil {
			return nil, err
		}
	case "acme":
		if len(cfg.Domains) == 0 {
			return nil, errors.New("tls mode acme needs at least one domain")
		}
		if !slices.Contains([]string{TLSALPN01, HTTP01, DNS01}, cfg.Challenge) {
			return nil, fmt.Errorf("unknown ACME challenge %q", cfg.Challenge)
		}
		if cfg.Challenge == HTTP01 && cfg.HTTPAddr == "" {
			return nil, errors.New("the http-01 challenge needs http_addr, the listener port 80 is forwarded to")
		}
		if cfg.Challenge == DNS01 && cfg.DNSProvider == "" {
			return nil, errors.New("the dns-01 challenge needs dns_provider")
		}
		if err := m.loadStored(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown tls mode %q", cfg.Mode)
	}
	return m, nil
}

// TLSConfig is the HTTPS listener's configuration.
func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: m.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1", tlsalpn01.ACMETLS1Protocol},
	}
}

// GetCertificate answers a tls-alpn-01 validation with its challenge
// certificate and every other handshake with the server's certificate.
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if slices.Contains(hello.SupportedProtos, tlsalpn01.ACMETLS1Protocol) {
		if c, ok := m.alpn.Load(strings.ToLower(hello.ServerName)); ok {
			return c.(*tls.Certificate), nil
		}
		return nil, fmt.Errorf("no tls-alpn-01 challenge for %q", hello.ServerName)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cert == nil {
		return nil, errors.New("no certificate yet: see the log for why it could not be obtained")
	}
	return m.cert, nil
}

// Ready reports whether the manager has a certificate to serve.
func (m *Manager) Ready() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cert != nil
}

// HTTPHandler answers http-01 challenges and passes every other request
// to next.
func (m *Manager) HTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token, ok := strings.CutPrefix(r.URL.Path, http01.ChallengePath("")); ok && r.Method == http.MethodGet {
			if ka, ok := m.token.Load(token); ok {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte(ka.(string)))
				return
			}
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Run keeps the certificate current until ctx ends: files are reloaded
// when they change, and an ACME certificate is obtained when missing and
// renewed when it nears expiry. Failures are logged and retried.
func (m *Manager) Run(ctx context.Context) {
	for {
		wait := time.Hour
		switch m.cfg.Mode {
		case "files":
			if err := m.loadFiles(); err != nil {
				m.log.Error("reload certificate", "err", err)
			}
		case "acme":
			if m.NeedsRenewal(time.Now()) {
				if err := m.Obtain(ctx); err != nil {
					m.log.Error("obtain certificate", "domains", m.cfg.Domains, "err", err)
				} else {
					wait = 12 * time.Hour
				}
			} else {
				wait = 12 * time.Hour
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// NeedsRenewal reports whether the ACME certificate is missing, for other
// names than configured, or within renewBefore of expiring.
func (m *Manager) NeedsRenewal(now time.Time) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cert == nil || m.cert.Leaf == nil {
		return true
	}
	leaf := m.cert.Leaf
	for _, d := range m.cfg.Domains {
		if leaf.VerifyHostname(strings.TrimPrefix(d, "*.")) != nil && !slices.Contains(leaf.DNSNames, d) {
			return true
		}
	}
	return now.Add(renewBefore).After(leaf.NotAfter)
}

// Expiry is when the certificate in use expires, zero without one.
func (m *Manager) Expiry() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cert == nil || m.cert.Leaf == nil {
		return time.Time{}
	}
	return m.cert.Leaf.NotAfter
}

// Obtain gets a certificate for the configured names from the CA now and
// starts serving it. The challenge has to reach this process: the server's
// listeners, or ServeChallenges during setup.
func (m *Manager) Obtain(ctx context.Context) error {
	if m.cfg.Mode != "acme" {
		return fmt.Errorf("tls mode %q does not obtain certificates", m.cfg.Mode)
	}
	client, err := m.client()
	if err != nil {
		return err
	}
	type result struct {
		res *certificate.Resource
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := client.Certificate.Obtain(certificate.ObtainRequest{Domains: m.cfg.Domains, Bundle: true})
		done <- result{res, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		// lego cannot be cancelled; it gives up on its own timeouts.
		return ctx.Err()
	}
	if r.err != nil {
		return explain(m.cfg, r.err)
	}
	if err := m.store(r.res); err != nil {
		return err
	}
	m.log.Info("obtained certificate", "domains", m.cfg.Domains, "expires", m.Expiry())
	return nil
}

// explain adds what to check to a failed validation, which is nearly always
// a port the router does not forward or a name that points elsewhere.
func explain(cfg config.TLS, err error) error {
	var hint string
	switch cfg.Challenge {
	case TLSALPN01:
		hint = fmt.Sprintf("check that %s points at your public address and that your router forwards TCP port 443 to this computer's %s", strings.Join(cfg.Domains, ", "), cfg.Addr)
	case HTTP01:
		hint = fmt.Sprintf("check that %s points at your public address and that your router forwards TCP port 80 to this computer's %s", strings.Join(cfg.Domains, ", "), cfg.HTTPAddr)
	case DNS01:
		hint = fmt.Sprintf("check the %s credentials and that they may edit the zone of %s", cfg.DNSProvider, strings.Join(cfg.Domains, ", "))
	}
	return fmt.Errorf("%w (%s)", err, hint)
}

// ServeChallenges answers challenges on the configured listeners until the
// returned stop is called: for setup, which obtains the certificate before
// the server is running so that a mistake shows in the wizard.
func (m *Manager) ServeChallenges() (stop func() error, err error) {
	var servers []*http.Server
	stop = func() error {
		var errs []error
		for _, s := range servers {
			errs = append(errs, s.Close())
		}
		return errors.Join(errs...)
	}
	switch m.cfg.Challenge {
	case TLSALPN01:
		ln, err := tls.Listen("tcp", m.cfg.Addr, m.TLSConfig())
		if err != nil {
			return nil, fmt.Errorf("listen on %s for the tls-alpn-01 challenge: %w", m.cfg.Addr, err)
		}
		s := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, s)
		go func() { _ = s.Serve(ln) }() // ends with ErrServerClosed at stop
	case HTTP01:
		s := &http.Server{Addr: m.cfg.HTTPAddr, Handler: m.HTTPHandler(http.NotFoundHandler()), ReadHeaderTimeout: 10 * time.Second}
		ln, err := listen(s.Addr)
		if err != nil {
			return nil, fmt.Errorf("listen on %s for the http-01 challenge: %w", s.Addr, err)
		}
		servers = append(servers, s)
		go func() { _ = s.Serve(ln) }() // ends with ErrServerClosed at stop
	}
	return stop, nil
}

// client is a lego client for the stored account, registering one first
// when there is none.
func (m *Manager) client() (*lego.Client, error) {
	acct, err := m.account()
	if err != nil {
		return nil, err
	}
	lc := lego.NewConfig(acct)
	lc.UserAgent = "cog"
	lc.Certificate.KeyType = certcrypto.EC256
	if m.httpClient != nil {
		lc.HTTPClient = m.httpClient
	}
	switch m.cfg.CA {
	case "":
	case "staging":
		lc.CADirURL = StagingCA
	default:
		lc.CADirURL = m.cfg.CA
	}
	client, err := lego.NewClient(lc)
	if err != nil {
		return nil, fmt.Errorf("ACME client: %w", err)
	}
	switch m.cfg.Challenge {
	case TLSALPN01:
		err = client.Challenge.SetTLSALPN01Provider(alpnProvider{m})
	case HTTP01:
		err = client.Challenge.SetHTTP01Provider(httpProvider{m})
	case DNS01:
		prov, perr := newDNSProvider(m.cfg.DNSProvider, m.cfg.DNSEnv)
		if perr != nil {
			return nil, perr
		}
		err = client.Challenge.SetDNS01Provider(prov)
	}
	if err != nil {
		return nil, fmt.Errorf("ACME challenge: %w", err)
	}
	if acct.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, fmt.Errorf("register ACME account: %w", err)
		}
		acct.Registration = reg
		if err := m.saveAccount(acct); err != nil {
			return nil, err
		}
	}
	return client, nil
}

type alpnProvider struct{ m *Manager }

func (p alpnProvider) Present(domain, _, keyAuth string) error {
	cert, err := tlsalpn01.ChallengeCert(domain, keyAuth)
	if err != nil {
		return err
	}
	p.m.alpn.Store(strings.ToLower(domain), cert)
	return nil
}

func (p alpnProvider) CleanUp(domain, _, _ string) error {
	p.m.alpn.Delete(strings.ToLower(domain))
	return nil
}

type httpProvider struct{ m *Manager }

func (p httpProvider) Present(_, token, keyAuth string) error {
	p.m.token.Store(token, keyAuth)
	return nil
}

func (p httpProvider) CleanUp(_, token, _ string) error {
	p.m.token.Delete(token)
	return nil
}

// account is the ACME account, kept per CA and email under dir.
type account struct {
	Email        string                 `json:"email"`
	Registration *registration.Resource `json:"registration"`
	key          crypto.PrivateKey
}

func (a *account) GetEmail() string                        { return a.Email }
func (a *account) GetRegistration() *registration.Resource { return a.Registration }
func (a *account) GetPrivateKey() crypto.PrivateKey        { return a.key }

// accountDir separates accounts by CA, so trying staging first does not
// leave the production CA an account it never registered.
func (m *Manager) accountDir() string {
	ca := m.cfg.CA
	if ca == "" {
		ca = "production"
	}
	return filepath.Join(m.dir, "accounts", sanitize(ca))
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		return '_'
	}, s)
}

func (m *Manager) account() (*account, error) {
	dir := m.accountDir()
	keyPath := filepath.Join(dir, "account.key")
	acct := &account{Email: m.cfg.Email}
	keyPEM, err := os.ReadFile(keyPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		key, err := certcrypto.GeneratePrivateKey(certcrypto.EC256)
		if err != nil {
			return nil, fmt.Errorf("account key: %w", err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("account directory: %w", err)
		}
		if err := writeFile(keyPath, certcrypto.PEMEncode(key)); err != nil {
			return nil, err
		}
		acct.key = key
		return acct, nil
	case err != nil:
		return nil, fmt.Errorf("read account key: %w", err)
	}
	if acct.key, err = certcrypto.ParsePEMPrivateKey(keyPEM); err != nil {
		return nil, fmt.Errorf("parse account key: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "account.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return acct, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read account: %w", err)
	}
	var stored account
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("parse account: %w", err)
	}
	// A changed email registers again; the CA keys accounts by key, and
	// the old registration would keep sending notices to the old address.
	if stored.Email == acct.Email {
		acct.Registration = stored.Registration
	}
	return acct, nil
}

func (m *Manager) saveAccount(a *account) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("encode account: %w", err)
	}
	return writeFile(filepath.Join(m.accountDir(), "account.json"), data)
}

// certPaths are where the certificate for the configured names is kept.
func (m *Manager) certPaths() (cert, key string) {
	base := filepath.Join(m.dir, "certificates", sanitize(strings.ReplaceAll(m.cfg.Domains[0], "*", "_wildcard")))
	return base + ".crt", base + ".key"
}

func (m *Manager) store(res *certificate.Resource) error {
	certPath, keyPath := m.certPaths()
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return fmt.Errorf("certificate directory: %w", err)
	}
	cert, err := parsePair(res.Certificate, res.PrivateKey)
	if err != nil {
		return err
	}
	if err := writeFile(keyPath, res.PrivateKey); err != nil {
		return err
	}
	if err := writeFile(certPath, res.Certificate); err != nil {
		return err
	}
	m.set(cert)
	return nil
}

func (m *Manager) loadStored() error {
	certPath, keyPath := m.certPaths()
	return m.loadPair(certPath, keyPath, true)
}

func (m *Manager) loadFiles() error {
	return m.loadPair(m.cfg.CertFile, m.cfg.KeyFile, false)
}

func (m *Manager) loadPair(certPath, keyPath string, missingOK bool) error {
	certPEM, err := os.ReadFile(certPath)
	if missingOK && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read certificate key: %w", err)
	}
	cert, err := parsePair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("%s: %w", certPath, err)
	}
	m.set(cert)
	return nil
}

func (m *Manager) set(c *tls.Certificate) {
	m.mu.Lock()
	m.cert = c
	m.mu.Unlock()
}

func parsePair(certPEM, keyPEM []byte) (*tls.Certificate, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("certificate: %w", err)
	}
	if cert.Leaf == nil {
		block, _ := pem.Decode(certPEM)
		if block == nil {
			return nil, errors.New("certificate: no PEM block")
		}
		if cert.Leaf, err = x509.ParseCertificate(block.Bytes); err != nil {
			return nil, fmt.Errorf("certificate: %w", err)
		}
	}
	return &cert, nil
}

// writeFile replaces path with data, readable only by its owner.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", filepath.Base(path), err), os.Remove(tmp))
	}
	return nil
}

func listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
