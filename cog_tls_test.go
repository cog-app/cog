package cog

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/certs"
	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// TestServeHTTPSAndRedirect: the HTTPS listener serves the site with the
// configured certificate, the plain one sends visitors to the HTTPS
// address, and both stop when the server is asked to.
func TestServeHTTPSAndRedirect(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	writeTestCert(t, certPath, keyPath)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := certs.New(config.TLS{Mode: "files", CertFile: certPath, KeyFile: keyPath}, dir, log)
	require.NoError(t, err)

	site := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "cog") })
	httpsAddr, httpAddr := freeAddr(t), freeAddr(t)
	secure := newServer(httpsAddr, site)
	secure.TLSConfig = m.TLSConfig()
	servers := []listener{{srv: secure, tls: true}, {srv: newServer(httpAddr, m.HTTPHandler(redirectToHTTPS("https://cloud.example.test")))}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, servers, log) }()

	client := &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var res *http.Response
	for range 50 { // until the listeners are up
		if res, err = client.Get("https://" + httpsAddr + "/"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, err)
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, "cog", string(body))
	require.Equal(t, "cloud.example.test", res.TLS.PeerCertificates[0].Subject.CommonName)

	res, err = client.Get("http://" + httpAddr + "/files?dir=/a")
	require.NoError(t, err)
	require.Equal(t, http.StatusPermanentRedirect, res.StatusCode)
	require.Equal(t, "https://cloud.example.test/files?dir=/a", res.Header.Get("Location"))

	cancel()
	require.NoError(t, <-done)
}

// TestLoadConfigLayers: the file overrides the defaults and the
// environment overrides the file.
func TestLoadConfigLayers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.FileName)
	require.NoError(t, os.WriteFile(path, []byte("addr = \":9000\"\nlog_level = \"debug\"\n[tls]\nmode = \"acme\"\ndomains = [\"cloud.example.test\"]\n"), 0o600))
	t.Setenv("COG_CONFIG", path)
	t.Setenv("COG_ADDR", ":9100")
	cfg, err := loadConfig()
	require.NoError(t, err)
	require.Equal(t, ":9100", cfg.Addr)
	require.Equal(t, "debug", cfg.LogLevel)
	require.Equal(t, filepath.Join(dir, "system.db"), cfg.DBPath, "default, from the file's folder")
	require.Equal(t, "https://cloud.example.test", cfg.BaseURL)
}

func writeTestCert(t *testing.T, certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "cloud.example.test"},
		DNSNames: []string{"cloud.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
}
