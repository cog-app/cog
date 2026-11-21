package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sortedLines(s string) []string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	for i := 1; i < len(lines); i++ {
		for j := i; j > 0 && lines[j] < lines[j-1]; j-- {
			lines[j], lines[j-1] = lines[j-1], lines[j]
		}
	}
	return lines
}

func assertLines(t *testing.T, got string, want ...string) {
	t.Helper()
	g := sortedLines(got)
	w := sortedLines(strings.Join(want, "\n") + "\n")
	if strings.Join(g, "\n") != strings.Join(w, "\n") {
		t.Fatalf("got lines %q, want %q", g, w)
	}
}

func TestFind(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{
		"src/a.go":       "package a\n",
		"src/b.txt":      "b",
		"src/deep/c.go":  "",
		"src/.hidden.go": "x",
	})
	run := func(cmd string) string {
		t.Helper()
		out, stderr, status := runStatus(t, sh, "cd /files && "+cmd)
		if status != 0 || stderr != "" {
			t.Fatalf("%s: status %d: %s", cmd, status, stderr)
		}
		return out
	}

	assertLines(t, run("find src -name '*.go'"), "src/a.go", "src/deep/c.go", "src/.hidden.go")
	assertLines(t, run("find src -type d"), "src", "src/deep")
	assertLines(t, run("find src -maxdepth 1 -type f -name '*.go'"), "src/a.go", "src/.hidden.go")
	assertLines(t, run("find src -mindepth 2"), "src/deep/c.go")
	assertLines(t, run("find src -empty"), "src/deep/c.go")
	assertLines(t, run("find src -size +0c -type f ! -name '.*'"), "src/a.go", "src/b.txt")
	assertLines(t, run("find src -name deep -prune -o -type f -print"), "src/a.go", "src/b.txt", "src/.hidden.go")
	assertLines(t, run("find src \\( -name '*.txt' -o -name 'c.*' \\)"), "src/b.txt", "src/deep/c.go")
	assertLines(t, run("find src/ -path '*deep*'"), "src/deep", "src/deep/c.go")
	assertLines(t, run("find . -iname 'A.GO'"), "./src/a.go")
	assertLines(t, run("find src -mtime -1 -name b.txt"), "src/b.txt")
	if got := run("find src -name b.txt -print0"); got != "src/b.txt\x00" {
		t.Fatalf("print0: %q", got)
	}

	if _, stderr, status := runStatus(t, sh, "find /files -bogus"); status != 1 || !strings.Contains(stderr, "unknown predicate -bogus") {
		t.Fatalf("expected an unknown predicate to fail, got %d %q", status, stderr)
	}
	if _, stderr, status := runStatus(t, sh, "find /files/nosuch"); status != 1 || stderr == "" {
		t.Fatalf("expected a missing root to fail, got %d %q", status, stderr)
	}
}

func TestFindDelete(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{
		"d/keep.txt":  "k",
		"d/a.tmp":     "",
		"d/sub/b.tmp": "",
	})
	if err := os.MkdirAll(filepath.Join(dir, "d/empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, stderr, status := runStatus(t, sh, "find /files/d -name '*.tmp' -delete"); status != 0 {
		t.Fatalf("status %d: %s", status, stderr)
	}
	for _, gone := range []string{"d/a.tmp", "d/sub/b.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("expected %s deleted", gone)
		}
	}
	// A non-empty directory is refused rather than deleted with its contents.
	_, stderr, status := runStatus(t, sh, "find /files/d -type d -delete")
	if status != 1 || !strings.Contains(stderr, "not empty") {
		t.Fatalf("expected non-empty directories to be refused, got %d %q", status, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "d/keep.txt")); err != nil {
		t.Fatal("a file in a directory find -delete refused was lost:", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "d/empty")); !os.IsNotExist(err) {
		t.Fatal("expected the empty directory deleted")
	}
}

