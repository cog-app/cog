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

	"git.duckfam.us/jj-software/cog/vfs"

	"mvdan.cc/sh/v3/interp"
)

func exitErr(code uint8) error { return interp.ExitStatus(code) }

type builtinFunc func(ctx context.Context, args []string) error

func (s *Shell) initBuiltins() {
	s.builtins = map[string]builtinFunc{
		"sh":     s.cmdSh,
		"ls":     s.cmdLs,
		"cd":     s.cmdCd,
		"pwd":    s.cmdPwd,
		"cat":    s.cmdCat,
		"head":   s.cmdHead,
		"tail":   s.cmdTail,
		"cp":     s.cmdCp,
		"mv":     s.cmdMv,
		"rm":     s.cmdRm,
		"mkdir":  s.cmdMkdir,
		"touch":  s.cmdTouch,
		"echo":   s.cmdEcho,
		"whoami": s.cmdWhoami,
		"help":   s.cmdHelp,
		"exit":   s.cmdExit,
		"clear":  s.cmdClear,
		"true":   s.cmdTrue,
		"false":  s.cmdFalse,
		"uname": func(ctx context.Context, args []string) error {
			hc := interp.HandlerCtx(ctx)
			_, err := io.WriteString(hc.Stdout, "Cog\n")
			return err
		},
		"dirname": func(ctx context.Context, args []string) error {
			hc := interp.HandlerCtx(ctx)
			_, err := io.WriteString(hc.Stdout, path.Dir(args[0]))
			return err
		},
	}
}

func (s *Shell) cmdSh(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	return s.Run(ctx, args[1], hc.Stdin, hc.Stdout, hc.Stderr)
}

func (s *Shell) cmdLs(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	var showAll, showLong bool
	var paths []string
	for _, arg := range args {
		switch {
		case arg == "-a":
			showAll = true
		case arg == "-l":
			showLong = true
		case arg == "-la" || arg == "-al":
			showAll = true
			showLong = true
		case strings.HasPrefix(arg, "-"):
			// ignore other flags
		default:
			paths = append(paths, arg)
		}
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}

	for _, p := range paths {
		if s.fs != nil {
			if dr, ok := s.fs.(vfs.DirReader); ok {
				vp := s.toVirtualPath(hc.Dir, p)
				entries, err := dr.ReadDirEntries(vp)
				if err != nil {
					fmt.Fprintf(hc.Stderr, "ls: %v\n", err)
					return exitErr(1)
				}
				if len(paths) > 1 {
					fmt.Fprintf(hc.Stdout, "%s:\n", vp)
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
						fmt.Fprintf(hc.Stdout, "%s %8d %s %s\n",
							info.Mode(), info.Size(), info.ModTime().Format("Jan _2 15:04"), e.Name())
					} else {
						fmt.Fprintln(hc.Stdout, e.Name())
					}
				}
				continue
			}
		}

		resolved, err := s.resolve(hc.Dir, p)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "ls: %v\n", err)
			return exitErr(1)
		}

		entries, err := os.ReadDir(resolved)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "ls: %v\n", err)
			return exitErr(1)
		}

		if len(paths) > 1 {
			fmt.Fprintf(hc.Stdout, "%s:\n", s.virtualPath(resolved))
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
				fmt.Fprintf(hc.Stdout, "%s %8d %s %s\n",
					info.Mode(), info.Size(), info.ModTime().Format("Jan _2 15:04"), e.Name())
			} else {
				fmt.Fprintln(hc.Stdout, e.Name())
			}
		}
	}
	return nil
}

func (s *Shell) cmdCd(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	target := "/"
	if len(args) > 0 {
		target = args[0]
	}

	if s.fs != nil {
		vp := s.toVirtualPath(hc.Dir, target)
		fi, err := s.fs.Stat(vp)
		if err != nil || !fi.IsDir {
			fmt.Fprintf(hc.Stderr, "cd: not a directory: %s\n", target)
			return exitErr(1)
		}
		return nil
	}

	resolved, err := s.resolve(hc.Dir, target)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "cd: %v\n", err)
		return exitErr(1)
	}

	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(hc.Stderr, "cd: not a directory: %s\n", target)
		return exitErr(1)
	}

	return nil
}

func (s *Shell) cmdPwd(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	fmt.Fprintln(hc.Stdout, s.virtualPath(hc.Dir))
	return nil
}

func (s *Shell) cmdCat(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	if len(args) == 0 {
		_, err := io.Copy(hc.Stdout, hc.Stdin)
		return err
	}

	for _, name := range args {
		if s.fs != nil {
			vp := s.toVirtualPath(hc.Dir, name)
			rc, err := s.fs.Get(vp)
			if err != nil {
				fmt.Fprintf(hc.Stderr, "cat: %s: %v\n", name, err)
				return exitErr(1)
			}
			_, err = io.Copy(hc.Stdout, rc)
			rc.Close()
			if err != nil {
				return err
			}
			continue
		}

		resolved, err := s.resolve(hc.Dir, name)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "cat: %v\n", err)
			return exitErr(1)
		}
		f, err := os.Open(resolved)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "cat: %s: %v\n", name, err)
			return exitErr(1)
		}
		_, err = io.Copy(hc.Stdout, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Shell) cmdHead(ctx context.Context, args []string) error {
	return s.headTail(ctx, args, true)
}

