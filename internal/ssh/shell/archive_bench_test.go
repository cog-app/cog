package shell

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/vfs"
)

// benchTree makes /files/tree: 2000 small text files across 40 directories,
// and three 8MiB files — a video, random data, and a log — and returns a
// shell on it and the tree's size in bytes.
func benchTree(b *testing.B) (*Shell, string, int64) {
	b.Helper()
	dir := b.TempDir()
	rng := rand.New(rand.NewPCG(1, 2))
	words := strings.Fields("the quick brown fox jumps over lazy dog cog files share volume mount quota archive")
	var total int64
	write := func(name string, data []byte) {
		p := filepath.Join(dir, "tree", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			b.Fatal(err)
		}
		total += int64(len(data))
	}
	text := func(n int) []byte {
		var buf bytes.Buffer
		for buf.Len() < n {
			buf.WriteString(words[rng.IntN(len(words))])
			buf.WriteByte(' ')
		}
		return buf.Bytes()[:n]
	}
	random := func(n int) []byte {
		data := make([]byte, n)
		for i := range data {
			data[i] = byte(rng.Uint32())
		}
		return data
	}
	for i := range 2000 {
		write(fmt.Sprintf("d%02d/f%04d.txt", i%40, i), text(500+rng.IntN(3500)))
	}
	write("big/video.mp4", random(8<<20))
	write("big/data.bin", random(8<<20))
	write("big/log.txt", text(8<<20))

	ofs := vfs.NewOverlayFS(nil)
	ofs.Mount("/files", &testFileFS{root: dir})
	return New("testuser", ofs, nil, nil, nil, slog.New(slog.DiscardHandler)), dir, total
}

func benchRunOnce(b *testing.B, sh *Shell, command string) {
	b.Helper()
	var stderr bytes.Buffer
	if err := sh.Run(context.Background(), command, strings.NewReader(""), &bytes.Buffer{}, &stderr); err != nil {
		b.Fatalf("%s: %v: %s", command, err, stderr.String())
	}
}

func benchArchive(b *testing.B, setup, command string) {
	sh, _, total := benchTree(b)
	if setup != "" {
		benchRunOnce(b, sh, setup)
	}
	b.SetBytes(total)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchRunOnce(b, sh, command)
	}
}

func BenchmarkZip(b *testing.B) {
	benchArchive(b, "", "zip -rq /files/out.zip /files/tree")
}

func BenchmarkUnzip(b *testing.B) {
	benchArchive(b, "zip -rq /files/out.zip /files/tree", "unzip -oq -d /files/x /files/out.zip")
}

func BenchmarkTarCreate(b *testing.B) {
	benchArchive(b, "", "tar cf /files/out.tar -C /files tree")
}

func BenchmarkTarCreateGzip(b *testing.B) {
	benchArchive(b, "", "tar czf /files/out.tgz -C /files tree")
}

func BenchmarkTarExtract(b *testing.B) {
	benchArchive(b, "tar cf /files/out.tar -C /files tree", "mkdir -p /files/x && tar xf /files/out.tar -C /files/x")
}

func BenchmarkTarExtractGzip(b *testing.B) {
	benchArchive(b, "tar czf /files/out.tgz -C /files tree", "mkdir -p /files/x && tar xf /files/out.tgz -C /files/x")
}
