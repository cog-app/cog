package ssh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/ssh/shell"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// benchShell is a shell on a real session filesystem, the files plugin's,
// holding 20 directories of 100 small text files and one 16MB log.
func benchShell(b *testing.B) *shell.Shell {
	b.Helper()
	env := cogtest.New(b)
	env.CreateUser(b, "bench", "pass")
	fsys, err := SessionFS(env.Providers, env.Providers.SSHKeys, "bench")
	require.NoError(b, err)

	var line bytes.Buffer
	for i := range 40 {
		fmt.Fprintf(&line, "line %d of a small text file with some words in it\n", i)
	}
	for d := range 20 {
		for f := range 100 {
			content := line.Bytes()
			if f == 50 {
				content = append(bytes.Clone(content), "the needle is here\n"...)
			}
			require.NoError(b, fsys.Put(fmt.Sprintf("/files/tree/d%02d/f%03d.txt", d, f), bytes.NewReader(content)))
		}
	}
	var big bytes.Buffer
	for i := 0; big.Len() < 16<<20; i++ {
		fmt.Fprintf(&big, "2026-11-20T10:00:%02d INFO request %d served in %dms\n", i%60, i, i%997)
	}
	require.NoError(b, fsys.Put("/files/big.log", &big))

	sh, err := NewShell(env.Providers, env.Providers.SSHKeys, slog.New(slog.NewTextHandler(io.Discard, nil)), "bench", fsys)
	require.NoError(b, err)
	return sh
}

func BenchmarkShellTools(b *testing.B) {
	sh := benchShell(b)
	for _, bc := range []struct{ name, cmd string }{
		{"find-name", "find /files/tree -name 'f05*'"},
		{"find-all", "find /files"},
		{"grep-r-literal", "grep -r needle /files/tree"},
		{"grep-r-regexp-i", "grep -ri 'need[l]e' /files/tree"},
		{"grep-bigfile-literal", "grep -c 'served in 996ms' /files/big.log"},
		{"grep-bigfile-regexp", "grep -c 'request [0-9]*7 served' /files/big.log"},
		{"grep-bigfile-regexp-sparse", "grep -c 'served in 99[0-9]ms' /files/big.log"},
		{"du-s", "du -s /files"},
		{"du", "du /files"},
		{"df", "df"},
		{"head", "head -n 10 /files/big.log"},
		{"tail", "tail -n 10 /files/big.log"},
		{"cp-r", "cp -r /files/tree/d00 /files/copy && rm -r /files/copy"},
		{"wc", "wc /files/big.log"},
		{"sort", "sort -k4n /files/big.log"},
		{"sort-uniq-c", "grep -o 'in [0-9]*ms' /files/big.log | sort | uniq -c"},
		{"find-xargs", "find /files/tree -name '*.txt' -print0 | xargs -0 grep -l needle"},
		{"sha256sum", "sha256sum /files/big.log"},
		{"diff-same", "diff /files/big.log /files/big.log"},
		{"diff-r", "diff -r /files/tree/d00 /files/tree/d00"},
		{"zip-r", "zip -qr /files/t.zip /files/tree && rm /files/t.zip"},
		{"tar-cz", "tar czf /files/t.tgz -C /files tree && rm /files/t.tgz"},
		{"tar-xz", "tar czf /files/t.tgz -C /files/tree d00 && mkdir -p /files/x && tar xzf /files/t.tgz -C /files/x && rm -r /files/x /files/t.tgz"},
	} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var stderr strings.Builder
				err := sh.Run(context.Background(), bc.cmd, strings.NewReader(""), io.Discard, &stderr)
				if err != nil {
					b.Fatalf("%s: %v: %s", bc.cmd, err, stderr.String())
				}
			}
		})
	}
}
