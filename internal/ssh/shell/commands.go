package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
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
		s.newLnCmd(),
		s.newUnlinkCmd(),
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
	var showAll, showLong, dirOnly, classify, onePerLine, deref bool
	cmd := &cobra.Command{
		Use:               "ls [path...]",
		Short:             "List directory contents",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, dir := getHC(cmd)
			if len(args) == 0 {
				args = []string{"."}
			}
			// An entry that can't be stat'ed is reported and skipped; the
			// listing goes on, but the command fails, as GNU ls does.
			failed := false

			formatName := func(name string, isDir bool) string {
				if classify && isDir {
					return s.lsName(name, isDir) + "/"
				}
				return s.lsName(name, isDir)
			}

			printEntry := func(w io.Writer, name string, isDir bool, info os.FileInfo) {
				if showLong {
					fmt.Fprintf(w, "%s %8s %s %s\n",
						s.colorize(ansiYellow, info.Mode().String()),
						s.colorize(ansiGreen, fmt.Sprintf("%d", info.Size())),
						info.ModTime().Format("Jan _2 15:04"),
						formatName(name, isDir))
				} else {
					fmt.Fprintln(w, formatName(name, isDir))
				}
			}

			dr, err := s.dirReader()
			if err != nil {
				s.errorf(stderr, "ls: %v\n", err)
				return exitErr(1)
			}
			for _, p := range args {
				vp := s.toVirtualPath(dir, p)
				if dirOnly {
					info, err := dr.StatInfo(vp)
					if err != nil {
						s.errorf(stderr, "ls: %v\n", err)
						return exitErr(1)
					}
					printEntry(stdout, path.Base(vp), info.IsDir(), info)
					continue
				}
				entries, err := dr.ReadDirEntries(vp)
				if err != nil {
					// Not a directory — stat as a file.
					info, serr := dr.StatInfo(vp)
					if serr != nil {
						s.errorf(stderr, "ls: %v\n", err)
						return exitErr(1)
					}
					printEntry(stdout, path.Base(vp), info.IsDir(), info)
					continue
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
							s.errorf(stderr, "ls: %s: %v\n", e.Name(), err)
							failed = true
							continue
						}
						printEntry(stdout, e.Name(), e.IsDir(), info)
					} else {
						printEntry(stdout, e.Name(), e.IsDir(), nil)
					}
				}
			}
			if failed {
				return exitErr(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "Show hidden files")
	cmd.Flags().BoolVarP(&showLong, "long", "l", false, "Long format")
	cmd.Flags().BoolVarP(&dirOnly, "directory", "d", false, "List directories themselves, not their contents")
	cmd.Flags().BoolVarP(&classify, "classify", "F", false, "Append / to directories")
	cmd.Flags().BoolVarP(&onePerLine, "one", "1", false, "One entry per line")
	cmd.Flags().BoolVarP(&deref, "dereference", "L", false, "Follow symbolic links")
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
				rc, err := s.fs.Get(s.toVirtualPath(dir, name))
				if err != nil {
					s.errorf(stderr, "cat: %s: %v\n", name, err)
					return exitErr(1)
				}
				_, err = io.Copy(stdout, rc)
				if cerr := rc.Close(); err == nil {
					err = cerr
				}
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func (s *Shell) newCpCmd() *cobra.Command {
	var recursive bool
	cmd := &cobra.Command{
		Use:               "cp [flags] <src...> <dst>",
		Short:             "Copy files and directories",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stderr, _, dir := getHC(cmd)
			return s.eachTarget(stderr, "cp", dir, args, func(src, dst string) error {
				fi, err := s.fs.Stat(src)
				if err != nil {
					return err
				}
				if fi.IsDir {
					if !recursive {
						return fmt.Errorf("-r not specified; omitting directory %s", src)
					}
					if dst == src || strings.HasPrefix(dst, src+"/") {
						return fmt.Errorf("cannot copy %s into itself", src)
					}
				}
				return s.copyTree(src, dst, fi)
			})
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Copy directories recursively")
	cmd.Flags().BoolVarP(&recursive, "R", "R", false, "Same as -r")
	_ = cmd.Flags().MarkHidden("R")
	return cmd
}

func (s *Shell) copyTree(src, dst string, fi *vfs.FileInfo) error {
	if !fi.IsDir {
		rc, err := s.fs.Get(src)
		if err != nil {
			return err
		}
		defer rc.Close()
		return s.fs.Put(dst, rc)
	}
	if err := s.fs.Mkdir(dst); err != nil {
		return err
	}
	entries, err := s.fs.List(src)
	if err != nil {
		return err
	}
	for i := range entries {
		e := &entries[i]
		if err := s.copyTree(path.Join(src, e.Name), path.Join(dst, e.Name), e); err != nil {
			return err
		}
	}
	return nil
}

func (s *Shell) newMvCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "mv <src...> <dst>",
		Short:             "Move or rename files",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stderr, _, dir := getHC(cmd)
			return s.eachTarget(stderr, "mv", dir, args, s.fs.Rename)
		},
	}
}

