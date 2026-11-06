package federation

import (
	"fmt"
	"slices"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/settings"
)

// AnyPeer in the allowlist opens federation to every server. It is a
// choice an admin makes by writing it, not what an empty list means.
const AnyPeer = "*"

// Policy decides whether this server federates, and with whom. Federation is
// off until an admin turns it on: an inbound share is a request from a server
// nobody here has vouched for, landing in a user's pending shares. Once on,
// the allowed servers are the admin's list, and no server when the list is
// empty: a server joins federation with the peers it names, or with anyone
// by naming AnyPeer.
type Policy struct {
	Settings *settings.Store
	// EnvPeers is COG_FEDERATION_PEERS, a comma-separated allowlist from
	// before federation had a setting. Its servers are allowed alongside
	// the admin's, and a server that sets it federates until an admin
	// says otherwise.
	EnvPeers string
}

// Enabled reports whether federation is on.
func (p *Policy) Enabled() (bool, error) {
	if p == nil || p.Settings == nil {
		return false, nil
	}
	v, err := p.Settings.Get(settings.KeyFederationEnabled)
	if err != nil {
		return false, fmt.Errorf("read federation setting: %w", err)
	}
	if v == "" {
		return len(ParsePeers(p.EnvPeers)) > 0, nil
	}
	return v == "true", nil
}

// PeerAllowed reports whether this server federates with host, which may
// carry a scheme.
func (p *Policy) PeerAllowed(host string) (bool, error) {
	on, err := p.Enabled()
	if err != nil || !on {
		return false, err
	}
	peers, err := p.Settings.Get(settings.KeyFederationPeers)
	if err != nil {
		return false, fmt.Errorf("read federation peers: %w", err)
	}
	allowed := append(ParsePeers(peers), ParsePeers(p.EnvPeers)...)
	if slices.Contains(allowed, AnyPeer) {
		return true, nil
	}
	h := normalizePeer(host)
	return h != "" && slices.Contains(allowed, h), nil
}

// ParsePeers splits a list of servers separated by commas, spaces or
// newlines into their normalized host names.
func ParsePeers(spec string) []string {
	var out []string
	for _, raw := range strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	}) {
		if h := normalizePeer(raw); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// normalizePeer is host compared case-insensitively, without a scheme, a
// path or a trailing slash. A host carrying userinfo is nobody: "a@b" would
// let the name of a server stand in for a host.
func normalizePeer(host string) string {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), "/")
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	if strings.ContainsAny(h, "@/?# ") {
		return ""
	}
	return h
}
