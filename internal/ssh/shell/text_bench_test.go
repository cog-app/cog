package shell

import (
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// benchLines writes n lines made by line to name in dir.
func benchLines(b *testing.B, dir, name string, n int, line func(i int, r *rand.Rand) string) int64 {
	b.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	var buf bytes.Buffer
	for i := range n {
		buf.WriteString(line(i, r))
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		b.Fatal(err)
	}
	return int64(buf.Len())
}

func benchRunInput(b *testing.B, sh *Shell, command string, stdin func() io.Reader) {
	b.Helper()
	b.ReportAllocs()
	for b.Loop() {
		in := io.Reader(strings.NewReader(""))
		if stdin != nil {
			in = stdin()
		}
		if err := sh.Run(context.Background(), command, in, io.Discard, io.Discard); err != nil {
			b.Fatalf("%s: %v", command, err)
		}
	}
}

func randWord(r *rand.Rand) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	w := make([]byte, 4+r.IntN(8))
	for i := range w {
		w[i] = letters[r.IntN(len(letters))]
	}
	return string(w)
}

func BenchmarkSort(b *testing.B) {
	const n = 1_000_000
	sh, dir := newTestShell(b)
	size := benchLines(b, dir, "words", n, func(_ int, r *rand.Rand) string {
		return randWord(r) + " " + strconv.Itoa(r.IntN(1_000_000)) + "," + randWord(r)
	})
	for _, c := range []struct{ name, cmd string }{
		{"lines", "sort /files/words"},
		{"reverse-unique", "sort -ru /files/words"},
		{"fold", "sort -f /files/words"},
		{"numeric-key", "sort -k2n /files/words"},
		{"separator-keys", "sort -t , -k2,2 -k1,1nr /files/words"},
	} {
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(size)
			benchRunInput(b, sh, c.cmd, nil)
		})
	}
}

func BenchmarkUniq(b *testing.B) {
	const n = 1_000_000
	sh, dir := newTestShell(b)
	size := benchLines(b, dir, "sorted", n, func(i int, _ *rand.Rand) string {
		return "line number " + strconv.Itoa(i/4)
	})
	for _, c := range []struct{ name, cmd string }{
		{"plain", "uniq /files/sorted"},
		{"count", "uniq -c /files/sorted"},
		{"ignore-case", "uniq -i /files/sorted"},
	} {
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(size)
			benchRunInput(b, sh, c.cmd, nil)
		})
	}
}

func BenchmarkTee(b *testing.B) {
	sh, _ := newTestShell(b)
	data := bytes.Repeat([]byte("0123456789abcdef"), 4<<20) // 64 MiB
	b.SetBytes(int64(len(data)))
	benchRunInput(b, sh, "tee /files/out", func() io.Reader { return bytes.NewReader(data) })
}

func BenchmarkSeq(b *testing.B) {
	sh, _ := newTestShell(b)
	b.Run("int", func(b *testing.B) { benchRunInput(b, sh, "seq 1000000", nil) })
	b.Run("float", func(b *testing.B) { benchRunInput(b, sh, "seq 0 0.5 500000", nil) })
	b.Run("width", func(b *testing.B) { benchRunInput(b, sh, "seq -w 1000000", nil) })
}

func BenchmarkXargs(b *testing.B) {
	sh, _ := newTestShell(b)
	var many bytes.Buffer
	for i := range 100_000 {
		many.WriteString(strconv.Itoa(i))
		many.WriteByte('\n')
	}
	b.Run("batched", func(b *testing.B) {
		benchRunInput(b, sh, "xargs echo", func() io.Reader { return bytes.NewReader(many.Bytes()) })
	})
	few := many.Bytes()[:bytes.Index(many.Bytes(), []byte("\n2000\n"))+1]
	b.Run("one-per-command", func(b *testing.B) {
		benchRunInput(b, sh, "xargs -n 1 echo", func() io.Reader { return bytes.NewReader(few) })
	})
	b.Run("replace", func(b *testing.B) {
		benchRunInput(b, sh, "xargs -I {} echo x{}y", func() io.Reader { return bytes.NewReader(few) })
	})
}

func BenchmarkMisc(b *testing.B) {
	sh, _ := newTestShell(b)
	for _, c := range []struct{ name, cmd string }{
		{"date", "date -u '+%F %T %s %a %b'"},
		{"env", "env"},
		{"which", "which ls sort cd nosuch || true"},
		{"sleep0", "sleep 0"},
		{"sh", "sh -c 'echo hi'"},
	} {
		b.Run(c.name, func(b *testing.B) { benchRunInput(b, sh, c.cmd, nil) })
	}
}
