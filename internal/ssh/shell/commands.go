package shell

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
	"mvdan.cc/sh/v3/interp"

	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"
)

func exitErr(code uint8) error { return interp.ExitStatus(code) }

// shellDirKey is a context key for passing the working directory to completion
// functions (where HandlerCtx is not available).
type shellDirKey struct{}

func withShellDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, shellDirKey{}, dir)
}

func shellDir(ctx context.Context) string {
	if d, ok := ctx.Value(shellDirKey{}).(string); ok {
		return d
	}
	return ""
}

// newRootCmd builds a fresh cobra command tree for a single invocation.
func (s *Shell) newRootCmd() *cobra.Command {
	root := &cobra.Command{
		SilenceUsage:      true,
		SilenceErrors:     true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	// Override the default usage template to remove the empty root command
	// name from Usage lines (e.g. "cat [file...]" instead of " cat [file...]").
	defaultTmpl := root.UsageTemplate()
	root.SetUsageTemplate(strings.Replace(defaultTmpl, "{{.UseLine}}", "{{trimUseLine .}}", 1))
	cobra.AddTemplateFuncs(template.FuncMap{
		"trimUseLine": func(cmd *cobra.Command) string {
			return strings.TrimSpace(cmd.UseLine())
		},
	})

	root.AddCommand(
		s.newLsCmd(),
		s.newCatCmd(),
		s.newCpCmd(),
		s.newMvCmd(),
		s.newRmCmd(),
		s.newMkdirCmd(),
		s.newTouchCmd(),
		s.newHeadCmd(),
		s.newTailCmd(),
		s.newTreeCmd(),
		s.newEchoCmd(),
		s.newWhoamiCmd(),
		s.newUnameCmd(),
		s.newClearCmd(),
		s.newDirnameCmd(),
		s.newShCmd(),
		s.newTrueCmd(),
		s.newFalseCmd(),
		s.newCdCmd(),
		s.newPwdCmd(),
		s.newExitCmd(),
		s.newSearchCmd(),
	)

	// _help is the renamed "help" (via callHandler) since the interpreter
	// intercepts "help" as a builtin. Delegates to cobra's help system.
	root.AddCommand(&cobra.Command{
		Use:    "_help [command]",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return root.Help()
			}
			target, _, err := root.Find(args)
			if err != nil || target == nil {
				return root.Help()
			}
			return target.Help()
		},
	})

	// Plugin commands.
	for pluginID, defs := range s.pluginCmds {
		parent := &cobra.Command{
			Use:   pluginID,
			Short: pluginID + " plugin commands",
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Fprintf(cmd.ErrOrStderr(), "usage: %s <command> [args...]\n", cmd.Name())
				return exitErr(1)
			},
		}
		for _, def := range defs {
			parent.AddCommand(s.buildPluginCmd(def))
		}
		root.AddCommand(parent)
	}

	return root
}

// getHC returns the interp.HandlerContext from cmd's context, or falls back
// to cmd's own IO (used during completion when there's no HandlerCtx).
func getHC(cmd *cobra.Command) (stdout, stderr io.Writer, stdin io.Reader, dir string) {
	ctx := cmd.Context()
	// Try HandlerCtx first (normal execution).
	defer func() {
		if r := recover(); r != nil {
			// No HandlerCtx — use cobra IO and shellDirKey.
			stdout = cmd.OutOrStdout()
			stderr = cmd.ErrOrStderr()
			stdin = cmd.InOrStdin()
			dir = shellDir(ctx)
		}
	}()
	hc := interp.HandlerCtx(ctx)
	return hc.Stdout, hc.Stderr, hc.Stdin, hc.Dir
}

