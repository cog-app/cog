// Package safehttp is an HTTP client for URLs that users or remote servers
// supply: it dials only public addresses, checked after resolution and pinned
// to the address checked, so neither a hostile URL nor DNS rebinding reaches
// loopback, private networks or cloud metadata services.
package safehttp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"time"
)

// blockedPrefixes are address ranges the client refuses to dial,
// to prevent SSRF into internal networks, loopback, and cloud metadata services.
var blockedPrefixes = mustPrefixes(
	// IPv4
	"0.0.0.0/8",          // current network
	"10.0.0.0/8",         // RFC 1918
	"100.64.0.0/10",      // CGNAT
	"127.0.0.0/8",        // loopback
	"169.254.0.0/16",     // link-local + AWS/GCP metadata
	"172.16.0.0/12",      // RFC 1918
	"192.0.0.0/24",       // IETF reserved
	"192.0.2.0/24",       // TEST-NET-1
	"192.168.0.0/16",     // RFC 1918
	"198.18.0.0/15",      // benchmarking
	"198.51.100.0/24",    // TEST-NET-2
	"203.0.113.0/24",     // TEST-NET-3
	"224.0.0.0/4",        // multicast
	"240.0.0.0/4",        // reserved
	"255.255.255.255/32", // broadcast
	// IPv6
	"::/128",        // unspecified
	"::1/128",       // loopback
	"::ffff:0:0/96", // IPv4-mapped (defense-in-depth; we re-check after Is4In6)
	"64:ff9b::/96",  // NAT64
	"100::/64",      // discard prefix
	"fc00::/7",      // ULA
	"fe80::/10",     // link-local
	"ff00::/8",      // multicast
)

// allowPrivate, when true, permits dialing private/loopback/link-local IPs.
// Set via SetAllowPrivate for integration tests against httptest servers.
var allowPrivate atomic.Bool

// SetAllowPrivate enables dialing into normally-blocked ranges for every
// client in the process. Intended for tests only; must not be true in
// production. Prefer Options.Allow, which reaches only one client.
func SetAllowPrivate(b bool) { allowPrivate.Store(b) }

// AllowPrivate reports whether SetAllowPrivate is on.
func AllowPrivate() bool { return allowPrivate.Load() }

func mustPrefixes(specs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(specs))
	for _, s := range specs {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			panic("safehttp: bad prefix " + s + ": " + err.Error())
		}
		out = append(out, p)
	}
	return out
}

// ErrBlockedIP is returned by the safe dialer when the resolved address is in
// a blocked range.
var ErrBlockedIP = errors.New("safehttp: blocked IP")

// addressBlocked reports whether ip is in a blocked range and not in allow.
func addressBlocked(ip netip.Addr, allow []netip.Prefix) bool {
	if allowPrivate.Load() {
		return false
	}
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	for _, p := range allow {
		if p.Contains(ip) {
			return false
		}
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// DialContext resolves and dials only if the resolved IP is not in a
// blocked range. The dial target is pinned to the IP that was validated, so
// DNS rebinding between lookup and dial is impossible.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialer{}.DialContext(ctx, network, addr)
}

// dialer is DialContext with ranges it may dial although they are blocked.
type dialer struct{ allow []netip.Prefix }

func (dl dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ipRaw := range ips {
		ip, ok := netip.AddrFromSlice(ipRaw)
		if !ok {
			continue
		}
		if addressBlocked(ip, dl.allow) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedIP, host, ip)
		}
	}
	// Dial the first resolvable, non-blocked address. Pin to IP to defeat rebinding.
	var d net.Dialer
	d.Timeout = 5 * time.Second
	var errs []error
	for _, ipRaw := range ips {
		ip, ok := netip.AddrFromSlice(ipRaw)
		if !ok {
			continue
		}
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("dial %s: %w", host, errors.Join(errs...))
	}
	return nil, fmt.Errorf("dial %s: no usable address", host)
}

// Transport returns an http.Transport that dials through DialContext.
func Transport() *http.Transport { return transport(Options{}) }

func transport(o Options) *http.Transport {
	return &http.Transport{
		DialContext:           dialer{o.Allow}.DialContext,
		TLSClientConfig:       o.TLS,
		ResponseHeaderTimeout: 10 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
	}
}

// NoRedirect refuses all HTTP redirects.
func NoRedirect(req *http.Request, via []*http.Request) error {
	return http.ErrUseLastResponse
}

// Client returns a client through Transport with a timeout for the whole
// request. It follows up to five redirects to http or https URLs; each hop is
// dialed through DialContext, so a redirect cannot reach a blocked address
// either.
func Client(timeout time.Duration) *http.Client { return ClientWith(timeout, Options{}) }

// Options loosen one client, for a test that talks to a server of its own:
// the rest of the process keeps every check.
type Options struct {
	// Allow lists ranges the client may dial although they are blocked,
	// such as the loopback address of an httptest server.
	Allow []netip.Prefix
	// TLS configures the client's TLS, such as an httptest server's roots.
	TLS *tls.Config
}

// ClientWith is Client with options.
func ClientWith(timeout time.Duration, o Options) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: transport(o),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("safehttp: too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("safehttp: redirect to %s URL", req.URL.Scheme)
			}
			return nil
		},
	}
}
