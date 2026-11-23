package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// DefaultAccount is the account a system service runs as: a virtual
// account, which Windows makes for the service and which needs no
// password.
const DefaultAccount = `NT SERVICE\cog`

// Install registers and starts s: a service for System, a scheduled task
// at sign-in for User, which needs no administrator rights.
func Install(s Spec) error {
	if s.Scope == User {
		return installTask(s)
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %w", err)
	}
	defer m.Disconnect()
	cfg := mgr.Config{
		StartType:        mgr.StartAutomatic,
		ServiceStartName: s.Account,
		DisplayName:      s.DisplayName,
		Description:      s.Description,
		DelayedAutoStart: true,
	}
	ws, err := m.OpenService(s.Name)
	if err != nil && !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return fmt.Errorf("open service: %w", err)
	}
	if err == nil {
		if err := stopService(ws); err != nil {
			ws.Close()
			return err
		}
		cur, err := ws.Config()
		if err != nil {
			ws.Close()
			return fmt.Errorf("service configuration: %w", err)
		}
		cur.BinaryPathName = commandLine(s.Exe, s.Args)
		cur.StartType = cfg.StartType
		cur.ServiceStartName = cfg.ServiceStartName
		cur.DisplayName = cfg.DisplayName
		cur.Description = cfg.Description
		cur.DelayedAutoStart = cfg.DelayedAutoStart
		if err := ws.UpdateConfig(cur); err != nil {
			ws.Close()
			return fmt.Errorf("update service: %w", err)
		}
	} else {
		if ws, err = m.CreateService(s.Name, s.Exe, cfg, s.Args...); err != nil {
			return fmt.Errorf("create service: %w", err)
		}
	}
	defer ws.Close()
	// A failure is any exit without reporting the service stopped,
	// including the one an update ends with: start it again.
	restart := mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: 2 * time.Second}
	if err := ws.SetRecoveryActions([]mgr.RecoveryAction{restart, restart, restart}, 24*60*60); err != nil {
		return fmt.Errorf("set recovery: %w", err)
	}
	if err := ws.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("set recovery: %w", err)
	}
	if err := ws.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

// commandLine joins exe and args as Windows parses a command line.
func commandLine(exe string, args []string) string {
	parts := []string{syscall.EscapeArg(exe)}
	for _, a := range args {
		parts = append(parts, syscall.EscapeArg(a))
	}
	return strings.Join(parts, " ")
}

func stopService(ws *mgr.Service) error {
	st, err := ws.Query()
	if err != nil {
		return fmt.Errorf("query service: %w", err)
	}
	if st.State == svc.Stopped {
		return nil
	}
	if _, err := ws.Control(svc.Stop); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		if st, err = ws.Query(); err != nil {
			return fmt.Errorf("query service: %w", err)
		}
		if st.State == svc.Stopped {
			return nil
		}
	}
	return errors.New("the service did not stop within 45 seconds")
}

