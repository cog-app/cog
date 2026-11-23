//go:build !windows && !darwin

package setup

import (
	"fmt"
	"os"
	"os/exec"
)

// OwnsConsole is false: there is no way to tell a file manager's launch
// from a terminal's.
func OwnsConsole() bool { return false }

// GUIAvailable reports whether there is a desktop to open a browser on.
func GUIAvailable() bool {
	return os.Getenv("SSH_CONNECTION") == "" && (os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "")
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	if err := exec.Command("xdg-open", url).Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}
