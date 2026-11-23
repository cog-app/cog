package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"git.duckfam.us/jj-software/cog/internal/service"
)

// DefaultDir is where an install for scope goes unless the person picks
// another folder.
func DefaultDir(scope service.Scope) (string, error) {
	switch runtime.GOOS {
	case "windows":
		if scope == service.System {
			return filepath.Join(envOr("ProgramData", `C:\ProgramData`), "Cog"), nil
		}
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return "", fmt.Errorf("LOCALAPPDATA is not set")
		}
		return filepath.Join(local, "Cog"), nil
	case "darwin":
		if scope == service.System {
			return "/Library/Application Support/Cog", nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home directory: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "Cog"), nil
	default:
		if scope == service.System {
			return "/opt/cog", nil
		}
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			return filepath.Join(d, "cog"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home directory: %w", err)
		}
		return filepath.Join(home, ".local", "share", "cog"), nil
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
