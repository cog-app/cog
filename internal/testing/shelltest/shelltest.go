// Package shelltest drives interactive shell sessions through a virtual terminal emulator.
//
// It wraps the cog SSH shell (internal/ssh/shell) and connects it to a
// charmbracelet/x/vt emulator so tests can type commands, press keys,
// and assert on screen content without a real SSH connection.
package shelltest

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	"git.duckfam.us/jj-software/cog/internal/search"
	cogssh "git.duckfam.us/jj-software/cog/internal/ssh"
	"git.duckfam.us/jj-software/cog/internal/ssh/shell"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"
)

// Terminal drives an interactive shell session through a virtual terminal emulator.
type Terminal struct {
	em      *vt.SafeEmulator
	stdinW  io.WriteCloser // write end of pipe feeding the shell's stdin
	cancel  context.CancelFunc
	done    chan struct{}
	timeout time.Duration
	t       testing.TB
}

type config struct {
	width, height int
	username      string
	userFS        vfs.FS
	pluginCmds    map[string][]plugin.CommandDef
	usernames     func() ([]string, error)
	search        *search.Manager
	timeout       time.Duration
}

// Option configures a Terminal.
type Option func(*config)

// WithSize sets the terminal dimensions. Default is 80x24.
func WithSize(width, height int) Option {
	return func(c *config) { c.width = width; c.height = height }
}

// WithTimeout sets the timeout for wait operations. Default is 5s.
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// WithPluginCmds registers plugin CLI commands.
func WithPluginCmds(cmds map[string][]plugin.CommandDef) Option {
	return func(c *config) { c.pluginCmds = cmds }
}

// WithUserFS sets the filesystem the shell works in, with the user's files
// at /files.
func WithUserFS(fs vfs.FS) Option {
	return func(c *config) { c.userFS = fs }
}

// WithUsernames sets the username completion function.
func WithUsernames(fn func() ([]string, error)) Option {
	return func(c *config) { c.usernames = fn }
}

// WithSearch sets the search manager.
func WithSearch(m *search.Manager) Option {
	return func(c *config) { c.search = m }
}

// New creates a Terminal with a standalone shell whose files are a temp
// directory.
func New(t testing.TB, opts ...Option) *Terminal {
	t.Helper()

	files, err := volume.NewBackend(&volume.Volume{Name: "shelltest", URI: "file://" + t.TempDir()})
	if err != nil {
		t.Fatalf("shelltest: files: %v", err)
	}
	ofs := vfs.NewOverlayFS(nil)
	ofs.Mount("/files", files)
	cfg := config{
		width:    80,
		height:   24,
		username: "testuser",
		userFS:   ofs,
		timeout:  5 * time.Second,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.pluginCmds == nil {
		cfg.pluginCmds = map[string][]plugin.CommandDef{}
	}
	if cfg.usernames == nil {
		cfg.usernames = func() ([]string, error) { return nil, nil }
	}

	return start(t, cfg)
}

// NewFromEnv creates a Terminal wired to a cogtest environment.
func NewFromEnv(t testing.TB, env *cogtest.Env, user *cogtest.User, opts ...Option) *Terminal {
	t.Helper()

	userFS, err := cogssh.SessionFS(env.Providers, env.Providers.SSHKeys, user.Username)
	if err != nil {
		t.Fatalf("shelltest: user fs for %s: %v", user.Username, err)
	}

	cfg := config{
		width:      80,
		height:     24,
		username:   user.Username,
		userFS:     userFS,
		pluginCmds: env.Providers.Plugins.UserCommands(),
		usernames:  env.Providers.Users.Usernames,
		search:     env.Providers.Search,
		timeout:    5 * time.Second,
	}
	for _, o := range opts {
		o(&cfg)
	}

	return start(t, cfg)
}

func start(t testing.TB, cfg config) *Terminal {
	t.Helper()

	em := vt.NewSafeEmulator(cfg.width, cfg.height)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	sh := shell.New(cfg.username, cfg.userFS, cfg.pluginCmds, cfg.usernames, cfg.search, log)
	sh.SetSSHMode(cfg.width, cfg.height)

	// Pipes connecting the VT emulator to the shell:
	//   VT emulator Read() -> stdinW -> stdinR -> shell stdin
	//   shell stdout -> stdoutW -> stdoutR -> VT emulator Write()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	tm := &Terminal{
		em:      em,
		stdinW:  stdinW,
		cancel:  cancel,
		done:    done,
		timeout: cfg.timeout,
		t:       t,
	}

	// Copy goroutine: VT emulator Read() -> shell stdin
	var wg sync.WaitGroup
	wg.Go(func() {
		defer stdinW.Close()
		buf := make([]byte, 4096)
		for {
			n, err := em.Read(buf)
			if n > 0 {
				if _, werr := stdinW.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	})

	// Copy goroutine: shell stdout -> VT emulator Write()
	wg.Go(func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdoutR.Read(buf)
			if n > 0 {
				em.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	})

	// Shell goroutine
	go func() {
		defer close(done)
		sh.RunInteractive(ctx, stdinR, stdoutW, stdoutW)
		stdoutW.Close()
		stdinR.Close()
		wg.Wait()
	}()

	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("shelltest: final screen:\n%s", tm.Screen())
		}
		tm.Close()
	})

	return tm
}

