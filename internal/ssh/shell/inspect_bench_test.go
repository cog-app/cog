package shell

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func benchRun(b *testing.B, sh *Shell, command string) {
	b.Helper()
	b.ReportAllocs()
	for b.Loop() {
		err := sh.Run(context.Background(), command, strings.NewReader(""), io.Discard, io.Discard)
		if err != nil && err.Error() != "exit status 1" {
			b.Fatal(err)
		}
	}
}

// textFile is about size bytes of 50-byte lines.
func textFile(r *rand.Rand, size int) []string {
	lines := make([]string, size/50)
	for i := range lines {
		lines[i] = fmt.Sprintf("%08d %040x", i, r.Uint64())
	}
	return lines
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") + "\n" }

// A 5 MB file against a copy with 1% of its lines changed, deleted or added.
func BenchmarkDiff(b *testing.B) {
	sh, dir := newTestShell(b)
	r := rand.New(rand.NewPCG(1, 1))
	a := textFile(r, 5<<20)
	var edited []string
	for _, l := range a {
		switch r.IntN(300) {
		case 0:
			edited = append(edited, "changed "+l)
		case 1:
		case 2:
			edited = append(edited, l, "inserted")
		default:
			edited = append(edited, l)
		}
	}
	writeFiles(b, dir, map[string]string{"a": joinLines(a), "b": joinLines(edited)})
	b.Run("normal", func(b *testing.B) { benchRun(b, sh, "diff /files/a /files/b") })
	b.Run("unified", func(b *testing.B) { benchRun(b, sh, "diff -u /files/a /files/b") })
	b.Run("brief", func(b *testing.B) { benchRun(b, sh, "diff -q /files/a /files/b") })
}

// Two wholly different 1 MB files: the worst case, bounded by maxMyersCost.
func BenchmarkDiffUnrelated(b *testing.B) {
	sh, dir := newTestShell(b)
	r := rand.New(rand.NewPCG(2, 2))
	writeFiles(b, dir, map[string]string{"a": joinLines(textFile(r, 1<<20)), "b": joinLines(textFile(r, 1<<20))})
	benchRun(b, sh, "diff /files/a /files/b")
}

func BenchmarkDiffIdentical(b *testing.B) {
	sh, dir := newTestShell(b)
	content := joinLines(textFile(rand.New(rand.NewPCG(3, 3)), 16<<20))
	writeFiles(b, dir, map[string]string{"a": content, "b": content})
	benchRun(b, sh, "diff /files/a /files/b")
}

// Two copies of a 3000-file tree that differ in a handful of files.
func BenchmarkDiffRecursive(b *testing.B) {
	sh, dir := newTestShell(b)
	for i := range 3000 {
		rel := filepath.Join(fmt.Sprintf("d%02d", i%30), fmt.Sprintf("f%04d.txt", i))
		content := fmt.Sprintf("file %d\nline two\n", i)
		for _, side := range []string{"x", "y"} {
			p := filepath.Join(dir, side, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				b.Fatal(err)
			}
			if side == "y" && i%500 == 0 {
				content += "extra\n"
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	benchRun(b, sh, "diff -r /files/x /files/y")
}

func bigFile(b *testing.B, size int) (*Shell, string) {
	sh, dir := newTestShell(b)
	writeFiles(b, dir, map[string]string{"big": joinLines(textFile(rand.New(rand.NewPCG(4, 4)), size))})
	b.SetBytes(int64(size))
	return sh, dir
}

func BenchmarkSums(b *testing.B) {
	for _, cmd := range []string{"sha256sum", "sha1sum", "md5sum"} {
		b.Run(cmd, func(b *testing.B) {
			sh, _ := bigFile(b, 16<<20)
			benchRun(b, sh, cmd+" /files/big")
		})
	}
}

func BenchmarkWc(b *testing.B) {
	for name, flags := range map[string]string{"lines": "-l", "default": "", "chars": "-m"} {
		b.Run(name, func(b *testing.B) {
			sh, _ := bigFile(b, 16<<20)
			benchRun(b, sh, "wc "+flags+" /files/big")
		})
	}
}

func BenchmarkSmallTools(b *testing.B) {
	sh, dir := newTestShell(b)
	names := make([]string, 200)
	files := map[string]string{}
	for i := range names {
		names[i] = fmt.Sprintf("/files/f%03d.txt", i)
		files[filepath.Base(names[i])] = "some text\n"
	}
	writeFiles(b, dir, files)
	args := strings.Join(names, " ")
	b.Run("stat", func(b *testing.B) { benchRun(b, sh, "stat "+args) })
	b.Run("file", func(b *testing.B) { benchRun(b, sh, "file "+args) })
	b.Run("basename", func(b *testing.B) { benchRun(b, sh, "basename -a "+args) })
}
