// Package update keeps an installed Cog current: it reads the signed list
// of releases the docs site publishes, downloads the binary for this
// platform, checks it against the list, puts it in place of the running
// one and restarts.
//
// Trust rests on the signature, not on the transport: releases.json is
// signed with an Ed25519 key whose public half is compiled in (keys.go),
// and it carries each binary's SHA-256, from the record the release
// workflow signed when it built the binary (internal/cmd/release). A
// compromised GitHub Pages or Releases can withhold an update but cannot
// supply one.
package update

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// DefaultManifestURL is where releases are listed, unless config names
// another. The release workflow can set it with -ldflags -X.
var DefaultManifestURL = "https://cog-app.github.io/releases.json"

// SignatureSuffix is appended to the manifest's URL for its signature.
const SignatureSuffix = ".sig"

// maxManifest bounds what is read of the manifest or its signature.
const maxManifest = 4 << 20

// Manifest lists the published releases, newest first.
type Manifest struct {
	Releases []Release `json:"releases"`
}

// Release is one published version.
type Release struct {
	Version    string    `json:"version"` // "v1.2.3"
	Date       time.Time `json:"date"`
	NotesURL   string    `json:"notes_url,omitempty"`
	Prerelease bool      `json:"prerelease,omitempty"`
	// Assets are the binaries, by platform ("linux-amd64").
	Assets map[string]Asset `json:"assets"`
}

// Asset is one platform's binary.
type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"` // hex
	Size   int64  `json:"size"`
}

// Sign returns the signature of a manifest's bytes, as its .sig file holds
// it.
func Sign(key ed25519.PrivateKey, data []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, data)) + "\n")
}

// Verify checks sig, a .sig file's contents, against data with keys.
func Verify(keys []ed25519.PublicKey, data, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	for _, k := range keys {
		if ed25519.Verify(k, data, raw) {
			return nil
		}
	}
	return errors.New("the release list's signature does not match any trusted key")
}

// Fetch downloads the manifest at url and its signature, and returns the
// manifest if the signature is good.
func Fetch(ctx context.Context, client *http.Client, url string, keys []ed25519.PublicKey) (*Manifest, error) {
	data, err := get(ctx, client, url)
	if err != nil {
		return nil, err
	}
	sig, err := get(ctx, client, url+SignatureSuffix)
	if err != nil {
		return nil, err
	}
	if err := Verify(keys, data, sig); err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse release list: %w", err)
	}
	return &m, nil
}

func get(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", url, res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxManifest))
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	return data, nil
}

// Newer returns the newest release after current with a binary for
// platform, or nil when current is the newest. A prerelease is offered
// only to a build that is itself a prerelease. A build without a release
// version ("dev") is never offered one: it is not something an update
// should replace.
func (m *Manifest) Newer(current, platform string) *Release {
	if !semver.IsValid(current) {
		return nil
	}
	pre := semver.Prerelease(current) != ""
	var best *Release
	for i := range m.Releases {
		r := &m.Releases[i]
		if !semver.IsValid(r.Version) || semver.Compare(r.Version, current) <= 0 {
			continue
		}
		if (r.Prerelease || semver.Prerelease(r.Version) != "") && !pre {
			continue
		}
		if _, ok := r.Assets[platform]; !ok {
			continue
		}
		if best == nil || semver.Compare(r.Version, best.Version) > 0 {
			best = r
		}
	}
	return best
}