func (s *Shell) newLsCmd() *cobra.Command {
	var showAll, showLong bool
	cmd := &cobra.Command{
		Use:               "ls [path...]",
		Short:             "List directory contents",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, dir := getHC(cmd)
			if len(args) == 0 {
				args = []string{"."}
			}

			printEntry := func(w io.Writer, name string, isDir bool, info os.FileInfo) {
				if showLong {
					fmt.Fprintf(w, "%s %8s %s %s\n",
						s.colorize(ansiYellow, info.Mode().String()),
						s.colorize(ansiGreen, fmt.Sprintf("%d", info.Size())),
						info.ModTime().Format("Jan _2 15:04"),
						s.lsName(name, isDir))
				} else {
					fmt.Fprintln(w, s.lsName(name, isDir))
				}
			}

			for _, p := range args {
				if s.fs != nil {
					if dr, ok := s.fs.(vfs.DirReader); ok {
						vp := s.toVirtualPath(dir, p)
						entries, err := dr.ReadDirEntries(vp)
						if err != nil {
							s.errorf(stderr, "ls: %v\n", err)
							return exitErr(1)
						}
						if len(args) > 1 {
							fmt.Fprintf(stdout, "%s:\n", vp)
						}
						for _, e := range entries {
							if !showAll && strings.HasPrefix(e.Name(), ".") {
								continue
							}
							if showLong {
								info, err := e.Info()
								if err != nil {
									continue
								}
								printEntry(stdout, e.Name(), e.IsDir(), info)
							} else {
								printEntry(stdout, e.Name(), e.IsDir(), nil)
							}
						}
						continue
					}
				}
				resolved, err := s.resolve(dir, p)
				if err != nil {
					s.errorf(stderr, "ls: %v\n", err)
					return exitErr(1)
				}
				entries, err := os.ReadDir(resolved)
				if err != nil {
					s.errorf(stderr, "ls: %v\n", err)
					return exitErr(1)
				}
				if len(args) > 1 {
					fmt.Fprintf(stdout, "%s:\n", s.virtualPath(resolved))
				}
				for _, e := range entries {
					if !showAll && strings.HasPrefix(e.Name(), ".") {
						continue
					}
					if showLong {
						info, err := e.Info()
						if err != nil {
							continue
						}
						printEntry(stdout, e.Name(), e.IsDir(), info)
					} else {
						printEntry(stdout, e.Name(), e.IsDir(), nil)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "Show hidden files")
	cmd.Flags().BoolVarP(&showLong, "long", "l", false, "Long format")
	return cmd
}

func (s *Shell) newCatCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "cat [file...]",
		Short:             "Concatenate and print files",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, stdin, dir := getHC(cmd)
			if len(args) == 0 {
				_, err := io.Copy(stdout, stdin)
				return err
			}
			for _, name := range args {
				if s.fs != nil {
					vp := s.toVirtualPath(dir, name)
					rc, err := s.fs.Get(vp)
					if err != nil {
						s.errorf(stderr, "cat: %s: %v\n", name, err)
						return exitErr(1)
					}
					_, err = io.Copy(stdout, rc)
					rc.Close()
					if err != nil {
						return err
					}
					continue
				}
				resolved, err := s.resolve(dir, name)
				if err != nil {
					s.errorf(stderr, "cat: %v\n", err)
					return exitErr(1)
				}
				f, err := os.Open(resolved)
				if err != nil {
					s.errorf(stderr, "cat: %s: %v\n", name, err)
					return exitErr(1)
				}
				_, err = io.Copy(stdout, f)
				f.Close()
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func (s *Shell) newCpCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "cp <src> <dst>",
		Short:             "Copy a file",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stderr, _, dir := getHC(cmd)
			if s.fs != nil {
				srcVP := s.toVirtualPath(dir, args[0])
				dstVP := s.toVirtualPath(dir, args[1])
				if fi, err := s.fs.Stat(dstVP); err == nil && fi.IsDir {
					dstVP = filepath.Join(dstVP, filepath.Base(srcVP))
				}
				rc, err := s.fs.Get(srcVP)
				if err != nil {
					s.errorf(stderr, "cp: %v\n", err)
					return exitErr(1)
				}
				defer rc.Close()
				if err := s.fs.Put(dstVP, rc); err != nil {
					s.errorf(stderr, "cp: %v\n", err)
					return exitErr(1)
				}
				return nil
			}
			src, err := s.resolve(dir, args[0])
			if err != nil {
				s.errorf(stderr, "cp: %v\n", err)
				return exitErr(1)
			}
			dst, err := s.resolve(dir, args[1])
			if err != nil {
				s.errorf(stderr, "cp: %v\n", err)
				return exitErr(1)
			}
			if info, err := os.Stat(dst); err == nil && info.IsDir() {
				dst = filepath.Join(dst, filepath.Base(src))
			}
			in, err := os.Open(src)
			if err != nil {
				s.errorf(stderr, "cp: %v\n", err)
				return exitErr(1)
			}
			defer in.Close()
			out, err := os.Create(dst)
			if err != nil {
				s.errorf(stderr, "cp: %v\n", err)
				return exitErr(1)
			}
			defer out.Close()
			if _, err := io.Copy(out, in); err != nil {
				s.errorf(stderr, "cp: %v\n", err)
				return exitErr(1)
			}
			return nil
		},
	}
}

func (s *Shell) newMvCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "mv <src> <dst>",
		Short:             "Move or rename a file",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stderr, _, dir := getHC(cmd)
			if s.fs != nil {
				srcVP := s.toVirtualPath(dir, args[0])
				dstVP := s.toVirtualPath(dir, args[1])
				if fi, err := s.fs.Stat(dstVP); err == nil && fi.IsDir {
					dstVP = filepath.Join(dstVP, filepath.Base(srcVP))
				}
				if err := s.fs.Rename(srcVP, dstVP); err != nil {
					s.errorf(stderr, "mv: %v\n", err)
					return exitErr(1)
				}
				return nil
			}
			src, err := s.resolve(dir, args[0])
			if err != nil {
				s.errorf(stderr, "mv: %v\n", err)
				return exitErr(1)
			}
			dst, err := s.resolve(dir, args[1])
			if err != nil {
				s.errorf(stderr, "mv: %v\n", err)
				return exitErr(1)
			}
			if info, err := os.Stat(dst); err == nil && info.IsDir() {
				dst = filepath.Join(dst, filepath.Base(src))
			}
			if err := os.Rename(src, dst); err != nil {
				s.errorf(stderr, "mv: %v\n", err)
				return exitErr(1)
			}
			return nil
		},
	}
}