func TestGlobRegexp(t *testing.T) {
	for _, tc := range []struct {
		pat, name string
		want      bool
	}{
		{"*.go", "a.go", true},
		{"*.go", "dir/a.go", true},
		{"a?c", "abc", true},
		{"[abc]x", "bx", true},
		{"[!abc]x", "bx", false},
		{"[]]", "]", true},
		{`\*`, "*", true},
		{`\*`, "a", false},
		{"a.b", "axb", false},
	} {
		re, err := globRegexp(tc.pat, false)
		if err != nil {
			t.Fatalf("%s: %v", tc.pat, err)
		}
		if got := re.MatchString(tc.name); got != tc.want {
			t.Errorf("%q ~ %q = %v, want %v", tc.pat, tc.name, got, tc.want)
		}
	}
}

func TestGrep(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{
		"g/one.txt":     "alpha\nBeta\ngamma beta\n",
		"g/two.log":     "beta\nno newline at end beta",
		"g/sub/bin.dat": "beta\x00binary\n",
	})
	run := func(cmd string, wantStatus int) string {
		t.Helper()
		out, stderr, status := runStatus(t, sh, "cd /files/g && "+cmd)
		if status != wantStatus {
			t.Fatalf("%s: status %d, want %d: %s", cmd, status, wantStatus, stderr)
		}
		return out
	}

	if got := run("grep beta one.txt", 0); got != "gamma beta\n" {
		t.Fatalf("plain: %q", got)
	}
	if got := run("grep -in beta one.txt", 0); got != "2:Beta\n3:gamma beta\n" {
		t.Fatalf("-in: %q", got)
	}
	if got := run("grep -c beta one.txt two.log", 0); got != "one.txt:1\ntwo.log:2\n" {
		t.Fatalf("-c: %q", got)
	}
	if got := run("grep -v beta one.txt", 0); got != "alpha\nBeta\n" {
		t.Fatalf("-v: %q", got)
	}
	if got := run("grep -w gam one.txt", 1); got != "" {
		t.Fatalf("-w: %q", got)
	}
	if got := run("grep -x beta two.log", 0); got != "beta\n" {
		t.Fatalf("-x: %q", got)
	}
	if got := run("grep -o 'b.ta' one.txt", 0); got != "beta\n" {
		t.Fatalf("-o: %q", got)
	}
	if got := run("grep 'alpha\\|gamma' one.txt", 0); got != "alpha\ngamma beta\n" {
		t.Fatalf("BRE alternation: %q", got)
	}
	if got := run("grep -E 'al(p)h' one.txt", 0); got != "alpha\n" {
		t.Fatalf("-E: %q", got)
	}
	if got := run("grep 'al(p)h' one.txt", 1); got != "" {
		t.Fatalf("BRE parens are literal: %q", got)
	}
	if got := run("grep -F 'a.p' one.txt", 1); got != "" {
		t.Fatalf("-F: %q", got)
	}
	if got := run("grep 'end beta' two.log", 0); got != "no newline at end beta\n" {
		t.Fatalf("last line without newline: %q", got)
	}
	assertLines(t, run("grep -r beta .", 0),
		"./one.txt:gamma beta", "./two.log:beta", "./two.log:no newline at end beta", "Binary file ./sub/bin.dat matches")
	assertLines(t, run("grep -rl beta", 0), "./one.txt", "./two.log", "./sub/bin.dat")
	assertLines(t, run("grep -rL alpha .", 0), "./two.log", "./sub/bin.dat")
	assertLines(t, run("grep -r --include '*.log' beta .", 0), "./two.log:beta", "./two.log:no newline at end beta")
	if got := run("grep -m1 beta two.log", 0); got != "beta\n" {
		t.Fatalf("-m: %q", got)
	}
	if got := run("echo hello | grep ell", 0); got != "hello\n" {
		t.Fatalf("stdin: %q", got)
	}
	if got := run("grep -q beta one.txt", 0); got != "" {
		t.Fatalf("-q: %q", got)
	}
	if _, stderr, status := runStatus(t, sh, "cd /files/g && grep beta sub"); status != 2 || !strings.Contains(stderr, "Is a directory") {
		t.Fatalf("directory without -r: %d %q", status, stderr)
	}
	if _, _, status := runStatus(t, sh, "cd /files/g && grep beta nosuch one.txt"); status != 2 {
		t.Fatalf("missing file: status %d, want 2", status)
	}
	if _, _, status := runStatus(t, sh, "cd /files/g && grep '(' one.txt -E"); status != 2 {
		t.Fatalf("bad regexp: status %d, want 2", status)
	}
}