// eachTarget applies op to each source in args and its destination, the last
// arg. With several sources the destination must be a directory. A failed
// source is reported and the rest still run, as coreutils does.
func (s *Shell) eachTarget(stderr io.Writer, name, dir string, args []string, op func(src, dst string) error) error {
	srcs, target := args[:len(args)-1], s.toVirtualPath(dir, args[len(args)-1])
	fi, err := s.fs.Stat(target)
	if err != nil && !isNotExist(err) {
		s.errorf(stderr, "%s: %v\n", name, err)
		return exitErr(1)
	}
	intoDir := err == nil && fi.IsDir
	if len(srcs) > 1 && !intoDir {
		s.errorf(stderr, "%s: target %s is not a directory\n", name, args[len(args)-1])
		return exitErr(1)
	}
	failed := false
	for _, src := range srcs {
		srcVP := s.toVirtualPath(dir, src)
		dst := target
		if intoDir {
			dst = path.Join(target, path.Base(srcVP))
		}
		if err := op(srcVP, dst); err != nil {
			s.errorf(stderr, "%s: %v\n", name, err)
			failed = true
		}
	}
	if failed {
		return exitErr(1)
	}
	return nil
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
				vp := s.toVirtualPath(dir, t)
				if vp == "/" || vp == home {
					s.errorf(stderr, "rm: cannot remove %s\n", vp)
					return exitErr(1)
				}
				// A link is a mount: removing it unmounts, and what it
				// held stays where it is.
				if mounted, err := s.isLink(vp); err != nil {
					s.errorf(stderr, "rm: %v\n", err)
					return exitErr(1)
				} else if mounted {
					if err := s.unlink(vp); err != nil {
						s.errorf(stderr, "rm: %v\n", err)
						return exitErr(1)
					}
					continue
				}
				// Deleting a directory takes everything in it, and over
				// SSH it does not go to the trash.
				fi, err := s.fs.Stat(vp)
				if err != nil {
					if force && isNotExist(err) {
						continue
					}
					s.errorf(stderr, "rm: %v\n", err)
					return exitErr(1)
				}
				if fi.IsDir && !recursive {
					s.errorf(stderr, "rm: cannot remove %s: is a directory\n", t)
					return exitErr(1)
				}
				if err := s.fs.Delete(vp); err != nil && !(force && isNotExist(err)) {
					s.errorf(stderr, "rm: %v\n", err)
					return exitErr(1)
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
				vp := s.toVirtualPath(dir, d)
				if !parents {
					// The VFS makes missing parents; plain mkdir does not,
					// and refuses what exists.
					if _, err := s.fs.Stat(vp); err == nil {
						s.errorf(stderr, "mkdir: %s: file exists\n", d)
						return exitErr(1)
					} else if !isNotExist(err) {
						s.errorf(stderr, "mkdir: %v\n", err)
						return exitErr(1)
					}
					if fi, err := s.fs.Stat(path.Dir(vp)); err != nil || !fi.IsDir {
						s.errorf(stderr, "mkdir: %s: no such directory\n", path.Dir(d))
						return exitErr(1)
					}
				}
				if err := s.fs.Mkdir(vp); err != nil {
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
			opener, ok := s.fs.(vfs.FileOpener)
			if !ok {
				s.errorf(stderr, "touch: %v\n", errors.ErrUnsupported)
				return exitErr(1)
			}
			for _, name := range args {
				f, err := opener.OpenFile(s.toVirtualPath(dir, name), os.O_CREATE|os.O_WRONLY, 0o644)
				if err == nil {
					err = f.Close()
				}
				if err != nil {
					s.errorf(stderr, "touch: %v\n", err)
					return exitErr(1)
				}
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
		rc, err := s.fs.Get(s.toVirtualPath(dir, name))
		if err != nil {
			s.errorf(stderr, "%s: %v\n", name, err)
			return exitErr(1)
		}
		lines, err := readLines(rc)
		if cerr := rc.Close(); err == nil {
			err = cerr
		}
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
			_, stderr, _, _ := getHC(cmd)
			var dirs, files int
			ok := true
			for _, root := range args {
				vp := s.toVirtualPath(dir, root)
				if len(args) > 1 {
					fmt.Fprintln(stdout, vp)
				}
				d, f, rootOK := s.treeWalk(stdout, stderr, vp, "", showAll)
				dirs += d
				files += f
				ok = ok && rootOK
			}
			summary := fmt.Sprintf("\n%d directories, %d files\n", dirs, files)
			fmt.Fprint(stdout, s.colorize(ansiGreen, summary))
			if !ok {
				return exitErr(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "Show hidden files")
	return cmd
}

// treeWalk prints dir's subtree. A directory it can't read is reported on
// stderr and skipped; ok is false if that happened anywhere below dir.
func (s *Shell) treeWalk(stdout, stderr io.Writer, dir, prefix string, showAll bool) (dirs, files int, ok bool) {
	var names []string
	var isDir []bool

	dr, err := s.dirReader()
	if err != nil {
		s.errorf(stderr, "tree: %v\n", err)
		return 0, 0, false
	}
	entries, err := dr.ReadDirEntries(dir)
	if err != nil {
		s.errorf(stderr, "tree: %s: %v\n", dir, err)
		return 0, 0, false
	}
	for _, e := range entries {
		if !showAll && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, e.Name())
		isDir = append(isDir, e.IsDir())
	}

	sort.Sort(treeEntries{names: names, isDir: isDir})

	ok = true

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
			d, f, childOK := s.treeWalk(stdout, stderr, path.Join(dir, name), prefix+childPrefix, showAll)
			dirs += d
			files += f
			ok = ok && childOK
		} else {
			files++
		}
	}
	return dirs, files, ok
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
			fi, err := s.fs.Stat(vp)
			if isNotExist(err) {
				s.errorf(stderr, "cd: no such file or directory: %s\n", vp)
				return exitErr(1)
			}
			if err != nil {
				s.errorf(stderr, "cd: %s: %v\n", vp, err)
				return exitErr(1)
			}
			if !fi.IsDir {
				s.errorf(stderr, "cd: not a directory: %s\n", vp)
				return exitErr(1)
			}
			s.runner.Dir = vp
			return nil
		},
	}
}

