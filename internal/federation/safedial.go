package federation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// blockedPrefixes are address ranges that the federation client refuses to dial,
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
var allowPrivate bool

// SetAllowPrivate enables dialing into normally-blocked ranges. Intended for
// tests only; must not be true in production.
func SetAllowPrivate(b bool) { allowPrivate = b }

func mustPrefixes(specs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(specs))
	for _, s := range specs {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			panic("federation: bad prefix " + s + ": " + err.Error())
		}
		out = append(out, p)
	}
	return out
}

// ErrBlockedIP is returned by the safe dialer when the resolved address is in
// a blocked range.
var ErrBlockedIP = errors.New("blocked IP for federation request")

// addressBlocked reports whether ip is in a blocked range.
func addressBlocked(ip netip.Addr) bool {
	if allowPrivate {
		return false
	}
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// safeDialContext resolves and dials only if the resolved IP is not in a
// blocked range. The dial target is pinned to the IP that was validated, so
// DNS rebinding between lookup and dial is impossible.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
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
		if addressBlocked(ip) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedIP, host, ip)
		}
	}
	// Dial the first resolvable, non-blocked address. Pin to IP to defeat rebinding.
	var d net.Dialer
	d.Timeout = 5 * time.Second
	for _, ipRaw := range ips {
		ip, ok := netip.AddrFromSlice(ipRaw)
		if !ok {
			continue
		}
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("dial %s: no usable address", host)
}

// safeTransport is the http.Transport used by the federation client.
func safeTransport() *http.Transport {
	return &http.Transport{
		DialContext:           safeDialContext,
		ResponseHeaderTimeout: 10 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
	}
}

// noRedirect refuses all HTTP redirects.
func noRedirect(req *http.Request, via []*http.Request) error {
	return http.ErrUseLastResponse
}
