package shell

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

func TestCompleteCommand(t *testing.T) {
	sh, _ := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return home }}

	// Partial "ech" should complete to "echo".
	line := []rune("ech")
	candidates, length := c.Do(line, len(line))
	if length != 3 {
		t.Fatalf("expected length 3, got %d", length)
	}
	if len(candidates) != 1 || string(candidates[0]) != "o " {
		t.Fatalf("expected [\"o \"], got %v", runeSliceStrings(candidates))
	}
}

func TestCompleteCommandEmpty(t *testing.T) {
	sh, _ := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return home }}

	// Empty input should return all commands.
	candidates, length := c.Do(nil, 0)
	if length != 0 {
		t.Fatalf("expected length 0, got %d", length)
	}
	// Completions include all visible commands + cobra's auto-generated "help".
	root := sh.newRootCmd()
	visible := 0
	for _, cmd := range root.Commands() {
		if !cmd.Hidden {
			visible++
		}
	}
	total := visible + 1 // +1 for cobra's "help" command
	if len(candidates) != total {
		t.Fatalf("expected %d candidates, got %d", total, len(candidates))
	}
}

func TestCompletePath(t *testing.T) {
	sh, _ := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return home }}

	// "ls " + TAB should list files in current directory.
	line := []rune("ls ")
	candidates, length := c.Do(line, len(line))
	if length != 0 {
		t.Fatalf("expected length 0, got %d", length)
	}
	names := runeSliceStrings(candidates)
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["Documents/"] {
		t.Fatalf("expected Documents/ in %v", names)
	}
	if !found["hello.txt "] {
		t.Fatalf("expected 'hello.txt ' in %v", names)
	}
}

func TestCompletePathPartial(t *testing.T) {
	sh, _ := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return home }}

	// "cat Doc" should complete to "Documents/".
	line := []rune("cat Doc")
	candidates, length := c.Do(line, len(line))
	if length != 3 {
		t.Fatalf("expected length 3, got %d", length)
	}
	if len(candidates) != 1 || string(candidates[0]) != "uments/" {
		t.Fatalf("expected [\"uments/\"], got %v", runeSliceStrings(candidates))
	}
}

func TestCompletePathSubdir(t *testing.T) {
	sh, _ := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return home }}

	// "cat Documents/" should list files inside Documents.
	line := []rune("cat Documents/")
	candidates, length := c.Do(line, len(line))
	if length != 10 { // len("Documents/")
		t.Fatalf("expected length 10, got %d", length)
	}
	if len(candidates) != 1 || string(candidates[0]) != "notes.txt " {
		t.Fatalf("expected [\"notes.txt \"], got %v", runeSliceStrings(candidates))
	}
}

func TestCompletePluginSub(t *testing.T) {
	sh, _ := newTestShell(t)
	sh.pluginCmds = map[string][]plugin.CommandDef{
		"files": {
			{Name: "scan", Short: "scan files", Run: func(context.Context, []string) error { return nil }},
			{Name: "status", Short: "show status", Run: func(context.Context, []string) error { return nil }},
		},
	}
	c := &completer{shell: sh, dir: func() string { return home }}

	// "files s" should complete to "scan" and "status".
	line := []rune("files s")
	candidates, length := c.Do(line, len(line))
	if length != 1 {
		t.Fatalf("expected length 1, got %d", length)
	}
	names := runeSliceStrings(candidates)
	if len(names) != 2 {
		t.Fatalf("expected 2 candidates, got %v", names)
	}
}

func TestCompletePluginName(t *testing.T) {
	sh, _ := newTestShell(t)
	sh.pluginCmds = map[string][]plugin.CommandDef{
		"files": {{Name: "scan"}},
	}
	c := &completer{shell: sh, dir: func() string { return home }}

	// "fi" should complete to "files".
	line := []rune("fi")
	candidates, length := c.Do(line, len(line))
	if length != 2 {
		t.Fatalf("expected length 2, got %d", length)
	}
	if len(candidates) != 1 || string(candidates[0]) != "les " {
		t.Fatalf("expected [\"les \"], got %v", runeSliceStrings(candidates))
	}
}

func TestCompleteUsernameArg(t *testing.T) {
	sh, _ := newTestShell(t)
	sh.usernames = func() ([]string, error) {
		return []string{"admin", "alice", "bob"}, nil
	}
	sh.pluginCmds = map[string][]plugin.CommandDef{
		"gallery": {
			{
				Name:  "scan",
				Short: "scan media",
				Args:  []string{"username"},
				Run:   func(context.Context, []string) error { return nil },
			},
		},
	}
	c := &completer{shell: sh, dir: func() string { return home }}

	// "gallery scan a" should complete to "admin" and "alice".
	line := []rune("gallery scan a")
	candidates, length := c.Do(line, len(line))
	if length != 1 {
		t.Fatalf("expected length 1, got %d", length)
	}
	names := runeSliceStrings(candidates)
	if len(names) != 2 {
		t.Fatalf("expected 2 candidates (admin, alice), got %v", names)
	}

	// "gallery scan " (with trailing space) should list all usernames.
	line = []rune("gallery scan ")
	candidates, length = c.Do(line, len(line))
	if length != 0 {
		t.Fatalf("expected length 0, got %d", length)
	}
	if len(candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %v", runeSliceStrings(candidates))
	}
}

