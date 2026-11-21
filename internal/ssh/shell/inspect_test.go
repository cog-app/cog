package shell

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func TestWcCountAcrossBuffers(t *testing.T) {
	text := "ab cd\t\tef\nhé llo\n  x"
	want, err := wcCount(strings.NewReader(text), make([]byte, 1<<10), true, true)
	if err != nil {
		t.Fatal(err)
	}
	if want != (wcCounts{lines: 2, words: 6, chars: 20, bytes: 21}) {
		t.Fatalf("counts %+v", want)
	}
	for size := 1; size < 8; size++ {
		got, err := wcCount(strings.NewReader(text), make([]byte, size), true, true)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("buffer %d: %+v, want %+v", size, got, want)
		}
	}
}

func TestUTF8Continuations(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for range 1000 {
		p := make([]byte, r.IntN(40))
		for i := range p {
			p[i] = byte(r.Uint32())
		}
		want := 0
		for _, b := range p {
			if b&0xC0 == 0x80 {
				want++
			}
		}
		if got := utf8Continuations(p); got != want {
			t.Fatalf("%x: got %d, want %d", p, got, want)
		}
	}
}

func TestDiffNormal(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{
		"a.txt": "one\ntwo\nthree\nfour\n",
		"b.txt": "one\n2\nthree\nfour\nfive\n",
		"c.txt": "one\ntwo\nthree\nfour\n",
	})
	out, _, status := runStatus(t, sh, "cd /files && diff a.txt b.txt")
	want := "2c2\n< two\n---\n> 2\n4a5\n> five\n"
	if out != want || status != 1 {
		t.Fatalf("got %q status %d, want %q status 1", out, status, want)
	}
	if out, _, status := runStatus(t, sh, "cd /files && diff a.txt c.txt"); out != "" || status != 0 {
		t.Fatalf("identical files: got %q status %d", out, status)
	}
	if _, _, status := runStatus(t, sh, "cd /files && diff a.txt nosuch.txt"); status != 2 {
		t.Fatalf("missing file: status %d, want 2", status)
	}
	out, _, _ = runStatus(t, sh, "cd /files && diff b.txt a.txt")
	if want := "2c2\n< 2\n---\n> two\n5d4\n< five\n"; out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestDiffUnified(t *testing.T) {
	sh, dir := newTestShell(t)
	var a, b strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&a, "line %d\n", i)
		switch i {
		case 3:
			b.WriteString("changed 3\n")
		case 17:
		default:
			fmt.Fprintf(&b, "line %d\n", i)
		}
	}
	writeFiles(t, dir, map[string]string{"a": a.String(), "b": b.String() + "tail"})
	out, _, status := runStatus(t, sh, "cd /files && diff -u a b")
	if status != 1 {
		t.Fatalf("status %d", status)
	}
	lines := strings.SplitN(out, "\n", 3)
	if !strings.HasPrefix(lines[0], "--- a\t") || !strings.HasPrefix(lines[1], "+++ b\t") {
		t.Fatalf("bad header: %q", out)
	}
	want := "@@ -1,6 +1,6 @@\n line 1\n line 2\n-line 3\n+changed 3\n line 4\n line 5\n line 6\n" +
		"@@ -14,7 +14,7 @@\n line 14\n line 15\n line 16\n-line 17\n line 18\n line 19\n line 20\n+tail\n\\ No newline at end of file\n"
	if lines[2] != want {
		t.Fatalf("got\n%s\nwant\n%s", lines[2], want)
	}
	out, _, _ = runStatus(t, sh, "cd /files && diff -U 0 a b")
	if !strings.Contains(out, "@@ -3 +3 @@\n-line 3\n+changed 3\n@@ -17 +16,0 @@\n-line 17\n@@ -20,0 +20 @@\n+tail\n") {
		t.Fatalf("-U 0: %q", out)
	}
}

