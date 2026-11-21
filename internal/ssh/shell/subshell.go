package shell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

func (s *Shell) newShCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "sh [-c command [name [arg...]] | file [arg...]]",
		Short:              "Run a shell script",
		DisableFlagParsing: true,
		ValidArgsFunction:  s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			hc := interp.HandlerCtx(cmd.Context())
			var script, name string
			var params []string
			switch {
			case len(args) > 0 && args[0] == "-c":
				if len(args) < 2 {
					s.errorf(hc.Stderr, "sh: -c: option requires an argument\n")
					return exitErr(2)
				}
				script, name = args[1], "sh"
				if len(args) > 2 {
					name, params = args[2], args[3:]
				}
			case len(args) > 0:
				name, params = args[0], args[1:]
				data, err := s.fs.ReadFile(s.toVirtualPath(hc.Dir, name))
				if err != nil {
					s.errorf(hc.Stderr, "sh: %s: %v\n", name, err)
					return exitErr(127)
				}
				script = string(data)
			default:
				data, err := io.ReadAll(hc.Stdin)
				if err != nil {
					s.errorf(hc.Stderr, "sh: %v\n", err)
					return exitErr(1)
				}
				script, name = string(data), "sh"
			}
			prog, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), name)
			if err != nil {
				s.errorf(hc.Stderr, "sh: %v\n", err)
				return exitErr(2)
			}
			r, err := s.subRunner(hc.Dir, hc.Env, hc.Stdin, hc.Stdout, hc.Stderr)
			if err != nil {
				return err
			}
			if err := interp.Params(append([]string{"--"}, params...)...)(r); err != nil {
				return err
			}
			return subshellStatus(hc.Stderr, s, "sh", r.Run(cmd.Context(), prog))
		},
	}
}

// subshellStatus turns what Runner.Run returned into what an exec handler may
// return: the interpreter panics on anything but an exit status.
func subshellStatus(stderr io.Writer, s *Shell, name string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[interp.ExitStatus](err); ok {
		return err
	}
	s.errorf(stderr, "%s: %v\n", name, err)
	return exitErr(1)
}

// emptyInput returns a reader at end of input. It is a file because the
// interpreter keeps stdin as one, and turns any other reader into a pipe fed
// by a goroutine; a nil one reaches commands as a nil *os.File.
func emptyInput() (*os.File, func(), error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	if err := w.Close(); err != nil {
		r.Close()
		return nil, nil, err
	}
	return r, func() { r.Close() }, nil
}

// callExpr builds a command from literal words: nothing in them is expanded.
func callExpr(words []string) *syntax.CallExpr {
	args := make([]*syntax.Word, len(words))
	ws := make([]syntax.Word, len(words))
	parts := make([]syntax.WordPart, len(words))
	quoted := make([]syntax.SglQuoted, len(words))
	for i, w := range words {
		quoted[i].Value = w
		parts[i] = &quoted[i]
		ws[i].Parts = parts[i : i+1 : i+1]
		args[i] = &ws[i]
	}
	return &syntax.CallExpr{Args: args}
}

// xargsMaxChars is GNU xargs' default limit on a command line's length.
const xargsMaxChars = 128 << 10

