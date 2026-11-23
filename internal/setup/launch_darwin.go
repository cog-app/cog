package setup

import (
	"fmt"
	"os"
	"os/exec"
)

// OwnsConsole is false: Finder opens a program in Terminal, which looks
// like any other terminal.
func OwnsConsole() bool { return false }

// GUIAvailable reports whether there is a desktop to open a browser on:
// not over SSH.
func GUIAvailable() bool { return os.Getenv("SSH_CONNECTION") == "" }

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	if err := exec.Command("open", url).Run(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}