func TestCompletePathArg(t *testing.T) {
	sh, _ := newTestShell(t)
	sh.pluginCmds = map[string][]plugin.CommandDef{
		"myplugin": {
			{
				Name:  "process",
				Short: "process a file",
				Args:  []string{"path"},
				Run:   func(context.Context, []string) error { return nil },
			},
		},
	}
	c := &completer{shell: sh, dir: func() string { return home }}

	// "myplugin process Doc" should complete to "Documents/".
	line := []rune("myplugin process Doc")
	candidates, length := c.Do(line, len(line))
	if length != 3 {
		t.Fatalf("expected length 3, got %d", length)
	}
	if len(candidates) != 1 || string(candidates[0]) != "uments/" {
		t.Fatalf("expected [\"uments/\"], got %v", runeSliceStrings(candidates))
	}
}

func TestArgLengthExceeded(t *testing.T) {
	sh, _ := newTestShell(t)
	sh.pluginCmds = map[string][]plugin.CommandDef{
		"gallery": {
			{
				Name:  "scan",
				Short: "scan media",
				Args:  []string{"username"},
				Run:   func(context.Context, []string) error { return nil },
			},
		},
	}
	c := &completer{shell: sh, dir: func() string { return home }}

	// "gallery scan alice " — already filled the one arg, no more completions.
	line := []rune("gallery scan alice ")
	candidates, _ := c.Do(line, len(line))
	if len(candidates) != 0 {
		t.Fatalf("expected 0 candidates, got %v", runeSliceStrings(candidates))
	}
}

func TestCompleteFlagNames(t *testing.T) {
	sh, _ := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return home }}

	// "ls -" should complete flag names.
	line := []rune("ls -")
	candidates, length := c.Do(line, len(line))
	if length != 1 { // the "-" is the partial
		t.Fatalf("expected length 1, got %d", length)
	}
	names := runeSliceStrings(candidates)
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["a "] && !found["-all "] {
		t.Fatalf("expected -a or --all in %v", names)
	}
}

// cobra reports a line it cannot complete on the process's stderr, which for
// the server is its log rather than the session.
func TestCompleteRejectedLineStaysOffStderr(t *testing.T) {
	sh, _ := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return home }}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = orig })

	for _, line := range []string{"cp -x Pho", "nosuchcmd ", "ls --nosuch=", "head -n abc "} {
		if got, _ := c.Do([]rune(line), len(line)); len(got) != 0 {
			t.Errorf("%q: expected no candidates, got %v", line, runeSliceStrings(got))
		}
	}
	os.Stderr = orig
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	leaked, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaked) != 0 {
		t.Fatalf("completion wrote to stderr: %q", leaked)
	}
}

func TestCompleteProblemPaintedBelowLine(t *testing.T) {
	sh, _ := newTestShell(t)
	sh.color = true
	c := &completer{shell: sh, dir: func() string { return home }}
	prompt := "\033[1;32mme@cog\033[0m:/files$ "
	line := []rune("cp -x Pho")

	if got := string(c.paint(prompt, line, line, 80)); got != string(line) {
		t.Fatalf("expected nothing painted before a Tab, got %q", got)
	}
	if got, _ := c.Do(line, len(line)); len(got) != 0 {
		t.Fatalf("expected no candidates, got %v", runeSliceStrings(got))
	}
	// 15 columns of prompt and 9 of line put the cursor back in column 25.
	want := "cp -x Pho\n\033[K\033[31mcp: unknown shorthand flag: 'x' in -x\033[0m\033[1A\033[25G"
	if got := string(c.paint(prompt, line, line, 80)); got != want {
		t.Fatalf("painted %q, want %q", got, want)
	}
	if got := string(c.paint(prompt, line, line, 80)); got != string(line) {
		t.Fatalf("expected the problem to be painted once, got %q", got)
	}

	var cleared strings.Builder
	require.NoError(t, c.clearShown(&cleared))
	require.NoError(t, c.clearShown(&cleared))
	if cleared.String() != "\033[J" {
		t.Fatalf("expected one clear after a problem was shown, got %q", cleared.String())
	}
}

func TestCompleteProblemTruncatedToWidth(t *testing.T) {
	sh, _ := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return home }}
	line := []rune("nosuchcommand ")
	c.Do(line, len(line))
	want := "nosuchcommand \n\033[Knosuchcommand: comma\033[1A\033[17G"
	if got := string(c.paint("$ ", line, line, 21)); got != want {
		t.Fatalf("painted %q, want %q", got, want)
	}
}

func TestCompleteAfterFlag(t *testing.T) {
	sh, _ := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return home }}

	line := []rune("cp -r Doc")
	candidates, _ := c.Do(line, len(line))
	got := runeSliceStrings(candidates)
	if len(got) != 1 || got[0] != "uments/" {
		t.Fatalf("expected Documents/, got %v", got)
	}
}

func runeSliceStrings(rs [][]rune) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}
