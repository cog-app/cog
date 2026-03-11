package shell

import (
	"context"
	"fmt"

	"mvdan.cc/sh/v3/interp"

	"git.duckfam.us/jj-software/cog/plugin"
)

// execHandler dispatches command execution to builtins or plugin commands.
// It never delegates to the OS — unknown commands return exit status 127.
func (s *Shell) execHandler(ctx context.Context, args []string) error {
	name := args[0]

	// Check built-in commands first.
	if fn, ok := s.builtins[name]; ok {
		return fn(ctx, args[1:])
	}

	// Check plugin commands: args[0] is the plugin ID, rest are subcommand + args.
	if cmds, ok := s.pluginCmds[name]; ok {
		return runPluginCmd(ctx, cmds, args[1:])
	}

	hc := interp.HandlerCtx(ctx)
	fmt.Fprintf(hc.Stderr, "%s: command not found\n", name)
	return interp.ExitStatus(127)
}

// runPluginCmd finds and executes a plugin command by walking the sub-command tree.
func runPluginCmd(ctx context.Context, cmds []plugin.CommandDef, args []string) error {
	if len(args) == 0 {
		hc := interp.HandlerCtx(ctx)
		fmt.Fprintf(hc.Stderr, "usage: <plugin> <command> [args...]\n")
		return interp.ExitStatus(1)
	}

	for _, cmd := range cmds {
		if cmd.Name != args[0] {
			continue
		}
		// If this command has sub-commands, recurse.
		if len(cmd.Sub) > 0 {
			return runPluginCmd(ctx, cmd.Sub, args[1:])
		}
		if cmd.Run != nil {
			cmdArgs := args[1:]
			if len(cmdArgs) != len(cmd.Args) {
				hc := interp.HandlerCtx(ctx)
				fmt.Fprintf(hc.Stderr, "usage: %s", cmd.Name)
				for _, a := range cmd.Args {
					fmt.Fprintf(hc.Stderr, " <%s>", a)
				}
				fmt.Fprintln(hc.Stderr)
				return interp.ExitStatus(1)
			}
			return cmd.Run(ctx, cmdArgs)
		}
	}

	hc := interp.HandlerCtx(ctx)
	fmt.Fprintf(hc.Stderr, "%s: unknown subcommand\n", args[0])
	return interp.ExitStatus(127)
}