func TestGrepLongLine(t *testing.T) {
	sh, dir := newTestShell(t)
	long := strings.Repeat("x", 200<<10) + "needle" + strings.Repeat("y", 100<<10)
	writeFiles(t, dir, map[string]string{"long.txt": "short\n" + long + "\nafter\n"})
	out, stderr, status := runStatus(t, sh, "grep needle /files/long.txt")
	if status != 0 || out != long+"\n" {
		t.Fatalf("status %d, %d bytes out, stderr %q", status, len(out), stderr)
	}
}

func TestBREToRE2(t *testing.T) {
	for in, want := range map[string]string{
		`a\|b`:         `a|b`,
		`\(ab\)*`:      `(ab)*`,
		`a+b?`:         `a\+b\?`,
		`x\{2\}`:       `x{2}`,
		`*star`:        `\*star`,
		`^*star`:       `^\*star`,
		`[(|)]`:        `[(|)]`,
		`[[:alpha:]]+`: `[[:alpha:]]\+`,
	} {
		if got := breToRE2(in); got != want {
			t.Errorf("breToRE2(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDu(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{
		"u/a":     strings.Repeat("a", 3000),
		"u/s/b":   strings.Repeat("b", 2048),
		"u/s/t/c": "c",
	})
	run := func(cmd string) string {
		t.Helper()
		out, stderr, status := runStatus(t, sh, "cd /files && "+cmd)
		if status != 0 {
			t.Fatalf("%s: %d %s", cmd, status, stderr)
		}
		return out
	}
	// Sizes are summed in bytes and rounded once: there are no disk blocks.
	if got := run("du u"); got != "1\tu/s/t\n3\tu/s\n5\tu\n" {
		t.Fatalf("du: %q", got)
	}
	if got := run("du -sb u"); got != "5049\tu\n" {
		t.Fatalf("du -sb: %q", got)
	}
	if got := run("du -d1 -b u/"); got != "2049\tu/s\n5049\tu/\n" {
		t.Fatalf("du -d1: %q", got)
	}
	if got := run("du -ab u/s"); got != "2048\tu/s/b\n1\tu/s/t/c\n1\tu/s/t\n2049\tu/s\n" {
		t.Fatalf("du -a: %q", got)
	}
	if got := run("du -sch u/a u/s"); got != "3.0K\tu/a\n2.1K\tu/s\n5.0K\ttotal\n" {
		t.Fatalf("du -sch: %q", got)
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{
		0:             "0",
		1023:          "1023",
		1024:          "1.0K",
		1025:          "1.1K",
		10 * 1024:     "10K",
		10*1024 + 1:   "11K",
		1023*1024 + 1: "1.0M",
		5 << 30:       "5.0G",
		1536 << 20:    "1.5G",
	} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDf(t *testing.T) {
	sh, _ := newTestShell(t)
	if _, _, status := runStatus(t, sh, "df"); status != 1 {
		t.Fatalf("df without a space source: status %d, want 1", status)
	}
	var asked, askedUser string
	sh.SetSpace(func(username, p string) (int64, int64, error) {
		asked, askedUser = p, username
		if p == "/files/unlimited" {
			return 2048, -1, nil
		}
		return 3 << 30, 10 << 30, nil
	})
	out, stderr, status := runStatus(t, sh, "df -h")
	if status != 0 || asked != "/files" || askedUser != "testuser" {
		t.Fatalf("status %d asked %q: %s", status, asked, stderr)
	}
	if !strings.Contains(out, "cog               10G       3.0G       7.0G   30% /files") {
		t.Fatalf("df -h: %q", out)
	}
	out, _, _ = runStatus(t, sh, "cd /files && df unlimited")
	if !strings.Contains(out, "cog                 -          2          -     - /files/unlimited") {
		t.Fatalf("df unlimited: %q", out)
	}
}

func TestRmdir(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{"full/f": "x"})
	if err := os.MkdirAll(filepath.Join(dir, "a/b/c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, stderr, status := runStatus(t, sh, "rmdir /files/full"); status != 1 || !strings.Contains(stderr, "not empty") {
		t.Fatalf("non-empty: %d %q", status, stderr)
	}
	if _, stderr, status := runStatus(t, sh, "rmdir /files/hello.txt"); status != 1 || !strings.Contains(stderr, "not a directory") {
		t.Fatalf("file: %d %q", status, stderr)
	}
	if _, stderr, status := runStatus(t, sh, "cd /files && rmdir -p a/b/c"); status != 0 {
		t.Fatalf("-p: %d %q", status, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); !os.IsNotExist(err) {
		t.Fatal("expected rmdir -p to remove the parents")
	}
}

func TestCpMvNoClobberVerbose(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{
		"src/a":     "new a",
		"src/b":     "new b",
		"dst/src/a": "old a",
		"x":         "x",
		"y":         "old y",
	})
	if _, stderr, status := runStatus(t, sh, "cp -rn /files/src /files/dst"); status != 0 {
		t.Fatalf("cp -rn: %d %s", status, stderr)
	}
	for name, want := range map[string]string{"dst/src/a": "old a", "dst/src/b": "new b"} {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	if _, _, status := runStatus(t, sh, "mv -n /files/x /files/y"); status != 0 {
		t.Fatal("mv -n failed")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "y")); string(got) != "old y" {
		t.Fatalf("mv -n overwrote: %q", got)
	}
	out, _, _ := runStatus(t, sh, "mv -v /files/x /files/z")
	if out != "renamed '/files/x' -> '/files/z'\n" {
		t.Fatalf("mv -v: %q", out)
	}
	out, _, _ = runStatus(t, sh, "cp -v /files/z /files/w")
	if out != "'/files/z' -> '/files/w'\n" {
		t.Fatalf("cp -v: %q", out)
	}
}

func TestLsHuman(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{"big": strings.Repeat("x", 1536)})
	out, _, _ := runStatus(t, sh, "ls -lh /files/big")
	if !strings.Contains(out, " 1.5K ") {
		t.Fatalf("ls -lh: %q", out)
	}
	out, _, _ = runStatus(t, sh, "ls -l /files/big")
	if !strings.Contains(out, " 1536 ") {
		t.Fatalf("ls -l: %q", out)
	}
}

func TestHeadTailStreaming(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{
		"n":    "1\n2\n3\n4\n5\n",
		"nonl": "a\nb\nc",
		"long": strings.Repeat("z", 100<<10) + "\nend\n",
	})
	for cmd, want := range map[string]string{
		"head -n 2 /files/n":                "1\n2\n",
		"tail -n 2 /files/n":                "4\n5\n",
		"tail -n 9 /files/n":                "1\n2\n3\n4\n5\n",
		"tail -n 0 /files/n":                "",
		"head -n 9 /files/nonl":             "a\nb\nc",
		"tail -n 2 /files/nonl":             "b\nc",
		"tail -n 1 /files/long":             "end\n",
		"head -n 2 /files/n /files/nonl":    "==> /files/n <==\n1\n2\n\n==> /files/nonl <==\na\nb\n",
		"printf '1\\n2\\n3\\n' | tail -n 1": "3\n",
	} {
		out, stderr, status := runStatus(t, sh, cmd)
		if status != 0 || out != want {
			t.Errorf("%s: status %d, got %q want %q (%s)", cmd, status, out, want, stderr)
		}
	}
	out, _, _ := runStatus(t, sh, "head -n 1 /files/long")
	if len(out) != 100<<10+1 {
		t.Fatalf("head of a long line: %d bytes", len(out))
	}
}