func (s *Shell) newRmCmd() *cobra.Command {
	var recursive, force bool
	cmd := &cobra.Command{
		Use:               "rm [flags] <path...>",
		Short:             "Remove files or directories",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stderr, _, dir := getHC(cmd)
			for _, t := range args {
				if s.fs != nil {
					vp := s.toVirtualPath(dir, t)
					if vp == "/" {
						s.errorf(stderr, "rm: cannot remove root directory\n")
						return exitErr(1)
					}
					if err := s.fs.Delete(vp); err != nil {
						if !force {
							s.errorf(stderr, "rm: %v\n", err)
							return exitErr(1)
						}
					}
					continue
				}
				resolved, err := s.resolve(dir, t)
				if err != nil {
					s.errorf(stderr, "rm: %v\n", err)
					return exitErr(1)
				}
				if resolved == s.filesDir {
					s.errorf(stderr, "rm: cannot remove root directory\n")
					return exitErr(1)
				}
				if recursive {
					err = os.RemoveAll(resolved)
				} else {
					err = os.Remove(resolved)
				}
				if err != nil {
					if !force {
						s.errorf(stderr, "rm: %v\n", err)
						return exitErr(1)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Remove directories recursively")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Ignore nonexistent files")
	return cmd
}

func (s *Shell) newMkdirCmd() *cobra.Command {
	var parents bool
	cmd := &cobra.Command{
		Use:               "mkdir [flags] <dir...>",
		Short:             "Create directories",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stderr, _, dir := getHC(cmd)
			for _, d := range args {
				if s.fs != nil {
					vp := s.toVirtualPath(dir, d)
					if err := s.fs.Mkdir(vp); err != nil {
						s.errorf(stderr, "mkdir: %v\n", err)
						return exitErr(1)
					}
					continue
				}
				resolved, err := s.resolve(dir, d)
				if err != nil {
					s.errorf(stderr, "mkdir: %v\n", err)
					return exitErr(1)
				}
				if parents {
					err = os.MkdirAll(resolved, 0o755)
				} else {
					err = os.Mkdir(resolved, 0o755)
				}
				if err != nil {
					s.errorf(stderr, "mkdir: %v\n", err)
					return exitErr(1)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&parents, "parents", "p", false, "Create parent directories as needed")
	return cmd
}

func (s *Shell) newTouchCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "touch <file...>",
		Short:             "Create empty files",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stderr, _, dir := getHC(cmd)
			for _, name := range args {
				if s.fs != nil {
					if opener, ok := s.fs.(vfs.FileOpener); ok {
						vp := s.toVirtualPath(dir, name)
						f, err := opener.OpenFile(vp, os.O_CREATE|os.O_WRONLY, 0o644)
						if err != nil {
							s.errorf(stderr, "touch: %v\n", err)
							return exitErr(1)
						}
						f.Close()
						continue
					}
				}
				resolved, err := s.resolve(dir, name)
				if err != nil {
					s.errorf(stderr, "touch: %v\n", err)
					return exitErr(1)
				}
				f, err := os.OpenFile(resolved, os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					s.errorf(stderr, "touch: %v\n", err)
					return exitErr(1)
				}
				f.Close()
			}
			return nil
		},
	}
}

func (s *Shell) newHeadCmd() *cobra.Command {
	var n int
	cmd := &cobra.Command{
		Use:               "head [flags] [file...]",
		Short:             "Output the first part of files",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			return s.headTailRun(cmd, args, n, true)
		},
	}
	cmd.Flags().IntVarP(&n, "lines", "n", 10, "Number of lines")
	return cmd
}

func (s *Shell) newTailCmd() *cobra.Command {
	var n int
	cmd := &cobra.Command{
		Use:               "tail [flags] [file...]",
		Short:             "Output the last part of files",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			return s.headTailRun(cmd, args, n, false)
		},
	}
	cmd.Flags().IntVarP(&n, "lines", "n", 10, "Number of lines")
	return cmd
}

