package setup

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32            = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteEx = shell32.NewProc("ShellExecuteExW")
)

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	swHide                = 0
)

// elevate runs exe with args as an administrator, which Windows asks the
// person to allow (UAC), and waits for it.
func elevate(exe string, args []string, _ bool) error {
	params := make([]string, len(args))
	for i, a := range args {
		params[i] = syscall.EscapeArg(a)
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(strings.Join(params, " "))
	if err != nil {
		return err
	}
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess, lpVerb: verb, lpFile: file, lpParameters: p, nShow: swHide}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, err := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return errors.New("administrator rights were not given")
		}
		return fmt.Errorf("run as administrator: %w", err)
	}
	defer windows.CloseHandle(info.hProcess)
	if _, err := windows.WaitForSingleObject(info.hProcess, windows.INFINITE); err != nil {
		return fmt.Errorf("wait for setup: %w", err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return fmt.Errorf("setup exit code: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("setup as administrator failed (exit code %d)", code)
	}
	return nil
}
