package plugin

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestProcessPluginIDsDoesNotStart: an admin command learns which IDs the
// process plugins on PATH hold without running any of them.
func TestProcessPluginIDsDoesNotStart(t *testing.T) {
	bin := t.TempDir()
	ran := filepath.Join(t.TempDir(), "ran")
	script := "#!/bin/sh\ntouch " + ran + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "cog-plugin-widgets"), []byte(script), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "cog-plugin-notexec"), []byte(script), 0o644))
	t.Setenv("PATH", bin)

	r := NewRegistry(slog.New(slog.DiscardHandler), t.TempDir(), http.NewServeMux(), nil, nil, clock.System())
	ids := r.ProcessPluginIDs()
	require.True(t, slices.Equal(ids, []string{"widgets"}), "got %v", ids)
	_, err := os.Stat(ran)
	require.True(t, os.IsNotExist(err), "the plugin binary was run")
}