func (s *Shell) headTailRun(cmd *cobra.Command, args []string, n int, isHead bool) error {
	stdout, stderr, stdin, dir := getHC(cmd)

	readLines := func(r io.Reader) ([]string, error) {
		var lines []string
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		return lines, scanner.Err()
	}

	printLines := func(lines []string) {
		if isHead {
			if n > len(lines) {
				n = len(lines)
			}
			for _, l := range lines[:n] {
				fmt.Fprintln(stdout, l)
			}
		} else {
			start := max(len(lines)-n, 0)
			for _, l := range lines[start:] {
				fmt.Fprintln(stdout, l)
			}
		}
	}

	if len(args) == 0 {
		lines, err := readLines(stdin)
		if err != nil {
			return err
		}
		printLines(lines)
		return nil
	}

	for _, name := range args {
		if s.fs != nil {
			vp := s.toVirtualPath(dir, name)
			rc, err := s.fs.Get(vp)
			if err != nil {
				s.errorf(stderr, "%s: %v\n", name, err)
				return exitErr(1)
			}
			lines, err := readLines(rc)
			rc.Close()
			if err != nil {
				return err
			}
			printLines(lines)
			continue
		}
		resolved, err := s.resolve(dir, name)
		if err != nil {
			s.errorf(stderr, "%v\n", err)
			return exitErr(1)
		}
		f, err := os.Open(resolved)
		if err != nil {
			s.errorf(stderr, "%s: %v\n", name, err)
			return exitErr(1)
		}
		lines, err := readLines(f)
		f.Close()
		if err != nil {
			return err
		}
		printLines(lines)
	}
	return nil
}

