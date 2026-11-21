// Package shell implements a sandboxed POSIX shell interpreter for cog users.
// It uses mvdan.cc/sh/v3 with custom handlers to ensure all file operations
// are contained within the user's files directory and no OS binaries can execute.
package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync/atomic"

	"github.com/ergochat/readline"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"
)

// Shell is a sandboxed POSIX shell for a single cog user.
type Shell struct {
	username   string
	fs         vfs.FS // the session's VFS, with the user's files at /files
	pluginCmds map[string][]plugin.CommandDef
	usernames  func() ([]string, error) // lists all usernames for completion
	search     *search.Manager          // may be nil
	space      SpaceFunc                // may be nil
	log        *slog.Logger
	runner     *interp.Runner // current interpreter runner (set after newRunner)
	termWidth  int            // terminal width for SSH pty sessions (0 = unknown)
	termHeight int            // terminal height for SSH pty sessions (0 = unknown)
	sshMode    bool           // true when running over an SSH channel
	color      bool           // true during RunInteractive for ANSI color output
	switchUser SwitchUser     // nil unless the session's user may su
	suStack    []identity     // who the shell was before each su still open
}

// SetSSHMode configures the shell for use over an SSH channel with a pty.
// When set, readline uses no-op raw mode functions (the SSH client handles
// raw mode) and output translates \n to \r\n for proper terminal display.
func (s *Shell) SetSSHMode(width, height int) {
	s.sshMode = true
	s.termWidth = width
	s.termHeight = height
}

// New creates a shell for the given user on userFS, which every path the
// shell names is in: the interpreter never sees the real filesystem, and its
// working directory is a path in userFS. pluginCmds maps plugin IDs to
// their registered CLI commands. usernames is called to list usernames for
// tab completion of "username" arguments.
func New(username string, userFS vfs.FS, pluginCmds map[string][]plugin.CommandDef, usernames func() ([]string, error), searchMgr *search.Manager, log *slog.Logger) *Shell {
	return &Shell{
		username:   username,
		fs:         userFS,
		pluginCmds: pluginCmds,
		usernames:  usernames,
		search:     searchMgr,
		log:        log,
	}
}

// newRunner creates an interp.Runner configured with sandboxed handlers.
func (s *Shell) newRunner(stdin io.Reader, stdout, stderr io.Writer) (*interp.Runner, error) {
	return s.runnerAt(home, s.environ(), stdin, stdout, stderr)
}

// subRunner creates an interpreter for a command that runs others (sh, xargs,
// env). It works on a copy of the shell, so a cd it runs moves that
// interpreter rather than the session's.
func (s *Shell) subRunner(dir string, env expand.Environ, stdin io.Reader, stdout, stderr io.Writer) (*interp.Runner, error) {
	child := *s
	return child.runnerAt(dir, env, stdin, stdout, stderr)
}

func (s *Shell) runnerAt(dir string, env expand.Environ, stdin io.Reader, stdout, stderr io.Writer) (*interp.Runner, error) {
	r, err := interp.New(
		interp.StdIO(stdin, stdout, stderr),
		interp.CallHandler(s.callHandler),
		interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return s.execHandler
		}),
		interp.OpenHandler(s.openHandler),
		interp.StatHandler(s.statHandler),
		interp.ReadDirHandler2(s.readDirHandler),
		interp.Env(env),
		// Not interp.Dir: it stats the directory on the real filesystem.
		func(r *interp.Runner) error { r.Dir = dir; return nil },
	)
	if err != nil {
		return nil, err
	}
	s.runner = r
	return r, nil
}

// home is where a session starts, and where cd with no argument goes.
const home = "/files"

// toVirtualPath converts a relative or absolute shell path to a clean
// absolute VFS path. base is the interpreter's working directory.
func (s *Shell) toVirtualPath(base, p string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(base, p)
}

// callHandler intercepts all commands before execution, including shell builtins.
// We rename cd/pwd/exit so the interpreter's native builtins don't fire —
// they reach execHandler → cobra commands registered as _cd/_pwd/_exit instead.
func (s *Shell) callHandler(ctx context.Context, args []string) ([]string, error) {
	switch args[0] {
	case "cd", "pwd", "exit":
		args[0] = "_" + args[0]
		return args, nil
	case "help":
		// Rename to reach cobra's help command via execHandler.
		args[0] = "_help"
		return args, nil
	case "umask":
		return []string{"true"}, nil
	case "command":
		// "command ls ..." → strip "command" prefix, run the rest directly.
		if len(args) > 1 {
			return args[1:], nil
		}
		return []string{"true"}, nil
	}
	return args, nil
}