func (s *Shell) newXargsCmd() *cobra.Command {
	var maxArgs int
	var replace, delim string
	var nulls, noRunIfEmpty, trace bool
	cmd := &cobra.Command{
		Use:   "xargs [flags] [command [arg...]]",
		Short: "Build and run commands from standard input",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			hc := interp.HandlerCtx(ctx)
			if len(args) == 0 {
				args = []string{"echo"}
			}
			sep, err := xargsDelim(nulls, delim)
			if err != nil {
				s.errorf(hc.Stderr, "xargs: %v\n", err)
				return exitErr(1)
			}
			if replace != "" {
				maxArgs = 1
			}
			// Commands get no input, as GNU xargs gives them /dev/null:
			// xargs' own input is the list of arguments.
			noInput, closeInput, err := emptyInput()
			if err != nil {
				return err
			}
			defer closeInput()
			r, err := s.subRunner(hc.Dir, hc.Env, noInput, hc.Stdout, hc.Stderr)
			if err != nil {
				return err
			}

			failed := false
			ran := false
			run := func(items []string) (stop error) {
				ran = true
				var words []string
				if replace != "" {
					words = make([]string, len(args))
					for i, a := range args {
						words[i] = strings.ReplaceAll(a, replace, items[0])
					}
				} else {
					words = append(slices.Clip(args), items...)
				}
				if trace {
					fmt.Fprintln(hc.Stderr, strings.Join(words, " "))
				}
				err := r.Run(ctx, callExpr(words))
				if err == nil {
					return nil
				}
				status, ok := errors.AsType[interp.ExitStatus](err)
				if !ok {
					s.errorf(hc.Stderr, "xargs: %s: %v\n", words[0], err)
					return exitErr(1)
				}
				switch status {
				case 255:
					s.errorf(hc.Stderr, "xargs: %s: exited with status 255; aborting\n", words[0])
					return exitErr(124)
				case 126, 127:
					return exitErr(uint8(status))
				}
				failed = true
				return nil
			}

			// A batch's items are gathered in one buffer and become one
			// string when it runs, rather than a string each.
			var pending []byte
			var ends []int
			var batch []string
			size := 0
			for _, a := range args {
				size += len(a) + 1
			}
			base := size
			flush := func() error {
				if len(ends) == 0 {
					return nil
				}
				all := string(pending)
				batch = batch[:0]
				start := 0
				for _, end := range ends {
					batch = append(batch, all[start:end])
					start = end
				}
				err := run(batch)
				pending, ends, size = pending[:0], ends[:0], base
				return err
			}
			err = readXargsItems(bufio.NewReaderSize(hc.Stdin, 64<<10), sep, replace != "", func(item []byte) error {
				if len(ends) > 0 && size+len(item)+1 > xargsMaxChars {
					if err := flush(); err != nil {
						return err
					}
				}
				pending = append(pending, item...)
				ends = append(ends, len(pending))
				size += len(item) + 1
				if maxArgs > 0 && len(ends) >= maxArgs {
					return flush()
				}
				return nil
			})
			if err == nil {
				err = flush()
			}
			if err != nil {
				if _, ok := errors.AsType[interp.ExitStatus](err); ok {
					return err
				}
				s.errorf(hc.Stderr, "xargs: %v\n", err)
				return exitErr(1)
			}
			if !ran && !noRunIfEmpty && replace == "" {
				if err := run(nil); err != nil {
					return err
				}
			}
			if failed {
				return exitErr(123)
			}
			return nil
		},
	}
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().IntVarP(&maxArgs, "max-args", "n", 0, "Use at most N arguments per command")
	cmd.Flags().StringVarP(&replace, "replace", "I", "", "Replace R in the command with each input line")
	cmd.Flags().BoolVarP(&nulls, "null", "0", false, "Items are separated by NUL, not whitespace")
	cmd.Flags().StringVarP(&delim, "delimiter", "d", "", "Items are separated by this character")
	cmd.Flags().BoolVarP(&noRunIfEmpty, "no-run-if-empty", "r", false, "Do not run the command if there is no input")
	cmd.Flags().BoolVarP(&trace, "verbose", "t", false, "Print each command on stderr before running it")
	return cmd
}

// xargsDelim returns the byte that separates items, or -1 for GNU xargs'
// default of blanks with quoting.
func xargsDelim(nulls bool, delim string) (int, error) {
	if nulls {
		return 0, nil
	}
	switch delim {
	case "":
		return -1, nil
	case `\n`:
		return '\n', nil
	case `\t`:
		return '\t', nil
	case `\0`:
		return 0, nil
	case `\\`:
		return '\\', nil
	}
	if len(delim) != 1 {
		return 0, fmt.Errorf("invalid delimiter %q", delim)
	}
	return int(delim[0]), nil
}

