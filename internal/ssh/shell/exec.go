package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/interp"
)

// execHandler dispatches command execution via the cobra command tree.
// It never delegates to the OS — unknown commands return exit status 127.
func (s *Shell) execHandler(ctx context.Context, args []string) error {
	root := s.newRootCmd()
	root.SetContext(ctx)

	hc := interp.HandlerCtx(ctx)
	root.SetOut(hc.Stdout)
	root.SetErr(hc.Stderr)
	root.SetIn(hc.Stdin)

	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		if req, ok := errors.AsType[exitRequest](err); ok {
			return req.err
		}
		// The interpreter panics on non-ExitStatus errors, so we must
		// always convert cobra errors to ExitStatus.
		if _, ok := errors.AsType[interp.ExitStatus](err); ok {
			return err
		}
		// Cobra's "unknown command" error → exit 127.
		if strings.Contains(err.Error(), "unknown command") {
			fmt.Fprintf(hc.Stderr, "%s: command not found\n", args[0])
			return interp.ExitStatus(127)
		}
		// Other cobra errors (arg validation, etc.) → print and exit 1.
		fmt.Fprintf(hc.Stderr, "%s: %v\n", args[0], err)
		return interp.ExitStatus(1)
	}
	return nil
}