// cdTarget extracts the target directory from cd arguments.
func (s *Shell) cdTarget(args []string) string {
	if len(args) == 0 {
		return home
	}
	target := args[0]
	if target == "~" || target == "-" {
		return home
	}
	if after, ok := strings.CutPrefix(target, "~/"); ok {
		return home + "/" + after
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
			fmt.Fprintln(stdout, dir)
			return nil
		},
	}
}

func (s *Shell) newExitCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "exit [code]",
		Aliases: []string{"_exit"},
		Short:   "Exit the shell",
		Args:    cobra.MaximumNArgs(1),
		// A negative status ("exit -1") is an argument, not a flag.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The interpreter's own exit is what stops the rest of the
			// script and makes Runner.Exited report it. It takes the status
			// modulo 256, as POSIX shells do, and with no argument the last
			// command's status.
			builtin := []string{"exit"}
			if len(args) > 0 {
				code := args[0]
				if _, err := strconv.Atoi(code); err != nil {
					// As bash does: exit anyway, with status 2.
					_, stderr, _, _ := getHC(cmd)
					s.errorf(stderr, "exit: %s: numeric argument required\n", code)
					code = "2"
				}
				builtin = append(builtin, code)
			}
			return exitRequest{interp.HandlerCtx(cmd.Context()).Builtin(cmd.Context(), builtin)}
		},
	}
}

// exitRequest carries the interpreter's exit out through cobra. execHandler
// returns what it holds unchanged: turning it into a plain status, as it does
// other errors, would lose the instruction to stop the shell.
type exitRequest struct{ err error }

func (e exitRequest) Error() string { return e.err.Error() }
func (e exitRequest) Unwrap() error { return e.err }

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

	dr, err := s.dirReader()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	vp := dir
	if dirPart != "" {
		vp = s.toVirtualPath(dir, dirPart)
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

	var use strings.Builder
	use.WriteString(def.Name)
	for _, a := range def.Args {
		use.WriteString(" <" + a + ">")
	}

	cmd := &cobra.Command{
		Use:   use.String(),
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
		// Completion has nowhere to show an error; offer nothing.
		s.log.Warn("shell: complete usernames", "err", err)
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

// isNotExist reports whether err means the path doesn't exist, from the
// local filesystem or a VFS backend.
func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, vfs.ErrNotFound)
}
