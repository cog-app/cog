// Package shell implements a sandboxed POSIX shell interpreter for cog users.
// It uses mvdan.cc/sh/v3 with custom handlers to ensure all file operations
// are contained within the user's files directory and no OS binaries can execute.
package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"git.duckfam.us/jj-software/cog/plugin"
)

// Shell is a sandboxed POSIX shell for a single cog user.
type Shell struct {
	username   string
	filesDir   string
	pluginCmds map[string][]plugin.CommandDef
	log        *slog.Logger
	builtins   map[string]builtinFunc
	exited     bool
}

// New creates a shell for the given user. filesDir is the absolute path to the
// user's files directory on the real filesystem. pluginCmds maps plugin IDs to
// their registered CLI commands.
func New(username, filesDir string, pluginCmds map[string][]plugin.CommandDef, log *slog.Logger) *Shell {
	// Resolve symlinks so containment checks and virtual path mapping
	// work consistently with paths returned by the interpreter.
	if resolved, err := filepath.EvalSymlinks(filesDir); err == nil {
		filesDir = resolved
	}
	s := &Shell{
		username:   username,
		filesDir:   filesDir,
		pluginCmds: pluginCmds,
		log:        log,
	}
	s.initBuiltins()
	return s
}

// newRunner creates an interp.Runner configured with sandboxed handlers.
func (s *Shell) newRunner(stdin io.Reader, stdout, stderr io.Writer) (*interp.Runner, error) {
	return interp.New(
		interp.StdIO(stdin, stdout, stderr),
		// CallHandler intercepts shell builtins (pwd) to display virtual paths.
		interp.CallHandler(s.callHandler),
		interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return s.execHandler
		}),
		interp.OpenHandler(s.openHandler),
		interp.StatHandler(s.statHandler),
		interp.ReadDirHandler2(s.readDirHandler),
		interp.Env(s.environ()),
		interp.Dir(s.filesDir),
	)
}

// callHandler intercepts all commands before execution, including shell builtins.
// We use it to replace "pwd" with our virtual-path-aware version.
func (s *Shell) callHandler(ctx context.Context, args []string) ([]string, error) {
	if args[0] == "pwd" {
		hc := interp.HandlerCtx(ctx)
		fmt.Fprintln(hc.Stdout, s.virtualPath(hc.Dir))
		// Replace with "true" so the interpreter's built-in pwd doesn't also run.
		return []string{"true"}, nil
	}
	return args, nil
}

// Run parses and executes a single command string.
func (s *Shell) Run(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) error {
	parser := syntax.NewParser(syntax.Variant(syntax.LangPOSIX))
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

// RunInteractive reads commands line-by-line from stdin and executes each one.
func (s *Shell) RunInteractive(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	parser := syntax.NewParser(syntax.Variant(syntax.LangPOSIX))
	runner, err := s.newRunner(stdin, stdout, stderr)
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdin)
	for {
		fmt.Fprintf(stdout, "%s@cog:%s$ ", s.username, s.virtualPath(runner.Dir))

		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		prog, err := parser.Parse(strings.NewReader(line), "")
		if err != nil {
			fmt.Fprintf(stderr, "parse error: %v\n", err)
			continue
		}

		if err := runner.Run(ctx, prog); err != nil {
			if s.exited {
				return nil
			}
			var exit interp.ExitStatus
			if !errors.As(err, &exit) {
				fmt.Fprintf(stderr, "error: %v\n", err)
			}
		}
	}

	return scanner.Err()
}
