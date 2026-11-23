package config

import (
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestSaveLoadRoundTrip: what setup writes is what the server reads, with
// the defaults kept for what the file leaves out.
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	cfg := Defaults()
	cfg.Addr = "127.0.0.1:8580"
	cfg.BaseURL = "https://cloud.example.com"
	cfg.DBPath = filepath.Join(dir, "system.db")
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.SSHNoAccountPasswords = true
	cfg.TLS = TLS{Mode: "acme", Addr: ":8443", Domains: []string{"cloud.example.com"}, Challenge: "dns-01",
		DNSProvider: "duckdns", DNSEnv: map[string]string{"DUCKDNS_TOKEN": "secret"}}
	cfg.Update = Update{Check: true, Auto: true}
	cfg.Install = Install{Service: "user", Dir: dir}
	require.NoError(t, Save(path, cfg))

	st, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm(), "the file may hold DNS credentials")

	got, err := Load(path)
	require.NoError(t, err)
	cfg.File = path
	require.Equal(t, cfg.Addr, got.Addr)
	require.Equal(t, cfg.DBPath, got.DBPath)
	require.Equal(t, cfg.TLS.DNSEnv["DUCKDNS_TOKEN"], got.TLS.DNSEnv["DUCKDNS_TOKEN"])
	require.Equal(t, cfg.TLS.Domains[0], got.TLS.Domains[0])
	require.True(t, got.SSHNoAccountPasswords)
	require.True(t, got.SSHEnabled, "a default the file does not name")
	require.True(t, got.Update.Auto)
	require.Equal(t, path, got.File)
}

// TestLoadResolvesRelativePaths: a service starts in any directory, so a
// relative path in the file is taken from the file's own directory.
func TestLoadResolvesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	require.NoError(t, os.WriteFile(path, []byte("db_path = \"system.db\"\ndata_dir = \"data\"\n"), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "system.db"), cfg.DBPath)
	require.Equal(t, filepath.Join(dir, "data"), cfg.DataDir)
}

// TestLoadRejectsUnknownSettings: a misspelt setting is an error, not a
// setting quietly ignored.
func TestLoadRejectsUnknownSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	require.NoError(t, os.WriteFile(path, []byte("adr = \":80\"\n"), 0o600))
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "adr")
}

// TestFind: --config, then COG_CONFIG, then the file beside an installed
// binary's bin directory.
func TestFind(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin", "cog")
	require.Equal(t, "", Find(nil, exe), "nothing installed")

	beside := filepath.Join(dir, FileName)
	require.NoError(t, os.WriteFile(beside, nil, 0o600))
	require.Equal(t, beside, Find(nil, exe))

	t.Setenv("COG_CONFIG", "/env.toml")
	require.Equal(t, "/env.toml", Find(nil, exe))
	require.Equal(t, "/flag.toml", Find([]string{"serve", "--config", "/flag.toml"}, exe))
	require.Equal(t, "/flag.toml", Find([]string{"--config=/flag.toml"}, exe))
	require.Equal(t, "/env.toml", Find([]string{"shell", "--", "--config", "/no.toml"}, exe), "after -- it is another program's argument")
}
