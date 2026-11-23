package setup

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// OwnsConsole reports whether this process is alone in its console: the
// console Windows made for it when it was opened from Explorer, rather
// than one a terminal shares with it.
func OwnsConsole() bool {
	var pids [4]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}

// GUIAvailable reports whether there is a desktop to open a browser on.
func GUIAvailable() bool { return true }

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}
