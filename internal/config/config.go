// Package config holds the serializable server configuration.
package config

import (
	"net/url"
	"strings"
)

// Config holds settings that can be loaded from flags, files, or environment.
type Config struct {
	Addr      string `json:"addr"`
	DBPath    string `json:"db_path"`
	BaseURL   string `json:"base_url"`
	DataDir   string `json:"data_dir"`   // Root directory for user data (e.g. /var/lib/cog/data)
	LogFormat string `json:"log_format"` // "text" (default) or "json"
	LogLevel  string `json:"log_level"`  // "debug", "info" (default), "warn", "error"

	// TrustedProxies is a comma-separated list of CIDR ranges whose
	// X-Forwarded-For / X-Real-IP headers are honored. Defaults to empty,
	// meaning headers are ignored entirely. Set this to the reverse proxy's
	// source range (e.g. "172.16.0.0/12" for a Docker bridge network, or
	// "127.0.0.1/32,::1/128" for a local proxy).
	TrustedProxies string `json:"trusted_proxies"`

	// FederationPeers is a comma-separated allowlist of remote hosts whose
	// inbound OCM shares are accepted, alongside the admin's list. "*"
	// accepts any server that runs federation.
	FederationPeers string `json:"federation_peers"`

	// PluginOrigin is the origin process plugins' pages render on, in a
	// frame in the shell, so their HTML cannot act as the user against the
	// host: "https://plugins.example.com", or "https://*.plugins.example.com"
	// for an origin per plugin. It must serve this server (the same
	// listener answers both). Empty renders them in the host origin, which
	// trusts every enabled process plugin with every user's session.
	PluginOrigin string `json:"plugin_origin"`

	// TrustedProcessPlugins lists process plugin IDs (without the "cog-plugin-"
	// prefix) that may be loaded as trusted: they receive the host CSRF secret
	// and direct system DB access. Plugins not in this list are still
	// discovered, but loaded sandboxed via gRPC.
	TrustedProcessPlugins string `json:"trusted_process_plugins"`

	SSHAddr        string `json:"ssh_addr"`          // env COG_SSH_ADDR, default ":2222"
	SSHEnabled     bool   `json:"ssh_enabled"`       // env COG_SSH_ENABLED, default true
	SSHHostKeyPath string `json:"ssh_host_key_path"` // env COG_SSH_HOST_KEY, default ""

	// JMAPCalendars offers JMAP for Calendars, which follows an Internet
	// draft (env COG_JMAP_CALENDARS, default false). JMAP for Contacts is
	// always offered.
	JMAPCalendars bool `json:"jmap_calendars"`

	// DisableProcessPlugins skips loading cog-plugin-* binaries from PATH.
	// Tests set this so they don't depend on what's installed on the machine.
	DisableProcessPlugins bool `json:"disable_process_plugins"`

	// ProcessPluginNamesOnly finds the cog-plugin-* binaries on PATH, so
	// their IDs stay reserved, without starting them: for a command that
	// uses none of what they serve.
	ProcessPluginNamesOnly bool `json:"-"`
}

// PluginOriginFor returns the origin pluginID's pages render on, or "" when
// process plugins render in the host.
func (c *Config) PluginOriginFor(pluginID string) string {
	if c.PluginOrigin == "" {
		return ""
	}
	return strings.TrimSuffix(strings.Replace(c.PluginOrigin, "*", pluginID, 1), "/")
}

// PluginOriginHost reports whether host is a plugin origin, and which
// plugin's when the origin is one per plugin ("" when all share one).
func (c *Config) PluginOriginHost(host string) (pluginID string, ok bool) {
	if c.PluginOrigin == "" {
		return "", false
	}
	u, err := url.Parse(c.PluginOrigin)
	if err != nil || u.Host == "" {
		return "", false
	}
	host = strings.ToLower(host)
	pattern := strings.ToLower(u.Host)
	if !strings.Contains(pattern, "*") {
		return "", host == pattern
	}
	prefix, suffix, _ := strings.Cut(pattern, "*")
	if !strings.HasPrefix(host, prefix) || !strings.HasSuffix(host, suffix) || len(host) <= len(prefix)+len(suffix) {
		return "", false
	}
	id := host[len(prefix) : len(host)-len(suffix)]
	if strings.ContainsAny(id, "./:") {
		return "", false
	}
	return id, true
}