// Run parses and executes a single command string.
func (s *Shell) Run(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) error {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	prog, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return fmt.Errorf("parse error: %w", err)
	}

	runner, err := s.newRunner(stdin, stdout, stderr)
	if err != nil {
		return err
	}

	return runner.Run(ctx, prog)
}

// RunInteractive reads commands from stdin with line editing, tab completion,
// and command history, then executes each one.
func (s *Shell) RunInteractive(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	s.color = true
	defer func() { s.color = false }()

	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))

	// In SSH mode, wrap writers to translate \n → \r\n for proper pty display.
	shellStdout, shellStderr := stdout, stderr
	if s.sshMode {
		shellStdout = &crlfWriter{w: stdout}
		shellStderr = &crlfWriter{w: stderr}
	}

	// Set up stdin routing. In SSH mode we use a mux to prevent the
	// shell interpreter from eagerly consuming stdin (it spawns a
	// background goroutine for non-*os.File readers) and to inject
	// synthetic CPR responses for readline's DSR cursor queries.
	// In non-SSH mode, stdin is a real terminal (*os.File) so no mux
	// is needed — readline and the runner can share it directly.
	shellStdin := io.Reader(stdin)
	rlStdin := io.Reader(stdin)
	rlStdout := io.Writer(shellStdout)
	var mux *sshStdinMux
	if s.sshMode {
		var err error
		mux, err = newSSHStdinMux(stdin)
		if err != nil {
			return err
		}
		rlStdin = mux
		rlStdout = &dsrInterceptWriter{w: shellStdout, mux: mux}
		shellStdin = mux.runnerStdin()
	}

	runner, err := s.newRunner(shellStdin, shellStdout, shellStderr)
	if err != nil {
		return err
	}
	if mux != nil {
		defer mux.closeRunnerStdin()
	}

	comp := &completer{
		shell: s,
		dir:   func() string { return runner.Dir },
	}

	prompt := s.prompt(runner)
	rlCfg := &readline.Config{
		Prompt:       prompt,
		AutoComplete: comp,
		Stdin:        rlStdin,
		Stdout:       rlStdout,
		Stderr:       shellStderr,
	}

	if s.sshMode {
		w, h := s.termWidth, s.termHeight
		if w == 0 {
			w = 80
		}
		if h == 0 {
			h = 24
		}
		rlCfg.FuncIsTerminal = func() bool { return true }
		rlCfg.FuncMakeRaw = func() error { return nil }
		rlCfg.FuncExitRaw = func() error { return nil }
		rlCfg.FuncGetSize = func() (int, int) { return w, h }
		rlCfg.FuncOnWidthChanged = func(func()) {}
	}

	rlCfg.Painter = func(line []rune, _ int) []rune {
		width, _ := rlCfg.FuncGetSize()
		return comp.paint(prompt, line, width)
	}

	rl, err := readline.NewFromConfig(rlCfg)
	if err != nil {
		return err
	}
	defer rl.Close()

	for {
		line, err := rl.Readline()
		if clearErr := comp.clearShown(rlStdout); clearErr != nil {
			return fmt.Errorf("clear completion message: %w", clearErr)
		}
		if err == readline.ErrInterrupt {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(s.suStack) == 0 {
				return nil
			}
			// Ctrl-D leaves an su, as it would a nested shell.
			line = "exit"
			err = nil
		}
		if err != nil {
			return fmt.Errorf("read line: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		prog, parseErr := parser.Parse(strings.NewReader(line), "")
		if parseErr != nil {
			s.errorf(shellStderr, "parse error: %v\n", parseErr)
			continue
		}

		// While the command runs, route stdin to the runner's pipe.
		if mux != nil {
			mux.setRunnerActive(true)
		}
		runErr := runner.Run(ctx, prog)
		if mux != nil {
			mux.setRunnerActive(false)
		}
		if runner.Exited() {
			// exit N ends the session with N: runErr is its status,
			// or nil for 0.
			return runErr
		}
		if runErr != nil {
			if _, ok := errors.AsType[interp.ExitStatus](runErr); !ok {
				s.errorf(shellStderr, "error: %v\n", runErr)
			}
		}

		// Update prompt — pwd may have changed.
		prompt = s.prompt(runner)
		rl.SetPrompt(prompt)
	}
}

// prompt returns the shell prompt string for the current state.
func (s *Shell) prompt(runner *interp.Runner) string {
	user := s.username + "@cog"
	dir := runner.Dir
	if s.color {
		return ansiBoldGreen + user + ansiReset + ":" + ansiBoldBlue + dir + ansiReset + "$ "
	}
	return user + ":" + dir + " $ "
}

// crlfWriter wraps a writer and translates bare \n to \r\n for terminal display.
// This is needed for SSH pty sessions where the terminal is in raw mode.
type crlfWriter struct {
	w io.Writer
}

func (c *crlfWriter) Write(p []byte) (int, error) {
	// Replace \n with \r\n, but don't double up \r\n that's already correct.
	replaced := bytes.ReplaceAll(p, []byte("\r\n"), []byte("\x00\n"))
	replaced = bytes.ReplaceAll(replaced, []byte("\n"), []byte("\r\n"))
	replaced = bytes.ReplaceAll(replaced, []byte("\x00\n"), []byte("\r\n"))
	_, err := c.w.Write(replaced)
	// Return original length so callers don't get confused.
	return len(p), err
}

// dsrInterceptWriter wraps a writer and intercepts DSR cursor position queries
// (\x1b[6n). Instead of forwarding them to the remote terminal (which would
// require a round-trip through the SSH channel and cause hangs), it writes an
// immediate synthetic CPR response into the sshStdinMux for readline to consume.
type dsrInterceptWriter struct {
	w   io.Writer
	mux *sshStdinMux
}

func (d *dsrInterceptWriter) Write(p []byte) (int, error) {
	origLen := len(p)
	dsr := []byte("\x1b[6n")
	if bytes.Contains(p, dsr) {
		// Inject synthetic CPR response into readline's stdin.
		d.mux.Inject([]byte("\x1b[1;1R"))
		// Remove the DSR query from the output — don't send to client.
		p = bytes.ReplaceAll(p, dsr, nil)
		if len(p) == 0 {
			return origLen, nil
		}
	}
	_, err := d.w.Write(p)
	return origLen, err
}

// sshStdinMux multiplexes real SSH channel input between readline and the
// shell interpreter's runner. A background goroutine reads from the real stdin
// and dispatches data to either readline (via the Read method) or the runner's
// stdin pipe (when a command is actively running). Injected data (synthetic
// CPR responses) is always delivered to readline via the Read method.
type sshStdinMux struct {
	dataCh chan []byte
	errCh  chan error
	buf    []byte // leftover from previous read
	err    error  // the real stdin's error, once it has ended

	// Runner stdin pipe — the runner gets the read end (*os.File)
	// so the shell interpreter doesn't spawn an eager-copy goroutine.
	runnerR *os.File
	runnerW *os.File

	runnerActive atomic.Bool
}

func newSSHStdinMux(real io.Reader) (*sshStdinMux, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create stdin mux pipe: %w", err)
	}
	m := &sshStdinMux{
		dataCh:  make(chan []byte, 16),
		errCh:   make(chan error, 1),
		runnerR: r,
		runnerW: w,
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := real.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				if m.runnerActive.Load() {
					if _, werr := m.runnerW.Write(chunk); werr != nil {
						slog.Error("ssh stdin mux write", "err", werr)
					}
				} else {
					m.dataCh <- chunk
				}
			}
			if err != nil {
				m.errCh <- err
				return
			}
		}
	}()
	return m, nil
}