func (s *Shell) newTreeCmd() *cobra.Command {
	var showAll bool
	cmd := &cobra.Command{
		Use:               "tree [path...]",
		Short:             "Display directory tree",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, _, _, dir := getHC(cmd)
			if len(args) == 0 {
				args = []string{"."}
			}
			var dirs, files int
			for _, root := range args {
				vp := s.toVirtualPath(dir, root)
				if len(args) > 1 {
					fmt.Fprintln(stdout, vp)
				}
				d, f := s.treeWalk(stdout, cmd.ErrOrStderr(), vp, "", showAll)
				dirs += d
				files += f
			}
			summary := fmt.Sprintf("\n%d directories, %d files\n", dirs, files)
			fmt.Fprint(stdout, s.colorize(ansiGreen, summary))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "Show hidden files")
	return cmd
}

func (s *Shell) treeWalk(stdout, stderr io.Writer, dir, prefix string, showAll bool) (dirs, files int) {
	var names []string
	var isDir []bool

	if s.fs != nil {
		if dr, ok := s.fs.(vfs.DirReader); ok {
			entries, err := dr.ReadDirEntries(dir)
			if err != nil {
				return 0, 0
			}
			for _, e := range entries {
				if !showAll && strings.HasPrefix(e.Name(), ".") {
					continue
				}
				names = append(names, e.Name())
				isDir = append(isDir, e.IsDir())
			}
		}
	} else {
		resolved, err := s.resolve(s.filesDir, dir)
		if err != nil {
			return 0, 0
		}
		entries, err := os.ReadDir(resolved)
		if err != nil {
			return 0, 0
		}
		for _, e := range entries {
			if !showAll && strings.HasPrefix(e.Name(), ".") {
				continue
			}
			names = append(names, e.Name())
			isDir = append(isDir, e.IsDir())
		}
	}

	sort.Sort(treeEntries{names: names, isDir: isDir})

	for i, name := range names {
		connector := "├── "
		childPrefix := "│   "
		if i == len(names)-1 {
			connector = "└── "
			childPrefix = "    "
		}
		displayName := name
		if isDir[i] {
			displayName = s.colorize(ansiBoldBlue, name)
		}
		fmt.Fprintf(stdout, "%s%s%s\n", prefix, s.colorize(ansiDim, connector), displayName)
		if isDir[i] {
			dirs++
			d, f := s.treeWalk(stdout, stderr, path.Join(dir, name), prefix+childPrefix, showAll)
			dirs += d
			files += f
		} else {
			files++
		}
	}
	return dirs, files
}

// treeEntries sorts directory entries: directories first, then files, each group alphabetical.
type treeEntries struct {
	names []string
	isDir []bool
}

func (t treeEntries) Len() int { return len(t.names) }
func (t treeEntries) Swap(i, j int) {
	t.names[i], t.names[j] = t.names[j], t.names[i]
	t.isDir[i], t.isDir[j] = t.isDir[j], t.isDir[i]
}
func (t treeEntries) Less(i, j int) bool {
	if t.isDir[i] != t.isDir[j] {
		return t.isDir[i]
	}
	return t.names[i] < t.names[j]
}

func (s *Shell) newEchoCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "echo [args...]",
		Short:              "Display a line of text",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, _, _, _ := getHC(cmd)
			fmt.Fprintln(stdout, strings.Join(args, " "))
			return nil
		},
	}
}

func (s *Shell) newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Print current user name",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, _, _, _ := getHC(cmd)
			fmt.Fprintln(stdout, s.username)
			return nil
		},
	}
}

