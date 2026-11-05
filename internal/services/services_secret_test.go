package services

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// The secret moves out of the database it protects, keeping its value, and
// stays the same across opens.
func TestSecretMovesToFile(t *testing.T) {
	dir := t.TempDir()
	sysDB, err := db.OpenSystem(filepath.Join(dir, "system.db"))
	require.NoError(t, err)
	t.Cleanup(func() { sysDB.Close() })
	_, err = sysDB.Exec(`INSERT INTO system_settings (key, value) VALUES (?, ?)`, secretSettingKey, "00112233445566778899aabbccddeeff")
	require.NoError(t, err)

	path := filepath.Join(dir, "data", secretFileName)
	got, err := loadSecret(sysDB, path)
	require.NoError(t, err)
	require.Equal(t, "00112233445566778899aabbccddeeff", hex.EncodeToString(got))
	var n int
	require.NoError(t, sysDB.QueryRow(`SELECT COUNT(*) FROM system_settings WHERE key = ?`, secretSettingKey).Scan(&n))
	require.Equal(t, 0, n)
	_, err = os.Stat(path)
	require.NoError(t, err)

	again, err := loadSecret(sysDB, path)
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(got), hex.EncodeToString(again))
}