func (s *Shell) cmdTail(ctx context.Context, args []string) error {
	return s.headTail(ctx, args, false)
}

func (s *Shell) headTail(ctx context.Context, args []string, isHead bool) error {
	hc := interp.HandlerCtx(ctx)
	n := 10
	var files []string

	for i := 0; i < len(args); i++ {
		if args[i] == "-n" && i+1 < len(args) {
			if v, err := strconv.Atoi(args[i+1]); err == nil {
				n = v
			}
			i++
		} else if !strings.HasPrefix(args[i], "-") {
			files = append(files, args[i])
		}
	}

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
				fmt.Fprintln(hc.Stdout, l)
			}
		} else {
			start := max(len(lines)-n, 0)
			for _, l := range lines[start:] {
				fmt.Fprintln(hc.Stdout, l)
			}
		}
	}

	if len(files) == 0 {
		lines, err := readLines(hc.Stdin)
		if err != nil {
			return err
		}
		printLines(lines)
		return nil
	}

	for _, name := range files {
		if s.fs != nil {
			vp := s.toVirtualPath(hc.Dir, name)
			rc, err := s.fs.Get(vp)
			if err != nil {
				fmt.Fprintf(hc.Stderr, "%s: %v\n", name, err)
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

		resolved, err := s.resolve(hc.Dir, name)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "%v\n", err)
			return exitErr(1)
		}
		f, err := os.Open(resolved)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "%s: %v\n", name, err)
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

func (s *Shell) cmdCp(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	if len(args) < 2 {
		fmt.Fprintln(hc.Stderr, "cp: missing operand")
		return exitErr(1)
	}

	if s.fs != nil {
		srcVP := s.toVirtualPath(hc.Dir, args[0])
		dstVP := s.toVirtualPath(hc.Dir, args[1])
		// If dst is an existing directory, copy into it.
		if fi, err := s.fs.Stat(dstVP); err == nil && fi.IsDir {
			dstVP = filepath.Join(dstVP, filepath.Base(srcVP))
		}
		rc, err := s.fs.Get(srcVP)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "cp: %v\n", err)
			return exitErr(1)
		}
		defer rc.Close()
		if err := s.fs.Put(dstVP, rc); err != nil {
			fmt.Fprintf(hc.Stderr, "cp: %v\n", err)
			return exitErr(1)
		}
		return nil
	}

	src, err := s.resolve(hc.Dir, args[0])
	if err != nil {
		fmt.Fprintf(hc.Stderr, "cp: %v\n", err)
		return exitErr(1)
	}
	dst, err := s.resolve(hc.Dir, args[1])
	if err != nil {
		fmt.Fprintf(hc.Stderr, "cp: %v\n", err)
		return exitErr(1)
	}

	if info, err := os.Stat(dst); err == nil && info.IsDir() {
		dst = filepath.Join(dst, filepath.Base(src))
	}

	in, err := os.Open(src)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "cp: %v\n", err)
		return exitErr(1)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "cp: %v\n", err)
		return exitErr(1)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		fmt.Fprintf(hc.Stderr, "cp: %v\n", err)
		return exitErr(1)
	}
	return nil
}

func (s *Shell) cmdMv(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	if len(args) < 2 {
		fmt.Fprintln(hc.Stderr, "mv: missing operand")
		return exitErr(1)
	}

	if s.fs != nil {
		srcVP := s.toVirtualPath(hc.Dir, args[0])
		dstVP := s.toVirtualPath(hc.Dir, args[1])
		if fi, err := s.fs.Stat(dstVP); err == nil && fi.IsDir {
			dstVP = filepath.Join(dstVP, filepath.Base(srcVP))
		}
		if err := s.fs.Rename(srcVP, dstVP); err != nil {
			fmt.Fprintf(hc.Stderr, "mv: %v\n", err)
			return exitErr(1)
		}
		return nil
	}

	src, err := s.resolve(hc.Dir, args[0])
	if err != nil {
		fmt.Fprintf(hc.Stderr, "mv: %v\n", err)
		return exitErr(1)
	}
	dst, err := s.resolve(hc.Dir, args[1])
	if err != nil {
		fmt.Fprintf(hc.Stderr, "mv: %v\n", err)
		return exitErr(1)
	}

	if info, err := os.Stat(dst); err == nil && info.IsDir() {
		dst = filepath.Join(dst, filepath.Base(src))
	}

	if err := os.Rename(src, dst); err != nil {
		fmt.Fprintf(hc.Stderr, "mv: %v\n", err)
		return exitErr(1)
	}
	return nil
}