// runnerStdin returns the *os.File for the runner's stdin.
func (m *sshStdinMux) runnerStdin() *os.File { return m.runnerR }

// closeRunnerStdin closes both ends of the runner's stdin pipe.
func (m *sshStdinMux) closeRunnerStdin() {
	if err := errors.Join(m.runnerW.Close(), m.runnerR.Close()); err != nil {
		slog.Warn("ssh stdin mux: close runner pipe", "err", err)
	}
}

// setRunnerActive switches whether stdin data goes to the runner or readline.
func (m *sshStdinMux) setRunnerActive(active bool) {
	m.runnerActive.Store(active)
}

// Inject adds data to the readline stream (used for synthetic CPR responses).
func (m *sshStdinMux) Inject(data []byte) {
	m.dataCh <- data
}

// Read returns data for readline. Blocks until data is available from
// the real stdin (when runner is not active) or from injected CPR responses.
func (m *sshStdinMux) Read(p []byte) (int, error) {
	// Return buffered data first.
	if len(m.buf) > 0 {
		n := copy(p, m.buf)
		m.buf = m.buf[n:]
		return n, nil
	}

	// Once the real stdin has ended, every read says so: leaving an su
	// on EOF reads again, and nothing will come.
	if m.err != nil {
		return 0, m.err
	}

	// Block waiting for data from either real stdin or injected.
	select {
	case data := <-m.dataCh:
		n := copy(p, data)
		if n < len(data) {
			m.buf = data[n:]
		}
		return n, nil
	case err := <-m.errCh:
		m.err = err
		return 0, err
	}
}
