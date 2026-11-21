package shell

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/vfs"
)

// adminShell is a shell that may su to bob, whose files hold bob.txt.
func adminShell(t *testing.T) *Shell {
	t.Helper()
	sh, _ := newTestShell(t)
	bobDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(bobDir, "bob.txt"), []byte("bob's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bobFS := vfs.NewOverlayFS(nil)
	bobFS.Mount("/files", &testFileFS{root: bobDir})
	sh.usernames = func() ([]string, error) { return []string{"alice", "bob"}, nil }
	sh.SetSwitchUser(func(name string) (string, vfs.FS, error) {
		if name != "bob" {
			return "", nil, errors.New("user " + name + " does not exist")
		}
		return "bob", bobFS, nil
	})
	return sh
}

func TestSuSwitchesAndExitReturns(t *testing.T) {
	sh := adminShell(t)
	out, errOut := runCmd(t, sh, "cd Documents; su bob; whoami; pwd; echo $USER; cat bob.txt; exit; whoami; pwd; echo $USER")
	want := "bob\n/files\nbob\nbob's\ntestuser\n/files/Documents\ntestuser\n"
	if out != want || errOut != "" {
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
}

// "su - bob" is the same su.
func TestSuDash(t *testing.T) {
	sh := adminShell(t)
	out, errOut := runCmd(t, sh, "su - bob; whoami")
	if out != "bob\n" || errOut != "" {
		t.Fatalf("got %q (stderr %q)", out, errOut)
	}
}

// exit inside an su is the status su's caller sees, and goes no further.
func TestSuExitStatus(t *testing.T) {
	sh := adminShell(t)
	out, _ := runCmd(t, sh, "su bob; exit 3; echo $?; whoami")
	if out != "3\ntestuser\n" {
		t.Fatalf("got %q", out)
	}
}

func TestSuUnknownUser(t *testing.T) {
	sh := adminShell(t)
	out, errOut := runCmd(t, sh, "su carol; echo $?; whoami")
	if out != "1\ntestuser\n" || !strings.Contains(errOut, "su: user carol does not exist") {
		t.Fatalf("got %q (stderr %q)", out, errOut)
	}
}

// A shell that may not su has no su: not to run, list or complete.
func TestSuHiddenWithoutSwitchUser(t *testing.T) {
	sh, _ := newTestShell(t)
	_, errOut := runCmd(t, sh, "su bob")
	if !strings.Contains(errOut, "su: command not found") {
		t.Fatalf("stderr %q", errOut)
	}
	if help, _ := runCmd(t, sh, "help"); strings.Contains(help, "su ") {
		t.Fatalf("help lists su:\n%s", help)
	}
	c := &completer{shell: sh, dir: func() string { return home }}
	if got, _ := c.Do([]rune("s"), 1); containsRunes(got, "u ") {
		t.Fatalf("completion offers su: %q", got)
	}

	admin := adminShell(t)
	if help, _ := runCmd(t, admin, "help"); !strings.Contains(help, "su ") {
		t.Fatalf("admin's help lacks su:\n%s", help)
	}
	c = &completer{shell: admin, dir: func() string { return home }}
	if got, _ := c.Do([]rune("su b"), 4); !containsRunes(got, "ob ") {
		t.Fatalf("su does not complete usernames: %q", got)
	}
}

func containsRunes(cands [][]rune, s string) bool {
	for _, c := range cands {
		if string(c) == s {
			return true
		}
	}
	return false
}
