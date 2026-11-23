package main

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/update"
)

// TestRelease: the manifest takes a release's checksums from the record
// signed when it was built; a binary swapped on GitHub since, or a record
// that is not signed, or not this release's, does not get in.
func TestRelease(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keys := []ed25519.PublicKey{pub}
	dir := t.TempDir()
	bin := filepath.Join(dir, "cog-linux-amd64")
	require.NoError(t, os.WriteFile(bin, []byte("the binary CI built"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte("x"), 0o644))
	rec, err := makeRecord("v1.2.3", []string{bin, filepath.Join(dir, "SHA256SUMS")})
	require.NoError(t, err)
	require.Equal(t, 1, len(rec.Assets), "only binaries")
	data, err := json.Marshal(rec)
	require.NoError(t, err)
	sig := update.Sign(key, data)

	gh := ghRelease{TagName: "v1.2.3", Assets: []ghAsset{{Name: "cog-linux-amd64", URL: "https://example.com/cog-linux-amd64", Size: 19}}}
	rel, err := release(gh, data, sig, keys)
	require.NoError(t, err)
	require.Equal(t, rec.Assets["linux-amd64"].SHA256, rel.Assets["linux-amd64"].SHA256)
	require.Equal(t, "https://example.com/cog-linux-amd64", rel.Assets["linux-amd64"].URL)

	swapped := gh
	swapped.Assets = []ghAsset{{Name: "cog-linux-amd64", Size: 4096}}
	_, err = release(swapped, data, sig, keys)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not the 19 it was built as")

	forged := []byte(strings.Replace(string(data), rec.Assets["linux-amd64"].SHA256, strings.Repeat("0", 64), 1))
	_, err = release(gh, forged, sig, keys)
	require.Error(t, err, "a record changed after signing")

	other := gh
	other.TagName = "v1.2.4"
	_, err = release(other, data, sig, keys)
	require.Error(t, err)
	require.Contains(t, err.Error(), `record of "v1.2.3"`)
}
