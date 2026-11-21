package shell

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/vfs"
)

func newTestHighlighter(t *testing.T) (*highlighter, string) {
	t.Helper()
	sh, dir := newTestShell(t)
	return newHighlighter(sh, func() string { return home }), dir
}

// typed paints line as it is after listen has seen it typed.
func typed(h *highlighter, line string) string {
	r := []rune(line)
	h.listen(r, len(r), r[len(r)-1])
	return string(h.paint(r))
}

func TestHighlightCommand(t *testing.T) {
	h, _ := newTestHighlighter(t)
	require.Equal(t, ansiGreen+"ls"+ansiReset, typed(h, "ls"))
	require.Equal(t, ansiGreen+"cd"+ansiReset, typed(h, "cd"))
	require.Equal(t, ansiGreen+"true"+ansiReset, typed(h, "true"))
	require.Equal(t, ansiRed+"lx"+ansiReset, typed(h, "lx"))
}

func TestHighlightPathsThatExist(t *testing.T) {
	h, _ := newTestHighlighter(t)
	want := ansiGreen + "cat" + ansiReset + " " +
		ansiUnderline + "hello.txt" + ansiReset + " hello.tx " +
		ansiUnderline + "Documents/notes.txt" + ansiReset + " " +
		ansiUnderline + "/files/Documents" + ansiReset + " " +
		ansiUnderline + "~/hello.txt" + ansiReset
	require.Equal(t, want, typed(h, "cat hello.txt hello.tx Documents/notes.txt /files/Documents ~/hello.txt"))

	// A redirection names a path too.
	require.Equal(t, ansiGreen+"echo"+ansiReset+" hi > "+ansiUnderline+"hello.txt"+ansiReset, typed(h, "echo hi > hello.txt"))
}

func TestHighlightFlagsQuotesExpansions(t *testing.T) {
	h, _ := newTestHighlighter(t)
	require.Equal(t, ansiGreen+"ls"+ansiReset+" "+ansiCyan+"-l"+ansiReset, typed(h, "ls -l"))
	require.Equal(t,
		ansiGreen+"echo"+ansiReset+" "+ansiYellow+`"hi `+ansiReset+ansiMagenta+"$USER"+ansiReset+ansiYellow+`"`+ansiReset+" "+ansiDim+"# x"+ansiReset,
		typed(h, `echo "hi $USER" # x`))
	// A quote still open is coloured as typed.
	require.Equal(t, ansiGreen+"echo"+ansiReset+" "+ansiYellow+`"abc`+ansiReset, typed(h, `echo "abc`))
}

func TestHighlightMultibyte(t *testing.T) {
	h, _ := newTestHighlighter(t)
	require.Equal(t, ansiRed+"é"+ansiReset+" "+ansiUnderline+"hello.txt"+ansiReset, typed(h, "é hello.txt"))
}

func TestHighlightResetForgetsPaths(t *testing.T) {
	h, dir := newTestHighlighter(t)
	require.Equal(t, ansiGreen+"cat"+ansiReset+" new.txt", typed(h, "cat new.txt"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), nil, 0o644))
	require.Equal(t, ansiGreen+"cat"+ansiReset+" new.txt", typed(h, "cat new.txt"))
	h.reset()
	require.Equal(t, ansiGreen+"cat"+ansiReset+" "+ansiUnderline+"new.txt"+ansiReset, typed(h, "cat new.txt"))
}

func TestHighlightListenRedraws(t *testing.T) {
	h, _ := newTestHighlighter(t)
	_, _, ok := h.listen(nil, 0, 0)
	require.False(t, ok, "nothing to redraw before the prompt")

	_, _, ok = h.listen([]rune("l"), 1, 'l')
	require.True(t, ok, "a rune appended was painted alone")
	_, _, ok = h.listen([]rune("ls"), 1, 'x'-'a'+1)
	require.False(t, ok, "readline redrew a line edited in the middle itself")

	// Readline draws its search prompt below the line; a redraw erases it.
	_, _, ok = h.listen([]rune("ls"), 2, keyBackSearch)
	require.False(t, ok)
	_, _, ok = h.listen([]rune("cat hello.txt"), 13, 'h')
	require.False(t, ok, "typing a search")
	_, _, ok = h.listen([]rune("cat hello.txt"), 13, 'e')
	require.False(t, ok, "typing a search")
	_, _, ok = h.listen([]rune("cat hello.txt"), 0, 'a'-'a'+1)
	require.False(t, ok)
	_, _, ok = h.listen([]rune("cat hello.txtx"), 14, 'x')
	require.True(t, ok, "the search is over after a key that ends it")
}

// slowFS answers Stat only once released.
type slowFS struct {
	vfs.FS
	release chan struct{}
}

func (s *slowFS) Stat(p string) (*vfs.FileInfo, error) {
	<-s.release
	return s.FS.Stat(p)
}

func TestHighlightSlowLookupDoesNotHoldTheKey(t *testing.T) {
	h, _ := newTestHighlighter(t)
	slow := &slowFS{FS: h.shell.fs, release: make(chan struct{})}
	h.shell.fs = slow

	start := time.Now()
	require.Equal(t, ansiGreen+"cat"+ansiReset+" hello.txt", typed(h, "cat hello.txt"))
	require.True(t, time.Since(start) < 10*lookupBudget, "a slow lookup held the key")

	close(slow.release)
	looked := func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.paths[home+"/hello.txt"] == pathExists
	}
	for deadline := time.Now().Add(5 * time.Second); !looked(); time.Sleep(time.Millisecond) {
		require.True(t, time.Now().Before(deadline), "the lookup never finished")
	}
	line := []rune("cat hello.txt ")
	h.listen(line, len(line), ' ')
	require.Equal(t, ansiGreen+"cat"+ansiReset+" "+ansiUnderline+"hello.txt"+ansiReset+" ", string(h.paint(line)))
}

// lockedBuffer is a bytes.Buffer the test reads while the shell writes it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestHighlightInteractive(t *testing.T) {
	sh, _ := newTestShell(t)
	sh.SetSSHMode(80, 24)
	in, typing := io.Pipe()
	var out lockedBuffer
	done := make(chan error, 1)
	go func() { done <- sh.RunInteractive(context.Background(), in, &out, io.Discard) }()

	_, err := io.WriteString(typing, "cat hello.txt\r")
	require.NoError(t, err)
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(out.String(), "hello world"); time.Sleep(time.Millisecond) {
		require.True(t, time.Now().Before(deadline), "the command never ran: %q", out.String())
	}
	require.NoError(t, typing.Close())
	require.NoError(t, <-done)
	require.Contains(t, out.String(), ansiGreen+"cat"+ansiReset+" "+ansiUnderline+"hello.txt"+ansiReset)
}
