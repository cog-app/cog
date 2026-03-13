package shell

import (
	"context"
	"testing"

	"git.duckfam.us/jj-software/cog/plugin"
)

func TestCompleteCommand(t *testing.T) {
	sh, dir := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return dir }}

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
	sh, dir := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return dir }}

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
	sh, dir := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return dir }}

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
	sh, dir := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return dir }}

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
	sh, dir := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return dir }}

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
	sh, dir := newTestShell(t)
	sh.pluginCmds = map[string][]plugin.CommandDef{
		"files": {
			{Name: "scan", Short: "scan files", Run: func(context.Context, []string) error { return nil }},
			{Name: "status", Short: "show status", Run: func(context.Context, []string) error { return nil }},
		},
	}
	c := &completer{shell: sh, dir: func() string { return dir }}

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
	sh, dir := newTestShell(t)
	sh.pluginCmds = map[string][]plugin.CommandDef{
		"files": {{Name: "scan"}},
	}
	c := &completer{shell: sh, dir: func() string { return dir }}

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
	sh, dir := newTestShell(t)
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
	c := &completer{shell: sh, dir: func() string { return dir }}

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
	sh, dir := newTestShell(t)
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
	c := &completer{shell: sh, dir: func() string { return dir }}

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
	sh, dir := newTestShell(t)
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
	c := &completer{shell: sh, dir: func() string { return dir }}

	// "gallery scan alice " — already filled the one arg, no more completions.
	line := []rune("gallery scan alice ")
	candidates, _ := c.Do(line, len(line))
	if len(candidates) != 0 {
		t.Fatalf("expected 0 candidates, got %v", runeSliceStrings(candidates))
	}
}

func TestCompleteFlagNames(t *testing.T) {
	sh, dir := newTestShell(t)
	c := &completer{shell: sh, dir: func() string { return dir }}

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

func runeSliceStrings(rs [][]rune) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}
