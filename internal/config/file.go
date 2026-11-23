package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"git.duckfam.us/jj-software/cog/internal/atomicfile"
)

// FileName is the name setup gives the configuration file, in the
// directory it installs to, one level above the binary (<dir>/bin/cog).
const FileName = "cog.toml"

// TLS is how the server serves HTTPS itself, instead of behind a proxy that
// terminates it.
type TLS struct {
	// Mode is "" or "off" (plain HTTP only), "acme" (certificates from an
	// ACME CA such as Let's Encrypt), "files" (CertFile and KeyFile) or
	// "self-signed" (made by the server for Domains, by default this
	// computer's names).
	Mode string `toml:"mode,omitempty"`
	// Addr is the HTTPS listener, ":8443" by default: a router forwards
	// the public 443 to it, so the server needs no privileged port.
	Addr string `toml:"addr,omitempty"`
	// HTTPAddr is an optional plain HTTP listener that answers ACME
	// http-01 challenges and redirects everything else to BaseURL. A
	// router forwards the public 80 to it.
	HTTPAddr string `toml:"http_addr,omitempty"`

	Domains []string `toml:"domains,omitempty"`
	Email   string   `toml:"email,omitempty"`
	// Challenge is "tls-alpn-01" (default: needs only 443 forwarded),
	// "http-01" (needs 80 forwarded to HTTPAddr) or "dns-01".
	Challenge   string `toml:"challenge,omitempty"`
	DNSProvider string `toml:"dns_provider,omitempty"`
	// DNSEnv holds the DNS provider's credentials, under the names lego
	// reads them from (CF_DNS_API_TOKEN, DUCKDNS_TOKEN, ...).
	DNSEnv map[string]string `toml:"dns_env,omitempty"`
	// CA is the ACME directory URL; "" is Let's Encrypt and "staging" its
	// staging environment.
	CA string `toml:"ca,omitempty"`

	CertFile string `toml:"cert_file,omitempty"`
	KeyFile  string `toml:"key_file,omitempty"`
}

// Enabled reports whether the server serves HTTPS itself.
func (t TLS) Enabled() bool {
	return t.Mode != "" && t.Mode != "off"
}

// Update is how the server keeps itself up to date. Builds made with the
// nosetup tag (images and distribution packages, which update through
// their own channel) ignore it.
type Update struct {
	// Check looks for a new release once a day and shows it to admins.
	Check bool `toml:"check"`
	// Auto installs a new release when one is found and restarts.
	Auto bool `toml:"auto"`
	// ManifestURL overrides where releases are listed.
	ManifestURL string `toml:"manifest_url,omitempty"`
}

// Install records what setup installed, so a repeated setup and the
// updater know how the server is run.
type Install struct {
	// Service is "system" or "user" when setup installed a service that
	// restarts the server when it exits, or "" when it runs by hand.
	Service string `toml:"service,omitempty"`
	// Dir is the directory setup installed to.
	Dir string `toml:"dir,omitempty"`
	// Account is the account a system service runs as.
	Account string `toml:"account,omitempty"`
	// Tailscale is "serve" or "funnel" when setup pointed the computer's
	// Tailscale HTTPS at the server, so a later setup can change or undo it.
	Tailscale string `toml:"tailscale,omitempty"`
}

// Defaults is the configuration before a file or the environment changes
// anything.
func Defaults() Config {
	return Config{
		Addr:       ":8580",
		DBPath:     "system.db",
		DataDir:    "data",
		LogFormat:  "text",
		LogLevel:   "info",
		SSHAddr:    ":8522",
		SSHEnabled: true,
		Update:     Update{Check: true},
	}
}

// Load reads the file at path over the defaults. Relative paths in it are
// taken from the file's directory, so a service started anywhere finds the
// same database.
func Load(path string) (Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return cfg, fmt.Errorf("parse %s: unknown settings %s", path, strings.Join(keys, ", "))
	}
	dir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return cfg, fmt.Errorf("config directory: %w", err)
	}
	for _, p := range []*string{&cfg.DBPath, &cfg.DataDir, &cfg.SSHHostKeyPath, &cfg.TLS.CertFile, &cfg.TLS.KeyFile} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(dir, *p)
		}
	}
	cfg.File = path
	return cfg, nil
}

// Save writes cfg to path, readable only by its owner: it may hold DNS
// credentials. It replaces the file in one rename, so a server reading it
// never sees half of it.
func Save(path string, cfg Config) error {
	var buf bytes.Buffer
	buf.WriteString("# Cog configuration, written by cog setup. Run cog setup again to change\n# it, or edit it and restart the service. Environment variables (COG_*)\n# override what is here.\n\n")
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := atomicfile.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// Find returns the configuration file this process should read: the one
// named by --config in args or COG_CONFIG, else the one beside an installed
// binary (<dir>/cog.toml for <dir>/bin/cog), else "" for none.
func Find(args []string, exe string) string {
	for i, a := range args {
		if a == "--" {
			break
		}
		if v, ok := strings.CutPrefix(a, "--config="); ok {
			return v
		}
		if a == "--config" && i+1 < len(args) {
			return args[i+1]
		}
	}
	if v := os.Getenv("COG_CONFIG"); v != "" {
		return v
	}
	if exe == "" {
		return ""
	}
	beside := filepath.Join(filepath.Dir(filepath.Dir(exe)), FileName)
	if _, err := os.Stat(beside); err == nil {
		return beside
	} else if !errors.Is(err, fs.ErrNotExist) {
		// Unreadable (a system install's config, from an unprivileged
		// shell): say so rather than quietly serving a fresh database
		// from the working directory.
		return beside
	}
	return ""
}