func (s *Shell) cmdRm(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	recursive := false
	var targets []string
	for _, arg := range args {
		switch arg {
		case "-r", "-rf", "-fr":
			recursive = true
		default:
			targets = append(targets, arg)
		}
	}

	if len(targets) == 0 {
		fmt.Fprintln(hc.Stderr, "rm: missing operand")
		return exitErr(1)
	}

	for _, t := range targets {
		if s.fs != nil {
			vp := s.toVirtualPath(hc.Dir, t)
			if vp == "/" {
				fmt.Fprintln(hc.Stderr, "rm: cannot remove root directory")
				return exitErr(1)
			}
			if err := s.fs.Delete(vp); err != nil {
				fmt.Fprintf(hc.Stderr, "rm: %v\n", err)
				return exitErr(1)
			}
			continue
		}

		resolved, err := s.resolve(hc.Dir, t)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "rm: %v\n", err)
			return exitErr(1)
		}
		if resolved == s.filesDir {
			fmt.Fprintln(hc.Stderr, "rm: cannot remove root directory")
			return exitErr(1)
		}
		if recursive {
			err = os.RemoveAll(resolved)
		} else {
			err = os.Remove(resolved)
		}
		if err != nil {
			fmt.Fprintf(hc.Stderr, "rm: %v\n", err)
			return exitErr(1)
		}
	}
	return nil
}

func (s *Shell) cmdMkdir(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	mkParents := false
	var dirs []string
	for _, arg := range args {
		if arg == "-p" {
			mkParents = true
		} else {
			dirs = append(dirs, arg)
		}
	}

	if len(dirs) == 0 {
		fmt.Fprintln(hc.Stderr, "mkdir: missing operand")
		return exitErr(1)
	}

	for _, d := range dirs {
		if s.fs != nil {
			vp := s.toVirtualPath(hc.Dir, d)
			if err := s.fs.Mkdir(vp); err != nil {
				fmt.Fprintf(hc.Stderr, "mkdir: %v\n", err)
				return exitErr(1)
			}
			continue
		}

		resolved, err := s.resolve(hc.Dir, d)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "mkdir: %v\n", err)
			return exitErr(1)
		}
		if mkParents {
			err = os.MkdirAll(resolved, 0o755)
		} else {
			err = os.Mkdir(resolved, 0o755)
		}
		if err != nil {
			fmt.Fprintf(hc.Stderr, "mkdir: %v\n", err)
			return exitErr(1)
		}
	}
	return nil
}

func (s *Shell) cmdTouch(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	if len(args) == 0 {
		fmt.Fprintln(hc.Stderr, "touch: missing operand")
		return exitErr(1)
	}

	for _, name := range args {
		if s.fs != nil {
			if opener, ok := s.fs.(vfs.FileOpener); ok {
				vp := s.toVirtualPath(hc.Dir, name)
				f, err := opener.OpenFile(vp, os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					fmt.Fprintf(hc.Stderr, "touch: %v\n", err)
					return exitErr(1)
				}
				f.Close()
				continue
			}
		}

		resolved, err := s.resolve(hc.Dir, name)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "touch: %v\n", err)
			return exitErr(1)
		}
		f, err := os.OpenFile(resolved, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "touch: %v\n", err)
			return exitErr(1)
		}
		f.Close()
	}
	return nil
}

func (s *Shell) cmdEcho(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	fmt.Fprintln(hc.Stdout, strings.Join(args, " "))
	return nil
}

func (s *Shell) cmdWhoami(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	fmt.Fprintln(hc.Stdout, s.username)
	return nil
}

func (s *Shell) cmdHelp(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	cmds := make([]string, 0, len(s.builtins))
	for name := range s.builtins {
		cmds = append(cmds, name)
	}
	sort.Strings(cmds)

	fmt.Fprintln(hc.Stdout, "Built-in commands:")
	for _, name := range cmds {
		fmt.Fprintf(hc.Stdout, "  %s\n", name)
	}

	if len(s.pluginCmds) > 0 {
		fmt.Fprintln(hc.Stdout, "\nPlugin commands:")
		for id := range s.pluginCmds {
			fmt.Fprintf(hc.Stdout, "  %s\n", id)
		}
	}
	return nil
}

func (s *Shell) cmdExit(ctx context.Context, args []string) error {
	code := 0
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil {
			code = v
		}
	}
	s.exited = true
	return exitErr(uint8(code))
}

func (s *Shell) cmdClear(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	fmt.Fprint(hc.Stdout, "\033[2J\033[H")
	return nil
}

func (s *Shell) cmdTrue(_ context.Context, _ []string) error {
	return nil
}

func (s *Shell) cmdFalse(_ context.Context, _ []string) error {
	return exitErr(1)
}