// Uninstall stops and removes s.
func Uninstall(s Spec) error {
	if s.Scope == User {
		st, err := Query(s)
		if err != nil || !st.Installed {
			return err
		}
		if st.Running {
			if err := run("schtasks", "/End", "/TN", taskName(s)); err != nil {
				return err
			}
		}
		return run("schtasks", "/Delete", "/TN", taskName(s), "/F")
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %w", err)
	}
	defer m.Disconnect()
	ws, err := m.OpenService(s.Name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer ws.Close()
	if err := stopService(ws); err != nil {
		return err
	}
	if err := ws.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	return nil
}

// Stop stops s.
func Stop(s Spec) error {
	if s.Scope == User {
		st, err := Query(s)
		if err != nil || !st.Running {
			return err
		}
		return run("schtasks", "/End", "/TN", taskName(s))
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %w", err)
	}
	defer m.Disconnect()
	ws, err := m.OpenService(s.Name)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer ws.Close()
	return stopService(ws)
}

// Restart restarts s.
func Restart(s Spec) error {
	if err := Stop(s); err != nil {
		return err
	}
	if s.Scope == User {
		return run("schtasks", "/Run", "/TN", taskName(s))
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %w", err)
	}
	defer m.Disconnect()
	ws, err := m.OpenService(s.Name)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer ws.Close()
	if err := ws.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

// Query reports whether s is installed and running.
func Query(s Spec) (Status, error) {
	if s.Scope == User {
		out, err := exec.Command("schtasks", "/Query", "/TN", taskName(s), "/FO", "CSV", "/NH").Output()
		if err != nil {
			// Query fails for a task that does not exist.
			return Status{}, nil
		}
		return Status{Installed: true, Running: strings.Contains(string(out), `"Running"`)}, nil
	}
	m, err := mgr.Connect()
	if err != nil {
		return Status{}, fmt.Errorf("service manager: %w", err)
	}
	defer m.Disconnect()
	ws, err := m.OpenService(s.Name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("open service: %w", err)
	}
	defer ws.Close()
	st, err := ws.Query()
	if err != nil {
		return Status{}, fmt.Errorf("query service: %w", err)
	}
	return Status{Installed: true, Running: st.State == svc.Running}, nil
}

func taskName(s Spec) string { return s.DisplayName }

// Task is the Task Scheduler definition of a user service: it starts at
// sign-in, through a console host without a window, and has no time limit.
func Task(s Spec, userID string) string {
	esc := func(v string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(v)) // a bytes.Buffer does not fail
		return e.String()
	}
	args := "--headless " + commandLine(s.Exe, s.Args)
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>` + esc(s.Description) + `</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + esc(userID) + `</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + esc(userID) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author"><Exec><Command>conhost.exe</Command><Arguments>` + esc(args) + `</Arguments><WorkingDirectory>` + esc(s.Dir) + `</WorkingDirectory></Exec></Actions>
</Task>
`
}

func installTask(s Spec) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("current user: %w", err)
	}
	st, err := Query(s)
	if err != nil {
		return err
	}
	if st.Running {
		if err := run("schtasks", "/End", "/TN", taskName(s)); err != nil {
			return err
		}
	}
	f, err := os.CreateTemp("", "cog-task-*.xml")
	if err != nil {
		return fmt.Errorf("task definition: %w", err)
	}
	path := f.Name()
	defer os.Remove(path)
	// schtasks reads the encoding the declaration names: UTF-16 with a
	// byte order mark.
	if _, err := f.Write(utf16LE(Task(s, u.Username))); err != nil {
		f.Close()
		return fmt.Errorf("task definition: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("task definition: %w", err)
	}
	if err := run("schtasks", "/Create", "/TN", taskName(s), "/XML", path, "/F"); err != nil {
		return err
	}
	return run("schtasks", "/Run", "/TN", taskName(s))
}

func utf16LE(s string) []byte {
	u := windows.StringToUTF16(s)
	u = u[:len(u)-1] // without the terminating NUL
	b := []byte{0xFF, 0xFE}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

// CreateAccount does nothing: Windows makes a service's virtual account.
func CreateAccount(string, string) error { return nil }

// GiveDir lets account change everything in dir, and what is made there
// later.
func GiveDir(dir, account string) error {
	return run("icacls", filepath.Clean(dir), "/grant", account+":(OI)(CI)M", "/T", "/Q")
}

// RunAs does nothing: a virtual account cannot be run as, and what setup
// makes in the install directory inherits the grant GiveDir made.
func RunAs(*exec.Cmd, string) error { return nil }

// OpenFirewall lets ports in through Windows Firewall.
func OpenFirewall(ports []Port) error {
	for _, p := range ports {
		name := fmt.Sprintf("name=Cog %s (%d)", p.Name, p.Number)
		out, err := exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", name).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "No rules match") {
			return fmt.Errorf("netsh delete rule: %w: %s", err, strings.TrimSpace(string(out)))
		}
		if err := run("netsh", "advfirewall", "firewall", "add", "rule", name, "dir=in", "action=allow",
			"protocol="+strings.ToUpper(p.Proto), fmt.Sprintf("localport=%d", p.Number)); err != nil {
			return err
		}
	}
	return nil
}

// IsAdmin reports whether this process runs elevated.
func IsAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}