func TestDiffBriefAndRecursive(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{
		"x/same":     "s\n",
		"y/same":     "s\n",
		"x/differs":  "1\n",
		"y/differs":  "2\n",
		"x/onlyx":    "",
		"y/onlyy":    "",
		"x/sub/deep": "a\n",
		"y/sub/deep": "b\n",
		"x/sizes":    "short\n",
		"y/sizes":    "much longer\n",
		"x/bin":      "a\x00b",
		"y/bin":      "a\x00c",
	})
	out, _, status := runStatus(t, sh, "cd /files && diff -q x y")
	want := "Files x/bin and y/bin differ\nFiles x/differs and y/differs differ\nOnly in x: onlyx\nOnly in y: onlyy\nFiles x/sizes and y/sizes differ\nCommon subdirectories: x/sub and y/sub\n"
	if out != want || status != 1 {
		t.Fatalf("got %q status %d, want %q", out, status, want)
	}
	out, _, _ = runStatus(t, sh, "cd /files && diff -r x y")
	for _, s := range []string{"Files x/bin and y/bin differ\n", "diff -r x/differs y/differs\n1c1\n< 1\n---\n> 2\n", "diff -r x/sub/deep y/sub/deep\n1c1\n< a\n---\n> b\n"} {
		if !strings.Contains(out, s) {
			t.Fatalf("expected %q in %q", s, out)
		}
	}
	if strings.Contains(out, "same") {
		t.Fatalf("identical files reported: %q", out)
	}
}

func TestDiffAgainstDirectoryAndStdin(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{"f": "a\n", "d/f": "b\n"})
	if out, _, _ := runStatus(t, sh, "cd /files && diff f d"); out != "1c1\n< a\n---\n> b\n" {
		t.Fatalf("file vs dir: %q", out)
	}
	if out, _, status := runStatus(t, sh, "cd /files && echo a | diff - f"); out != "" || status != 0 {
		t.Fatalf("stdin: %q %d", out, status)
	}
}

func lcsLen(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i, ai := range slices.Backward(a) {
		for j := len(b) - 1; j >= 0; j-- {
			if ai == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	return dp[0][0]
}

// The marked lines must leave a common subsequence, and a longest one.
func TestDiffLinesMinimal(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	gen := func() []string {
		n := r.IntN(30)
		out := make([]string, n)
		for i := range out {
			out[i] = string(rune('a' + r.IntN(4)))
		}
		return out
	}
	for range 2000 {
		a, b := gen(), gen()
		ca, cb := diffLines(a, b)
		var keptA, keptB []string
		for i, c := range ca {
			if !c {
				keptA = append(keptA, a[i])
			}
		}
		for j, c := range cb {
			if !c {
				keptB = append(keptB, b[j])
			}
		}
		if strings.Join(keptA, "") != strings.Join(keptB, "") {
			t.Fatalf("%v %v: kept %v and %v", a, b, keptA, keptB)
		}
		if len(keptA) != lcsLen(a, b) {
			t.Fatalf("%v %v: kept %d lines, LCS is %d", a, b, len(keptA), lcsLen(a, b))
		}
	}
}

func TestSums(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{"a": "abc", "b": ""})
	cases := map[string]string{
		"sha256sum": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad  a\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  b\n",
		"sha1sum":   "a9993e364706816aba3e25717850c26c9cd0d89d  a\nda39a3ee5e6b4b0d3255bfef95601890afd80709  b\n",
		"md5sum":    "900150983cd24fb0d6963f7d28e17f72  a\nd41d8cd98f00b204e9800998ecf8427e  b\n",
	}
	for cmd, want := range cases {
		if out, _, status := runStatus(t, sh, "cd /files && "+cmd+" a b"); out != want || status != 0 {
			t.Fatalf("%s: got %q status %d", cmd, out, status)
		}
	}
	if out, _, _ := runStatus(t, sh, "printf abc | md5sum"); out != "900150983cd24fb0d6963f7d28e17f72  -\n" {
		t.Fatalf("stdin: %q", out)
	}

	runStatus(t, sh, "cd /files && sha256sum a b > sums")
	if out, _, status := runStatus(t, sh, "cd /files && sha256sum -c sums"); out != "a: OK\nb: OK\n" || status != 0 {
		t.Fatalf("check: %q %d", out, status)
	}
	writeFiles(t, dir, map[string]string{"a": "abd"})
	out, stderr, status := runStatus(t, sh, "cd /files && sha256sum -c sums")
	if out != "a: FAILED\nb: OK\n" || status != 1 || !strings.Contains(stderr, "1 computed checksum did NOT match") {
		t.Fatalf("check after change: %q %q %d", out, stderr, status)
	}
	if _, _, status := runStatus(t, sh, "sha256sum /files/nosuch"); status != 1 {
		t.Fatalf("missing file: status %d", status)
	}
}

func TestWc(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{"a": "hello world\nfoo  bar baz\n", "u": "héllo\n"})
	if out, _, _ := runStatus(t, sh, "cd /files && wc a"); out != " 2  5 25 a\n" {
		t.Fatalf("wc: %q", out)
	}
	if out, _, _ := runStatus(t, sh, "cd /files && wc -l a"); out != "2 a\n" {
		t.Fatalf("wc -l: %q", out)
	}
	if out, _, _ := runStatus(t, sh, "cd /files && wc -m -c u"); out != "6 7 u\n" {
		t.Fatalf("wc -mc: %q", out)
	}
	if out, _, _ := runStatus(t, sh, "cd /files && wc -l a u"); out != " 2 a\n 1 u\n 3 total\n" {
		t.Fatalf("wc several: %q", out)
	}
	if out, _, _ := runStatus(t, sh, "printf 'a b\\nc' | wc"); out != "      1       3       5\n" {
		t.Fatalf("wc stdin: %q", out)
	}
	if _, _, status := runStatus(t, sh, "wc /files/Documents"); status != 1 {
		t.Fatalf("wc dir: status %d", status)
	}
}

