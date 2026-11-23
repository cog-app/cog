package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/letsencrypt/pebble/v2/ca"
	"github.com/letsencrypt/pebble/v2/db"
	"github.com/letsencrypt/pebble/v2/va"
	"github.com/letsencrypt/pebble/v2/wfe"
	"github.com/miekg/dns"

	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// freePort is a TCP port nothing listens on at the moment.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// pebble starts Let's Encrypt's test CA in the process, validating on the
// given ports of 127.0.0.1, which a DNS server of its own gives as every
// name's address. It returns the directory URL and a client trusting it.
func pebble(t *testing.T, httpPort, tlsPort int) (string, *http.Client) {
	t.Helper()
	t.Setenv("PEBBLE_VA_NOSLEEP", "1")
	t.Setenv("PEBBLE_WFE_NONCEREJECT", "0")

	// Pebble asks over TCP.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	dnsSrv := &dns.Server{Listener: ln, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		for _, q := range r.Question {
			if q.Qtype == dns.TypeA {
				m.Answer = append(m.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(127, 0, 0, 1)})
			}
		}
		_ = w.WriteMsg(m) // a lost answer fails the test by timing out
	})}
	go func() { _ = dnsSrv.ActivateAndServe() }() // ends at Shutdown
	t.Cleanup(func() { _ = dnsSrv.Shutdown() })

	logger := log.New(io.Discard, "", 0)
	store := db.NewMemoryStore()
	authority := ca.New(logger, store, "", "ecdsa", 0, 1, map[string]ca.Profile{"default": {Description: "default"}})
	validator := va.New(logger, httpPort, tlsPort, false, ln.Addr().String(), store)
	front := wfe.New(logger, store, validator, authority, []string{"pebble.letsencrypt.org"}, false, false, 0, 0)
	srv := httptest.NewTLSServer(front.Handler())
	t.Cleanup(srv.Close)
	return srv.URL + "/dir", srv.Client()
}

// TestObtainWithTLSALPN: the HTTPS listener answers the tls-alpn-01
// challenge itself, so a router forwarding only 443 is enough, and the
// certificate it gets is the one it serves.
func TestObtainWithTLSALPN(t *testing.T) {
	tlsPort := freePort(t)
	dir, client := pebble(t, freePort(t), tlsPort)
	cfg := config.TLS{Mode: "acme", Addr: "127.0.0.1:" + strconv.Itoa(tlsPort), Domains: []string{"cloud.example.test"}, Challenge: TLSALPN01, CA: dir}
	m, err := New(cfg, t.TempDir(), quiet)
	require.NoError(t, err)
	m.httpClient = client
	require.True(t, m.NeedsRenewal(time.Now()))

	stop, err := m.ServeChallenges()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, m.Obtain(ctx))
	require.False(t, m.NeedsRenewal(time.Now()))

	cert, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "cloud.example.test"})
	require.NoError(t, err)
	require.NoError(t, cert.Leaf.VerifyHostname("cloud.example.test"))
}

// TestObtainWithHTTP01: the plain listener answers http-01, and a restart
// finds the certificate on disk instead of asking again.
func TestObtainWithHTTP01(t *testing.T) {
	httpPort := freePort(t)
	dir, client := pebble(t, httpPort, freePort(t))
	data := t.TempDir()
	cfg := config.TLS{Mode: "acme", HTTPAddr: "127.0.0.1:" + strconv.Itoa(httpPort), Domains: []string{"cloud.example.test"}, Challenge: HTTP01, CA: dir}
	m, err := New(cfg, data, quiet)
	require.NoError(t, err)
	m.httpClient = client
	stop, err := m.ServeChallenges()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, m.Obtain(ctx))
	require.NoError(t, stop())

	again, err := New(cfg, data, quiet)
	require.NoError(t, err)
	require.True(t, again.Ready())
	require.False(t, again.NeedsRenewal(time.Now()))
}

// TestHTTPHandlerPassesOtherRequests: the plain listener answers only
// challenges it holds and hands the rest on (to the HTTPS redirect).
func TestHTTPHandlerPassesOtherRequests(t *testing.T) {
	m := &Manager{}
	m.token.Store("tok", "tok.key")
	h := m.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/.well-known/acme-challenge/tok", http.StatusOK},
		{"/.well-known/acme-challenge/other", http.StatusNotFound},
		{"/login", http.StatusTeapot},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		require.Equal(t, tc.want, rec.Code, tc.path)
	}
}

