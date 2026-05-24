package federation

import "slices"

import "strings"

// peerAllowlist holds the configured set of remote hosts whose inbound shares
// are accepted. Empty list = allow any. Set via SetPeerAllowlist at startup.
var peerAllowlist []string

// SetPeerAllowlist configures the peer allowlist from a comma-separated spec.
// Hosts may include scheme; comparison is case-insensitive and ignores trailing
// slashes. An empty spec disables the check (any peer accepted).
func SetPeerAllowlist(spec string) {
	var out []string
	for raw := range strings.SplitSeq(spec, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		raw = strings.TrimSuffix(strings.ToLower(raw), "/")
		raw = strings.TrimPrefix(raw, "https://")
		raw = strings.TrimPrefix(raw, "http://")
		out = append(out, raw)
	}
	peerAllowlist = out
}

// PeerAllowed reports whether host is acceptable as a federation peer.
// host may include a scheme; comparison is normalized.
func PeerAllowed(host string) bool {
	if len(peerAllowlist) == 0 {
		return true
	}
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), "/")
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	return slices.Contains(peerAllowlist, h)
}
