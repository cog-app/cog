package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SelfSigned is the TLS mode in which the server makes and signs its own
// certificate: HTTPS on a home network, where no CA can check a name.
// Browsers warn about it until it is trusted on each device, and its
// fingerprint is how a person tells it is theirs.
const SelfSigned = "self-signed"

// selfSignedLifetime is how long a self-signed certificate is valid:
// browsers refuse a server certificate valid for longer than 398 days
// even when a person has trusted it.
const selfSignedLifetime = 397 * 24 * time.Hour

// DefaultSelfSignedNames are the names a self-signed certificate covers
// when the configuration gives none: this computer's name, its .local
// name (mDNS), and localhost.
func DefaultSelfSignedNames() []string {
	names := []string{"localhost", "127.0.0.1"}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return names
	}
	host = strings.ToLower(strings.TrimSuffix(host, ".local"))
	return append([]string{host, host + ".local"}, names...)
}

// ensureSelfSigned loads the stored self-signed certificate and makes a new
// one when there is none, it does not cover the configured names, or it is
// near expiry.
func (m *Manager) ensureSelfSigned(now time.Time) error {
	certPath, keyPath := m.selfPaths()
	if !m.Ready() {
		if err := m.loadPair(certPath, keyPath, true); err != nil {
			return err
		}
	}
	if !m.NeedsRenewal(now) {
		return nil
	}
	certPEM, keyPEM, err := selfSign(m.cfg.Domains, now)
	if err != nil {
		return err
	}
	cert, err := parsePair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.selfDir, 0o700); err != nil {
		return fmt.Errorf("certificate directory: %w", err)
	}
	if err := writeFile(keyPath, keyPEM); err != nil {
		return err
	}
	if err := writeFile(certPath, certPEM); err != nil {
		return err
	}
	m.set(cert)
	m.log.Info("made a self-signed certificate", "names", m.cfg.Domains, "sha256", Fingerprint(cert.Leaf))
	return nil
}

func (m *Manager) selfPaths() (cert, key string) {
	base := filepath.Join(m.selfDir, "self-signed")
	return base + ".crt", base + ".key"
}

// selfSign makes a certificate for names (host names and IP addresses),
// signed by its own key.
func selfSign(names []string, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("certificate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, fmt.Errorf("certificate serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: names[0], Organization: []string{"Cog (self-signed)"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(selfSignedLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("sign certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("certificate key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// Fingerprint is a certificate's SHA-256 fingerprint as browsers show it:
// colon-separated upper-case hex.
func Fingerprint(c *x509.Certificate) string {
	if c == nil {
		return ""
	}
	sum := sha256.Sum256(c.Raw)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// Fingerprint is the SHA-256 fingerprint of the certificate in use, empty
// without one.
func (m *Manager) Fingerprint() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cert == nil {
		return ""
	}
	return Fingerprint(m.cert.Leaf)
}