// TestFilesModeReloads: certificates an admin manages are picked up when
// the files change, without a restart.
func TestFilesModeReloads(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	writeSelfSigned(t, certPath, keyPath, "one.example.test")
	m, err := New(config.TLS{Mode: "files", CertFile: certPath, KeyFile: keyPath}, dir, quiet)
	require.NoError(t, err)
	c, err := m.GetCertificate(&tls.ClientHelloInfo{})
	require.NoError(t, err)
	require.Equal(t, "one.example.test", c.Leaf.Subject.CommonName)

	writeSelfSigned(t, certPath, keyPath, "two.example.test")
	require.NoError(t, m.loadFiles())
	c, err = m.GetCertificate(&tls.ClientHelloInfo{})
	require.NoError(t, err)
	require.Equal(t, "two.example.test", c.Leaf.Subject.CommonName)
}

func TestNewRejectsIncompleteSettings(t *testing.T) {
	for _, cfg := range []config.TLS{
		{Mode: "acme"},
		{Mode: "acme", Domains: []string{"a.test"}, Challenge: HTTP01},
		{Mode: "acme", Domains: []string{"a.test"}, Challenge: DNS01},
		{Mode: "files"},
		{Mode: "bogus"},
	} {
		_, err := New(cfg, t.TempDir(), quiet)
		require.Error(t, err, cfg)
	}
}

func writeSelfSigned(t *testing.T, certPath, keyPath, name string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
}

func TestSelfSigned(t *testing.T) {
	data := t.TempDir()
	cfg := config.TLS{Mode: SelfSigned, Domains: []string{"cog.home.arpa", "192.168.1.20"}}
	m, err := New(cfg, data, quiet)
	require.NoError(t, err)
	require.True(t, m.Ready())
	c, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "cog.home.arpa"})
	require.NoError(t, err)
	require.NoError(t, c.Leaf.VerifyHostname("cog.home.arpa"))
	require.NoError(t, c.Leaf.VerifyHostname("192.168.1.20"))
	fp := m.Fingerprint()
	require.Equal(t, 95, len(fp))

	// The same certificate is served after a restart.
	m2, err := New(cfg, data, quiet)
	require.NoError(t, err)
	require.Equal(t, fp, m2.Fingerprint())

	// A new name makes a new one.
	cfg.Domains = append(cfg.Domains, "cog.local")
	m3, err := New(cfg, data, quiet)
	require.NoError(t, err)
	require.NotEqual(t, fp, m3.Fingerprint())
	require.False(t, m3.NeedsRenewal(time.Now()))
	require.True(t, m3.NeedsRenewal(time.Now().Add(380*24*time.Hour)))
}

// TestStoredPairIsOneFile: a certificate and its key are kept in one file,
// so that replacing them cannot leave a key that does not match; the
// separate files an earlier Cog kept are still read, and replaced.
func TestStoredPairIsOneFile(t *testing.T) {
	data := t.TempDir()
	base := filepath.Join(data, "tls", "self-signed")
	require.NoError(t, os.MkdirAll(filepath.Dir(base), 0o700))
	cfg := config.TLS{Mode: SelfSigned, Domains: []string{"cog.home.arpa"}}
	certPEM, keyPEM, err := selfSign(cfg.Domains, time.Now())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(base+".crt", certPEM, 0o600))
	require.NoError(t, os.WriteFile(base+".key", keyPEM, 0o600))
	old, err := parsePair(certPEM, keyPEM)
	require.NoError(t, err)

	m, err := New(cfg, data, quiet)
	require.NoError(t, err)
	require.Equal(t, Fingerprint(old.Leaf), m.Fingerprint(), "an earlier Cog's files")

	cfg.Domains = append(cfg.Domains, "cog.local")
	m, err = New(cfg, data, quiet)
	require.NoError(t, err)
	for _, ext := range []string{".crt", ".key"} {
		_, err := os.Stat(base + ext)
		require.True(t, errors.Is(err, fs.ErrNotExist), ext+" replaced")
	}
	pemData, err := os.ReadFile(base + ".pem")
	require.NoError(t, err)
	both, err := parsePair(pemData, pemData)
	require.NoError(t, err)
	require.Equal(t, m.Fingerprint(), Fingerprint(both.Leaf))
}