func TestStat(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{"e": ""})
	out, _, _ := runStatus(t, sh, "cd /files && stat -c '%n|%s|%F' hello.txt e Documents")
	if out != "hello.txt|12|regular file\ne|0|regular empty file\nDocuments|4096|directory\n" && !strings.HasPrefix(out, "hello.txt|12|regular file\ne|0|regular empty file\nDocuments|") {
		t.Fatalf("stat -c: %q", out)
	}
	out, _, _ = runStatus(t, sh, "stat /files/hello.txt")
	if !strings.Contains(out, "  File: /files/hello.txt\n  Size: 12") || !strings.Contains(out, "Modify: ") {
		t.Fatalf("stat: %q", out)
	}
	if _, _, status := runStatus(t, sh, "stat /files/nosuch"); status != 1 {
		t.Fatalf("missing: status %d", status)
	}
}

func TestBasename(t *testing.T) {
	sh, _ := newTestShell(t)
	for cmd, want := range map[string]string{
		"basename /a/b/c.txt":          "c.txt\n",
		"basename /a/b/c.txt .txt":     "c\n",
		"basename /a/b/":               "b\n",
		"basename /":                   "/\n",
		"basename .txt .txt":           ".txt\n",
		"basename -a /x/y /z":          "y\nz\n",
		"basename -s .go a.go /b/c.go": "a\nc\n",
	} {
		if out, _, _ := runStatus(t, sh, cmd); out != want {
			t.Errorf("%s: got %q, want %q", cmd, out, want)
		}
	}
}

func TestFile(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFiles(t, dir, map[string]string{"img": "\x89PNG\r\n\x1a\n0000", "e": ""})
	out, _, _ := runStatus(t, sh, "cd /files && file hello.txt img e Documents")
	want := "hello.txt: text/plain; charset=utf-8\nimg: image/png\ne: inode/x-empty\nDocuments: directory\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
	if out, _, _ := runStatus(t, sh, "file -b /files/img"); out != "image/png\n" {
		t.Fatalf("-b: %q", out)
	}
}