// readXargsItems calls fn for each item of r, which is valid until fn
// returns. With sep >= 0 items are split on that byte and taken as they are.
// Otherwise they are split on blanks, or only on newlines when lines is set
// (-I), and quotes and backslashes are interpreted as GNU xargs does.
func readXargsItems(r *bufio.Reader, sep int, lines bool, fn func([]byte) error) error {
	if sep >= 0 {
		var long []byte
		for {
			item, err := r.ReadSlice(byte(sep))
			if err == bufio.ErrBufferFull {
				long = append(long[:0], item...)
				for err == bufio.ErrBufferFull {
					item, err = r.ReadSlice(byte(sep))
					long = append(long, item...)
				}
				item = long
			}
			if err != nil && err != io.EOF {
				return err
			}
			if len(item) > 0 && item[len(item)-1] == byte(sep) {
				item = item[:len(item)-1]
			} else if err == io.EOF && len(item) == 0 {
				return nil
			}
			if ferr := fn(item); ferr != nil {
				return ferr
			}
			if err == io.EOF {
				return nil
			}
		}
	}
	var item []byte
	inItem := false
	var quote byte
	for {
		c, err := r.ReadByte()
		if err == io.EOF {
			if quote != 0 {
				return fmt.Errorf("unmatched %s quote", quoteName(quote))
			}
			if inItem {
				return fn(item)
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\n' {
				return fmt.Errorf("unmatched %s quote", quoteName(quote))
			} else {
				item = append(item, c)
			}
		case c == '\'' || c == '"':
			quote, inItem = c, true
		case c == '\\':
			next, err := r.ReadByte()
			if err != nil && err != io.EOF {
				return err
			}
			if err == nil {
				item = append(item, next)
			}
			inItem = true
		case c == '\n' || (!lines && (c == ' ' || c == '\t')):
			if inItem {
				if err := fn(item); err != nil {
					return err
				}
			}
			item, inItem = item[:0], false
		case lines && !inItem && (c == ' ' || c == '\t'):
		default:
			item = append(item, c)
			inItem = true
		}
	}
}

func quoteName(q byte) string {
	if q == '"' {
		return "double"
	}
	return "single"
}

func (s *Shell) newEnvCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "env [-i] [-u name] [name=value...] [command [arg...]]",
		Short:              "Print the environment, or run a command in a changed one",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			hc := interp.HandlerCtx(cmd.Context())
			ignoreEnv := false
			var unset []string
		options:
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				switch a := args[0]; {
				case a == "-i" || a == "-" || a == "--ignore-environment":
					ignoreEnv = true
				case a == "-u" && len(args) > 1:
					unset = append(unset, args[1])
					args = args[1:]
				case strings.HasPrefix(a, "-u"):
					unset = append(unset, a[2:])
				case a == "--":
					args = args[1:]
					break options
				default:
					s.errorf(hc.Stderr, "env: invalid option %s\n", a)
					return exitErr(125)
				}
				args = args[1:]
			}
			var vars []string
			names := map[string]int{}
			set := func(pair string) {
				name, _, _ := strings.Cut(pair, "=")
				if i, ok := names[name]; ok {
					vars[i] = pair
					return
				}
				names[name] = len(vars)
				vars = append(vars, pair)
			}
			if !ignoreEnv {
				hc.Env.Each(func(name string, vr expand.Variable) bool {
					if vr.Exported && vr.Kind == expand.String && !slices.Contains(unset, name) {
						set(name + "=" + vr.Str)
					}
					return true
				})
			}
			for len(args) > 0 && strings.Contains(args[0], "=") && !strings.HasPrefix(args[0], "=") {
				set(args[0])
				args = args[1:]
			}
			if len(args) == 0 {
				w := bufio.NewWriter(hc.Stdout)
				for _, v := range vars {
					w.WriteString(v)
					w.WriteByte('\n')
				}
				return w.Flush()
			}
			r, err := s.subRunner(hc.Dir, expand.ListEnviron(vars...), hc.Stdin, hc.Stdout, hc.Stderr)
			if err != nil {
				return err
			}
			return subshellStatus(hc.Stderr, s, "env", r.Run(cmd.Context(), callExpr(args)))
		},
	}
}

func (s *Shell) newWhichCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "which <command...>",
		Short: "Show whether each name is a command or a shell builtin",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, _ := getHC(cmd)
			missing := false
			for _, name := range args {
				switch {
				case interp.IsBuiltin(name) || name == "help":
					fmt.Fprintf(stdout, "%s: shell builtin\n", name)
				case s.isCommand(name):
					fmt.Fprintln(stdout, name)
				default:
					s.errorf(stderr, "which: no %s\n", name)
					missing = true
				}
			}
			if missing {
				return exitErr(1)
			}
			return nil
		},
	}
}

func (s *Shell) isCommand(name string) bool {
	if strings.HasPrefix(name, "_") {
		return false
	}
	if _, ok := commandIndex()[name]; ok {
		return true
	}
	_, ok := s.pluginCmds[name]
	return ok
}
