package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	return pub, priv
}

func TestVerify(t *testing.T) {
	pub, priv := testKey(t)
	other, _ := testKey(t)
	data := []byte(`{"releases":[]}`)
	sig := Sign(priv, data)
	require.NoError(t, Verify([]ed25519.PublicKey{other, pub}, data, sig), "any trusted key")
	require.Error(t, Verify([]ed25519.PublicKey{other}, data, sig), "an untrusted key")
	require.Error(t, Verify([]ed25519.PublicKey{pub}, []byte(`{"releases":[{}]}`), sig), "changed data")
}

// TestNewer: the newest stable release with a binary here; prereleases only
// for a prerelease build; nothing for a development build.
func TestNewer(t *testing.T) {
	asset := map[string]Asset{"linux-amd64": {}}
	m := Manifest{Releases: []Release{
		{Version: "v1.3.0-rc.1", Prerelease: true, Assets: asset},
		{Version: "v1.2.0", Assets: asset},
		{Version: "v1.2.5", Assets: map[string]Asset{"windows-amd64": {}}},
		{Version: "v1.1.0", Assets: asset},
		{Version: "garbage", Assets: asset},
	}}
	require.Equal(t, "v1.2.0", m.Newer("v1.1.0", "linux-amd64").Version)
	require.Equal(t, "v1.2.5", m.Newer("v1.1.0", "windows-amd64").Version)
	require.Nil(t, m.Newer("v1.2.0", "linux-amd64"))
	require.Equal(t, "v1.3.0-rc.1", m.Newer("v1.2.0-rc.1", "linux-amd64").Version)
	require.Nil(t, m.Newer("dev", "linux-amd64"))
}

// releaseServer serves a signed manifest offering bin as version for this
// platform.
func releaseServer(t *testing.T, priv ed25519.PrivateKey, version string, bin []byte, tamper bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(bin)
	m := Manifest{Releases: []Release{{Version: version, Date: time.Now(), Assets: map[string]Asset{
		runtime.GOOS + "-" + runtime.GOARCH: {URL: srv.URL + "/bin", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bin))},
	}}}}
	data, err := json.Marshal(m)
	require.NoError(t, err)
	mux.HandleFunc("/releases.json", func(w http.ResponseWriter, r *http.Request) { w.Write(data) })
	mux.HandleFunc("/releases.json.sig", func(w http.ResponseWriter, r *http.Request) { w.Write(Sign(priv, data)) })
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) {
		if tamper {
			bad := append([]byte(nil), bin...)
			bad[len(bad)-1] ^= 1
			w.Write(bad)
			return
		}
		w.Write(bin)
	})
	return srv
}

// fakeBinary is a script that answers `version` as a release binary does.
func fakeBinary(t *testing.T, version string) []byte {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a shell script")
	}
	return []byte("#!/bin/sh\necho " + version + "\n")
}

// TestCheckerInstallsAndRestarts: a check finds the release, and Install
// puts its binary in place of the running one, then restarts.
func TestCheckerInstallsAndRestarts(t *testing.T) {
	pub, priv := testKey(t)
	bin := fakeBinary(t, "v1.2.0")
	srv := releaseServer(t, priv, "v1.2.0", bin, false)
	exe := filepath.Join(t.TempDir(), "cog")
	require.NoError(t, os.WriteFile(exe, []byte("old"), 0o755))

	restarted := false
	c := &Checker{URL: srv.URL + "/releases.json", Current: "v1.1.0", Platform: runtime.GOOS + "-" + runtime.GOARCH,
		Exe: exe, Keys: []ed25519.PublicKey{pub}, Client: srv.Client(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Restart: func() { restarted = true }}
	rel, err := c.Check(context.Background())
	require.NoError(t, err)
	require.Equal(t, "v1.2.0", rel.Version)
	require.NoError(t, c.Status().Replace)

	require.NoError(t, c.Install(context.Background()))
	require.True(t, restarted)
	got, err := os.ReadFile(exe)
	require.NoError(t, err)
	require.Equal(t, string(bin), string(got))
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(exe), ".cog-update-*"))
	require.NoError(t, err)
	require.Equal(t, 0, len(leftovers))
}

// TestInstallRefusesATamperedBinary: a binary that does not match the
// signed checksum never replaces the running one.
func TestInstallRefusesATamperedBinary(t *testing.T) {
	pub, priv := testKey(t)
	bin := fakeBinary(t, "v1.2.0")
	srv := releaseServer(t, priv, "v1.2.0", bin, true)
	exe := filepath.Join(t.TempDir(), "cog")
	require.NoError(t, os.WriteFile(exe, []byte("old"), 0o755))
	m, err := Fetch(context.Background(), srv.Client(), srv.URL+"/releases.json", []ed25519.PublicKey{pub})
	require.NoError(t, err)

	err = Install(context.Background(), srv.Client(), &m.Releases[0], runtime.GOOS+"-"+runtime.GOARCH, exe)
	require.Error(t, err)
	require.Contains(t, err.Error(), "checksum")
	got, err := os.ReadFile(exe)
	require.NoError(t, err)
	require.Equal(t, "old", string(got))
}

// TestInstallRefusesABinaryOfAnotherVersion: the downloaded binary has to
// say it is the release it was listed as.
func TestInstallRefusesABinaryOfAnotherVersion(t *testing.T) {
	pub, priv := testKey(t)
	srv := releaseServer(t, priv, "v1.2.0", fakeBinary(t, "v1.0.0"), false)
	exe := filepath.Join(t.TempDir(), "cog")
	require.NoError(t, os.WriteFile(exe, []byte("old"), 0o755))
	m, err := Fetch(context.Background(), srv.Client(), srv.URL+"/releases.json", []ed25519.PublicKey{pub})
	require.NoError(t, err)
	err = Install(context.Background(), srv.Client(), &m.Releases[0], runtime.GOOS+"-"+runtime.GOARCH, exe)
	require.Error(t, err)
	require.Contains(t, err.Error(), "v1.0.0")
}

// TestFetchRefusesAnUnsignedList: a list signed by a key the build does not
// trust is not read at all.
func TestFetchRefusesAnUnsignedList(t *testing.T) {
	_, priv := testKey(t)
	other, _ := testKey(t)
	srv := releaseServer(t, priv, "v1.2.0", []byte("x"), false)
	_, err := Fetch(context.Background(), srv.Client(), srv.URL+"/releases.json", []ed25519.PublicKey{other})
	require.Error(t, err)
}
