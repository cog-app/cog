package shell

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func writeIn(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func expectOut(t *testing.T, sh *Shell, command, want string) {
	t.Helper()
	out, stderr, status := runStatus(t, sh, command)
	if out != want || status != 0 {
		t.Errorf("%s:\n got %q (status %d, stderr %q)\nwant %q", command, out, status, stderr, want)
	}
}

func TestSort(t *testing.T) {
	sh, dir := newTestShell(t)
	writeIn(t, dir, "words", "pear\napple\nBanana\napple\ncherry")
	writeIn(t, dir, "nums", "10\n-2.5\n9\n0\n-10\n3.25\n007\n-0\n")
	writeIn(t, dir, "human", "1G\n512\n2K\n1M\n3K\n")
	writeIn(t, dir, "table", "b:3:x\na:10:y\nc:2:z\na:2:w\n")
	writeIn(t, dir, "blank", "  b 2\na 10\n c 1\n")

	expectOut(t, sh, "sort /files/words", "Banana\napple\napple\ncherry\npear\n")
	expectOut(t, sh, "sort -r /files/words", "pear\ncherry\napple\napple\nBanana\n")
	expectOut(t, sh, "sort -u /files/words", "Banana\napple\ncherry\npear\n")
	expectOut(t, sh, "sort -f /files/words", "apple\napple\nBanana\ncherry\npear\n")
	expectOut(t, sh, "sort -n /files/nums", "-10\n-2.5\n-0\n0\n3.25\n007\n9\n10\n")
	expectOut(t, sh, "sort -nr /files/nums", "10\n9\n007\n3.25\n0\n-0\n-2.5\n-10\n")
	expectOut(t, sh, "sort -h /files/human", "512\n2K\n3K\n1M\n1G\n")
	expectOut(t, sh, "sort -t : -k 2n /files/table", "a:2:w\nc:2:z\nb:3:x\na:10:y\n")
	expectOut(t, sh, "sort -t : -k1,1 -k2,2nr /files/table", "a:10:y\na:2:w\nb:3:x\nc:2:z\n")
	expectOut(t, sh, "sort -k2n /files/blank", " c 1\n  b 2\na 10\n")
	expectOut(t, sh, "printf 'b\\r\\na\\r\\n' | sort", "a\r\nb\r\n")
	expectOut(t, sh, "sort /files/words - < /files/nums | head -n 2", "-0\n-10\n")
	expectOut(t, sh, "printf '' | sort", "")

	if _, stderr, status := runStatus(t, sh, "sort /files/nosuch /files/words"); status != 2 || !strings.Contains(stderr, "nosuch") {
		t.Errorf("expected a missing file to fail with 2, got %d %q", status, stderr)
	}
	if _, _, status := runStatus(t, sh, "sort -k 1.2 /files/words"); status != 2 {
		t.Errorf("expected a character position to be refused, got %d", status)
	}
}

func TestUniq(t *testing.T) {
	sh, dir := newTestShell(t)
	writeIn(t, dir, "f", "a\na\nb\nA\na\nc\nc\n")
	expectOut(t, sh, "uniq /files/f", "a\nb\nA\na\nc\n")
	expectOut(t, sh, "uniq -c /files/f", "      2 a\n      1 b\n      1 A\n      1 a\n      2 c\n")
	expectOut(t, sh, "uniq -d /files/f", "a\nc\n")
	expectOut(t, sh, "uniq -u /files/f", "b\nA\na\n")
	expectOut(t, sh, "uniq -i /files/f", "a\nb\nA\nc\n")
	expectOut(t, sh, "cat /files/f | sort | uniq -c", "      1 A\n      3 a\n      1 b\n      2 c\n")
	expectOut(t, sh, "printf 'x\\nx' | uniq", "x\n")
}

func TestTee(t *testing.T) {
	sh, dir := newTestShell(t)
	expectOut(t, sh, "echo hi | tee /files/a /files/b", "hi\n")
	expectOut(t, sh, "echo more | tee -a /files/a >/dev/null", "")
	for name, want := range map[string]string{"a": "hi\nmore\n", "b": "hi\n"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
	expectOut(t, sh, "echo new | tee /files/b >/dev/null; cat /files/b", "new\n")
	out, stderr, status := runStatus(t, sh, "echo x | tee /nosuch/f")
	if out != "x\n" || status != 1 || stderr == "" {
		t.Errorf("expected stdout to get the input and the bad file to fail; got %q %q %d", out, stderr, status)
	}
}

func TestSeq(t *testing.T) {
	sh, _ := newTestShell(t)
	expectOut(t, sh, "seq 3", "1\n2\n3\n")
	expectOut(t, sh, "seq 2 4", "2\n3\n4\n")
	expectOut(t, sh, "seq 1 3 10", "1\n4\n7\n10\n")
	expectOut(t, sh, "seq 5 -2 0", "5\n3\n1\n")
	expectOut(t, sh, "seq -2 0", "-2\n-1\n0\n")
	expectOut(t, sh, "seq -s , 3", "1,2,3\n")
	expectOut(t, sh, "seq -s: 3", "1:2:3\n")
	expectOut(t, sh, "seq -w 8 10", "08\n09\n10\n")
	expectOut(t, sh, "seq 0 0.5 1.5", "0.0\n0.5\n1.0\n1.5\n")
	expectOut(t, sh, "seq 0 0.1 1 | tail -n 2", "0.9\n1.0\n")
	expectOut(t, sh, "seq 3 1", "")
	if _, _, status := runStatus(t, sh, "seq 1 0 3"); status != 1 {
		t.Errorf("expected a zero increment to fail, got %d", status)
	}
	if _, _, status := runStatus(t, sh, "seq x"); status != 1 {
		t.Errorf("expected a bad number to fail, got %d", status)
	}
}

func TestDate(t *testing.T) {
	sh, dir := newTestShell(t)
	expectOut(t, sh, "date -u -d @0", "Thu Jan  1 00:00:00 UTC 1970\n")
	expectOut(t, sh, "date -u -d @86399.5 '+%F %T %N %j %a %A %b %B %e %I %p %s %% %z'",
		"1970-01-01 23:59:59 500000000 001 Thu Thursday Jan January  1 11 PM 86399 % +0000\n")
	expectOut(t, sh, "date -d '2026-03-04 05:06:07' '+%Y%m%d%H%M%S'", "20260304050607\n")
	expectOut(t, sh, "TZ=Asia/Tokyo date -d @0 +%H:%Z", "09:JST\n")

	mtime := time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "hello.txt"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	expectOut(t, sh, "date -u -r /files/hello.txt +%FT%T", "2025-06-07T08:09:10\n")

	if out, _, status := runStatus(t, sh, "date +%Y"); status != 0 || len(out) != 5 {
		t.Errorf("expected the current year, got %q %d", out, status)
	}
	if _, _, status := runStatus(t, sh, "date -d 'next blursday'"); status != 1 {
		t.Errorf("expected an unknown date to fail, got %d", status)
	}
	if _, _, status := runStatus(t, sh, "date 0101"); status != 1 {
		t.Errorf("expected setting the date to fail, got %d", status)
	}
}

func TestSleep(t *testing.T) {
	sh, _ := newTestShell(t)
	start := time.Now()
	if _, _, status := runStatus(t, sh, "sleep 0.02 0.01s"); status != 0 {
		t.Fatalf("status %d", status)
	}
	if d := time.Since(start); d < 30*time.Millisecond {
		t.Errorf("slept only %v", d)
	}
	if _, _, status := runStatus(t, sh, "sleep soon"); status != 1 {
		t.Errorf("expected a bad interval to fail, got %d", status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start = time.Now()
	var out bytes.Buffer
	err := sh.Run(ctx, "sleep 1d", strings.NewReader(""), &out, &out)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a cancelled sleep took %v", d)
	}
	if err == nil {
		t.Fatal("expected a cancelled sleep to fail")
	}
}

func TestEnv(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _, _ := runStatus(t, sh, "LOCAL=1; FOO=bar env")
	if !strings.Contains(out, "FOO=bar\n") || !strings.Contains(out, "USER=testuser\n") || strings.Contains(out, "LOCAL=") {
		t.Errorf("expected exported variables only, got %q", out)
	}
	expectOut(t, sh, "env -i A=1 B=2", "A=1\nB=2\n")
	expectOut(t, sh, "env -i A=1 A=2", "A=2\n")
	expectOut(t, sh, "env A=1 sh -c 'echo $A $USER'", "1 testuser\n")
	expectOut(t, sh, "env -u USER sh -c 'echo x${USER}x'", "xx\n")
	if _, _, status := runStatus(t, sh, "env false"); status != 1 {
		t.Errorf("expected env to pass on the status, got %d", status)
	}
}

func TestWhich(t *testing.T) {
	sh, _ := newTestShell(t)
	out, stderr, status := runStatus(t, sh, "which ls cd nosuch")
	if out != "ls\ncd: shell builtin\n" || status != 1 || !strings.Contains(stderr, "nosuch") {
		t.Errorf("got %q %q %d", out, stderr, status)
	}
	if _, _, status := runStatus(t, sh, "which sort xargs"); status != 0 {
		t.Errorf("expected found commands to succeed, got %d", status)
	}
}

func TestXargs(t *testing.T) {
	sh, dir := newTestShell(t)
	expectOut(t, sh, "printf 'a b\\nc\\n' | xargs", "a b c\n")
	expectOut(t, sh, "printf 'a b\\nc\\n' | xargs echo x", "x a b c\n")
	expectOut(t, sh, "printf 'a b c d e' | xargs -n 2", "a b\nc d\ne\n")
	expectOut(t, sh, `printf '"a b" c\\ d '"'"'e'"'"'' | xargs -n 1`, "a b\nc d\ne\n")
	expectOut(t, sh, `printf 'a b\0c' | xargs -0 -n 1`, "a b\nc\n")
	expectOut(t, sh, `printf 'a b,c' | xargs -d , -n 1`, "a b\nc\n")
	expectOut(t, sh, "printf '  one two\\nthree\\n' | xargs -I {} echo '<{}>'", "<one two>\n<three>\n")
	expectOut(t, sh, "printf '' | xargs echo empty", "empty\n")
	expectOut(t, sh, "printf '' | xargs -r echo empty", "")
	expectOut(t, sh, "printf '*' | xargs echo", "*\n")

	// Commands run where xargs was run, and see no input of their own.
	expectOut(t, sh, "cd Documents && echo notes.txt | xargs cat", "line1\nline2\nline3\n")
	expectOut(t, sh, "echo x | xargs sh -c 'cat; echo $0'", "x\n")

	_, stderr, status := runStatus(t, sh, "printf 'hello.txt nosuch' | xargs -n 1 cat")
	if status != 123 || !strings.Contains(stderr, "nosuch") {
		t.Errorf("expected a failed command to give 123, got %d %q", status, stderr)
	}
	if _, _, status := runStatus(t, sh, "echo a | xargs nosuchcommand"); status != 127 {
		t.Errorf("expected a missing command to give 127, got %d", status)
	}
	if _, _, status := runStatus(t, sh, "printf 'a b' | xargs -n 1 sh -c 'exit 255'"); status != 124 {
		t.Errorf("expected status 255 to stop xargs with 124, got %d", status)
	}
	if _, stderr, status := runStatus(t, sh, `printf '"open' | xargs echo`); status != 1 || !strings.Contains(stderr, "unmatched") {
		t.Errorf("expected an unmatched quote to fail, got %d %q", status, stderr)
	}
	_, stderr, _ = runStatus(t, sh, "echo a | xargs -t echo")
	if stderr != "echo a\n" {
		t.Errorf("expected -t to trace the command, got %q", stderr)
	}

	runStatus(t, sh, "seq 3 | xargs -I N touch /files/n_N")
	for _, n := range []string{"n_1", "n_2", "n_3"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Error(err)
		}
	}
}

func TestXargsBatchesLongInput(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _, status := runStatus(t, sh, "seq 100000 | xargs echo")
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected the arguments to be split across commands, got %d line(s)", len(lines))
	}
	n := 0
	for _, l := range lines {
		if len(l) > xargsMaxChars {
			t.Fatalf("a command line of %d bytes", len(l))
		}
		n += len(strings.Fields(l))
	}
	if n != 100000 {
		t.Fatalf("got %d arguments", n)
	}
}

func TestSh(t *testing.T) {
	sh, _ := newTestShell(t)
	expectOut(t, sh, "sh -c 'echo $0 $1 $2' name a b", "name a b\n")
	expectOut(t, sh, "sh -c 'cd Documents; pwd'; pwd", "/files/Documents\n/files\n")
	// sh used to replace the session's interpreter, which cd then moved
	// instead of the one running the rest of the line.
	expectOut(t, sh, "sh -c true; cd Documents; pwd", "/files/Documents\n")
	expectOut(t, sh, "cd Documents; sh -c pwd", "/files/Documents\n")
	expectOut(t, sh, "printf 'echo $1\\nexit 3\\n' > /files/s.sh; sh s.sh arg; echo $?", "arg\n3\n")
	expectOut(t, sh, "echo 'echo from stdin' | sh", "from stdin\n")
	if _, _, status := runStatus(t, sh, "sh -c 'if'"); status != 2 {
		t.Errorf("expected a syntax error to give 2, got %d", status)
	}
	if _, _, status := runStatus(t, sh, "sh nosuch.sh"); status != 127 {
		t.Errorf("expected a missing script to give 127, got %d", status)
	}
}

func TestSortUniqueKeepsFirstOfRun(t *testing.T) {
	sh, _ := newTestShell(t)
	expectOut(t, sh, "printf 'b 1\\na 2\\nb 3\\na 4\\n' | sort -u -k1,1", "a 2\nb 1\n")
	expectOut(t, sh, "printf 'b 1\\na 2\\nb 3\\na 4\\n' | sort -ru -k1,1", "b 1\na 2\n")
	expectOut(t, sh, "printf '10\\n9\\n010\\n' | sort -nu", "9\n10\n")
}

func TestXargsItemsLongerThanTheBuffer(t *testing.T) {
	long := strings.Repeat("y", 200<<10)
	var got []string
	err := readXargsItems(bufio.NewReaderSize(strings.NewReader(long+",x,"), 16), ',', false, func(item []byte) error {
		got = append(got, string(item))
		return nil
	})
	require.NoError(t, err)
	if len(got) != 2 || got[0] != long || got[1] != "x" {
		t.Fatalf("expected the long item and x, got %d items", len(got))
	}
}