// Type sends text as keystrokes without pressing Enter.
func (tm *Terminal) Type(text string) {
	tm.em.SendText(text)
}

// TypeLine sends text followed by Enter.
func (tm *Terminal) TypeLine(text string) {
	tm.em.SendText(text)
	tm.em.SendKey(vt.KeyPressEvent{Code: vt.KeyEnter})
}

// Press sends a key event.
func (tm *Terminal) Press(key vt.KeyPressEvent) {
	tm.em.SendKey(key)
}

// Ctrl sends Ctrl+key.
func (tm *Terminal) Ctrl(key rune) {
	tm.em.SendKey(vt.KeyPressEvent{Code: key, Mod: vt.ModCtrl})
}

// Resize changes the terminal dimensions. Note: the shell's readline does not
// learn about the new size; this only affects VT screen rendering.
func (tm *Terminal) Resize(width, height int) {
	tm.em.Resize(width, height)
}

// Screen returns plain text screen content with trailing whitespace trimmed.
func (tm *Terminal) Screen() string {
	return tm.em.String()
}

// ScreenRender returns the screen with ANSI styles preserved.
func (tm *Terminal) ScreenRender() string {
	return tm.em.Render()
}

// Row returns a single row's text (0-indexed), with ANSI stripped and trailing whitespace trimmed.
func (tm *Terminal) Row(y int) string {
	bounds := tm.em.Bounds()
	if y < 0 || y >= bounds.Dy() {
		return ""
	}
	var sb strings.Builder
	for x := 0; x < bounds.Dx(); x++ {
		cell := tm.em.CellAt(x, y)
		if cell != nil && cell.Content != "" {
			sb.WriteString(cell.Content)
		} else {
			sb.WriteByte(' ')
		}
	}
	return strings.TrimRight(sb.String(), " ")
}

// CursorPos returns the cursor position (x, y) where 0,0 is top-left.
func (tm *Terminal) CursorPos() (x, y int) {
	pos := tm.em.CursorPosition()
	return pos.X, pos.Y
}

// WaitText waits for substr to appear anywhere on screen. Returns the full screen text.
// Fatals on timeout.
func (tm *Terminal) WaitText(substr string) string {
	tm.t.Helper()
	deadline := time.Now().Add(tm.timeout)
	for {
		s := tm.Screen()
		if strings.Contains(s, substr) {
			return s
		}
		if time.Now().After(deadline) {
			tm.t.Fatalf("shelltest: timed out waiting for %q\nscreen:\n%s", substr, s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// WaitPrompt waits for the shell prompt ("$") to appear on screen.
func (tm *Terminal) WaitPrompt() {
	tm.t.Helper()
	tm.WaitText("$")
}

// WaitLine waits for substr to appear on any single row. Returns the matching row text.
// Fatals on timeout.
func (tm *Terminal) WaitLine(substr string) string {
	tm.t.Helper()
	deadline := time.Now().Add(tm.timeout)
	bounds := tm.em.Bounds()
	for {
		for y := 0; y < bounds.Dy(); y++ {
			row := tm.Row(y)
			if strings.Contains(row, substr) {
				return row
			}
		}
		if time.Now().After(deadline) {
			tm.t.Fatalf("shelltest: timed out waiting for line containing %q\nscreen:\n%s", substr, tm.Screen())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Close cancels the shell context, closes the emulator, and waits for the shell to exit.
func (tm *Terminal) Close() {
	tm.cancel()
	tm.em.Close()
	select {
	case <-tm.done:
	case <-time.After(5 * time.Second):
	}
}
