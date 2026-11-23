package setup

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// elevate runs exe with args as root and waits for it: macOS asks for an
// administrator's password in a dialog, or sudo at the terminal.
func elevate(exe string, args []string, gui bool) error {
	var cmd *exec.Cmd
	if gui {
		quoted := make([]string, 0, len(args)+1)
		for _, a := range append([]string{exe}, args...) {
			quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
		}
		script := `do shell script "` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(strings.Join(quoted, " ")) + `" with administrator privileges`
		cmd = exec.Command("osascript", "-e", script)
	} else {
		cmd = exec.Command("sudo", append([]string{exe}, args...)...)
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run as administrator: %w", err)
	}
	return nil
}