func (s *Shell) newUnameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uname",
		Short: "Print system name",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, _, _, _ := getHC(cmd)
			_, err := io.WriteString(stdout, "Cog\n")
			return err
		},
	}
}

func (s *Shell) newClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Clear the terminal screen",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, _, _, _ := getHC(cmd)
			fmt.Fprint(stdout, "\033[2J\033[H")
			return nil
		},
	}
}

func (s *Shell) newDirnameCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "dirname <path>",
		Short:             "Strip last component from path",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, _, _, _ := getHC(cmd)
			_, err := io.WriteString(stdout, path.Dir(args[0]))
			return err
		},
	}
}

func (s *Shell) newShCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "sh <script>",
		Short:              "Execute a shell script",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "sh: missing operand")
				return exitErr(1)
			}
			stdout, stderr, stdin, _ := getHC(cmd)
			return s.Run(cmd.Context(), args[0], stdin, stdout, stderr)
		},
	}
}

func (s *Shell) newTrueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "true",
		Short: "Return success",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
}

func (s *Shell) newFalseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "false",
		Short: "Return failure",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return exitErr(1) },
	}
}

func (s *Shell) newCdCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "cd [dir]",
		Aliases:           []string{"_cd"},
		Short:             "Change directory",
		ValidArgsFunction: s.completeDirs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stderr, _, dir := getHC(cmd)
			target := s.cdTarget(args)

			vp := s.toVirtualPath(dir, target)
			// Validate via VFS first — show virtual paths in errors.
			if s.fs != nil {
				fi, err := s.fs.Stat(vp)
				if err != nil {
					s.errorf(stderr, "cd: no such file or directory: %s\n", vp)
					return exitErr(1)
				}
				if !fi.IsDir {
					s.errorf(stderr, "cd: not a directory: %s\n", vp)
					return exitErr(1)
				}
			}
			realPath := s.virtualToReal(vp)
			if realPath == "" {
				s.errorf(stderr, "cd: cannot cd to %s\n", vp)
				return exitErr(1)
			}
			s.runner.Dir = realPath
			return nil
		},
	}
}

// cdTarget extracts the target directory from cd arguments.
func (s *Shell) cdTarget(args []string) string {
	if len(args) == 0 {
		return "/files"
	}
	target := args[0]
	if target == "~" || target == "-" {
		return "/files"
	}
	if strings.HasPrefix(target, "~/") {
		return "/files/" + strings.TrimPrefix(target, "~/")
	}
	return target
}

func (s *Shell) newPwdCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "pwd",
		Aliases: []string{"_pwd"},
		Short:   "Print working directory",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, _, _, dir := getHC(cmd)
			fmt.Fprintln(stdout, s.virtualPath(dir))
			return nil
		},
	}
}

func (s *Shell) newExitCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "exit [code]",
		Aliases: []string{"_exit"},
		Short:   "Exit the shell",
		RunE: func(cmd *cobra.Command, args []string) error {
			code := 0
			if len(args) > 0 {
				if v, err := strconv.Atoi(args[0]); err == nil {
					code = v
				}
			}
			s.exited = true
			if code == 0 {
				return nil
			}
			return exitErr(uint8(code))
		},
	}
}

// completePaths provides tab-completion for file/directory paths.
func (s *Shell) completePaths(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return s.completePathsFiltered(cmd, toComplete, false)
}

// completeDirs provides tab-completion for directories only.
func (s *Shell) completeDirs(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return s.completePathsFiltered(cmd, toComplete, true)
}

