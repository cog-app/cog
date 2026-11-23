package setup

import (
	"fmt"
	"os"
	"os/exec"
)

// elevate runs exe with args as root and waits for it: through polkit's
// graphical prompt when there is a desktop, else sudo at the terminal.
func elevate(exe string, args []string, gui bool) error {
	name := "sudo"
	if _, err := exec.LookPath("pkexec"); err == nil && gui {
		name = "pkexec"
	}
	cmd := exec.Command(name, append([]string{exe}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