func (s *Shell) completePathsFiltered(cmd *cobra.Command, toComplete string, dirsOnly bool) ([]cobra.Completion, cobra.ShellCompDirective) {
	dir := shellDir(cmd.Context())
	if dir == "" {
		// Try HandlerCtx.
		func() {
			defer func() { recover() }()
			hc := interp.HandlerCtx(cmd.Context())
			dir = hc.Dir
		}()
	}
	if dir == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	dirPart := ""
	prefixPart := toComplete
	if i := strings.LastIndex(toComplete, "/"); i >= 0 {
		dirPart = toComplete[:i+1]
		prefixPart = toComplete[i+1:]
	}

	var candidates []cobra.Completion

	if s.fs != nil {
		if dr, ok := s.fs.(vfs.DirReader); ok {
			vp := s.toVirtualPath(dir, dirPart)
			if dirPart == "" {
				vp = s.virtualPath(dir)
			}
			entries, err := dr.ReadDirEntries(vp)
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			for _, e := range entries {
				name := e.Name()
				if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefixPart, ".") {
					continue
				}
				if !strings.HasPrefix(name, prefixPart) {
					continue
				}
				if dirsOnly && !e.IsDir() {
					continue
				}
				display := dirPart + name
				if e.IsDir() {
					display += "/"
				}
				candidates = append(candidates, display)
			}
			return candidates, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
		}
	}

	searchDir := dir
	if dirPart != "" {
		resolved, err := s.resolve(dir, dirPart)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		searchDir = resolved
	}

	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefixPart, ".") {
			continue
		}
		if !strings.HasPrefix(name, prefixPart) {
			continue
		}
		if dirsOnly && !e.IsDir() {
			continue
		}
		display := dirPart + name
		if e.IsDir() {
			display += "/"
		}
		candidates = append(candidates, display)
	}
	return candidates, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// buildPluginCmd converts a plugin.CommandDef to a cobra.Command.
func (s *Shell) buildPluginCmd(def plugin.CommandDef) *cobra.Command {
	if len(def.Sub) > 0 {
		cmd := &cobra.Command{
			Use:   def.Name,
			Short: def.Short,
		}
		for _, sub := range def.Sub {
			cmd.AddCommand(s.buildPluginCmd(sub))
		}
		return cmd
	}

	use := def.Name
	for _, a := range def.Args {
		use += " <" + a + ">"
	}

	cmd := &cobra.Command{
		Use:   use,
		Short: def.Short,
		Args:  cobra.ExactArgs(len(def.Args)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return def.Run(cmd.Context(), args)
		},
	}

	// Set ValidArgsFunction based on arg name conventions.
	if len(def.Args) > 0 {
		cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) >= len(def.Args) {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			argName := strings.ToLower(def.Args[len(args)])
			switch argName {
			case "username", "user":
				return s.completeUsernames(toComplete)
			case "path", "file", "dir", "directory":
				return s.completePaths(cmd, args, toComplete)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	}

	return cmd
}

func (s *Shell) newSearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <query>",
		Short: "Search your files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, _ := getHC(cmd)
			if s.search == nil {
				fmt.Fprintln(stderr, "search not available")
				return exitErr(1)
			}
			query := strings.Join(args, " ")
			result, err := s.search.SearchUser(s.username, query, 20, 0)
			if err != nil {
				s.errorf(stderr, "search: %v\n", err)
				return exitErr(1)
			}
			if len(result.Hits) == 0 {
				fmt.Fprintln(stdout, "no results")
				return nil
			}
			for _, hit := range result.Hits {
				name, _ := hit.Fields["name"].(string)
				subtext, _ := hit.Fields["subtext"].(string)
				if name == "" {
					name = hit.ID
				}
				line := fmt.Sprintf("[%s] %s", hit.Source, name)
				if subtext != "" {
					line += " — " + subtext
				}
				fmt.Fprintln(stdout, line)
			}
			return nil
		},
	}
}

// completeUsernames returns username completions.
func (s *Shell) completeUsernames(toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if s.usernames == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, err := s.usernames()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var candidates []cobra.Completion
	for _, name := range names {
		if strings.HasPrefix(name, toComplete) {
			candidates = append(candidates, name)
		}
	}
	return candidates, cobra.ShellCompDirectiveNoFileComp
}
