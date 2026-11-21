package shell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/vfs"
)

func (s *Shell) newFindCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "find [path...] [expression]",
		Short: "Search for files in a directory hierarchy",
		Long: `Search for files in a directory hierarchy.

Tests: -name -iname -path -ipath -type f|d -size [+-]N[bckMG] -mtime [+-]N
       -mmin [+-]N -newer FILE -empty
Actions: -print -print0 -prune -delete (empty directories only)
Operators: ( EXPR ) ! -not -a -and -o -or
Options: -maxdepth N -mindepth N`,
		DisableFlagParsing: true,
		ValidArgsFunction:  s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, dir := getHC(cmd)
			if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
				return cmd.Help()
			}
			var roots []string
			for len(args) > 0 && !strings.HasPrefix(args[0], "-") && args[0] != "(" && args[0] != "!" {
				roots = append(roots, args[0])
				args = args[1:]
			}
			if len(roots) == 0 {
				roots = []string{"."}
			}
			f := &finder{s: s, dir: dir, now: time.Now(), stderr: stderr, maxDepth: -1}
			expr, err := f.parse(args)
			if err != nil {
				s.errorf(stderr, "find: %v\n", err)
				return exitErr(1)
			}
			out := bufio.NewWriter(stdout)
			f.out = out
			failed := false
			for _, root := range roots {
				rootVP := s.toVirtualPath(dir, root)
				err := s.walk(rootVP, func(p string, fi *vfs.FileInfo, depth int) error {
					return f.visit(expr, root, rootVP, p, fi, depth)
				}, func(p string, err error) {
					s.errorf(stderr, "find: %s: %v\n", f.display(root, rootVP, p), err)
					failed = true
				})
				if err != nil {
					if ferr := out.Flush(); ferr != nil {
						return ferr
					}
					s.errorf(stderr, "find: %s: %v\n", root, err)
					failed = true
				}
			}
			if err := out.Flush(); err != nil {
				return err
			}
			if failed || f.failed {
				return exitErr(1)
			}
			return nil
		},
	}
}

type finder struct {
	s        *Shell
	dir      string
	now      time.Time
	out      *bufio.Writer
	stderr   io.Writer
	maxDepth int
	minDepth int
	// hasAction is set when the expression prints or deletes itself, so it
	// is not wrapped in an implicit -print.
	hasAction bool
	failed    bool

	// prune is set by -prune, or -delete of a directory, while the
	// expression runs on an entry.
	prune bool
}

// findCtx is the entry an expression is evaluated against.
type findCtx struct {
	path    string
	display string
	fi      *vfs.FileInfo
}

type findExpr func(f *finder, c *findCtx) bool

func (f *finder) display(arg, rootVP, p string) string {
	if p == rootVP {
		return arg
	}
	rel := p[len(rootVP):]
	if strings.HasSuffix(arg, "/") {
		rel = rel[1:]
	}
	return arg + rel
}

func (f *finder) visit(expr findExpr, arg, rootVP, p string, fi *vfs.FileInfo, depth int) error {
	f.prune = false
	if depth >= f.minDepth {
		c := &findCtx{path: p, display: f.display(arg, rootVP, p), fi: fi}
		expr(f, c)
	}
	if fi.IsDir && (f.prune || (f.maxDepth >= 0 && depth >= f.maxDepth)) {
		return errSkipDir
	}
	return nil
}

func (f *finder) parse(args []string) (findExpr, error) {
	p := &findParser{f: f, args: args}
	if len(args) == 0 {
		return printExpr("\n"), nil
	}
	expr, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.args) {
		return nil, fmt.Errorf("unexpected %s", p.args[p.pos])
	}
	if expr == nil {
		expr = func(*finder, *findCtx) bool { return true }
	}
	if !f.hasAction {
		inner, print := expr, printExpr("\n")
		expr = func(f *finder, c *findCtx) bool { return inner(f, c) && print(f, c) }
	}
	return expr, nil
}

type findParser struct {
	f    *finder
	args []string
	pos  int
}

func (p *findParser) peek() string {
	if p.pos < len(p.args) {
		return p.args[p.pos]
	}
	return ""
}

func (p *findParser) next() string {
	a := p.peek()
	p.pos++
	return a
}

func (p *findParser) or() (findExpr, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.peek() == "-o" || p.peek() == "-or" {
		p.next()
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		if left == nil || right == nil {
			return nil, errors.New("-o needs an expression on both sides")
		}
		l, r := left, right
		left = func(f *finder, c *findCtx) bool { return l(f, c) || r(f, c) }
	}
	return left, nil
}

func (p *findParser) and() (findExpr, error) {
	var left findExpr
	for {
		switch p.peek() {
		case "", "-o", "-or", ")":
			return left, nil
		case "-a", "-and":
			p.next()
			continue
		}
		right, err := p.unary()
		if err != nil {
			return nil, err
		}
		if right == nil {
			continue
		}
		if left == nil {
			left = right
			continue
		}
		l, r := left, right
		left = func(f *finder, c *findCtx) bool { return l(f, c) && r(f, c) }
	}
}

func (p *findParser) unary() (findExpr, error) {
	switch p.peek() {
	case "!", "-not":
		p.next()
		inner, err := p.unary()
		if err != nil {
			return nil, err
		}
		if inner == nil {
			return nil, errors.New("! needs an expression")
		}
		return func(f *finder, c *findCtx) bool { return !inner(f, c) }, nil
	case "(":
		p.next()
		inner, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.next() != ")" {
			return nil, errors.New("missing )")
		}
		if inner == nil {
			return nil, errors.New("empty ( )")
		}
		return inner, nil
	}
	return p.primary()
}

// primary returns nil, nil for an option such as -maxdepth, which is not a
// test.
func (p *findParser) primary() (findExpr, error) {
	name := p.next()
	arg := func() (string, error) {
		if p.pos >= len(p.args) {
			return "", fmt.Errorf("missing argument to %s", name)
		}
		return p.next(), nil
	}
	switch name {
	case "-name", "-iname", "-path", "-ipath", "-wholename", "-iwholename":
		pat, err := arg()
		if err != nil {
			return nil, err
		}
		re, err := globRegexp(pat, strings.HasPrefix(name, "-i"))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if strings.HasSuffix(name, "name") && !strings.Contains(name, "whole") {
			return func(_ *finder, c *findCtx) bool { return re.MatchString(baseName(c.display)) }, nil
		}
		return func(_ *finder, c *findCtx) bool { return re.MatchString(c.display) }, nil
	case "-type":
		t, err := arg()
		if err != nil {
			return nil, err
		}
		switch t {
		case "f":
			return func(_ *finder, c *findCtx) bool { return !c.fi.IsDir }, nil
		case "d":
			return func(_ *finder, c *findCtx) bool { return c.fi.IsDir }, nil
		}
		return nil, fmt.Errorf("-type: unsupported type %q (f or d)", t)
	case "-size":
		v, err := arg()
		if err != nil {
			return nil, err
		}
		cmp, n, unit, err := parseFindSize(v)
		if err != nil {
			return nil, err
		}
		return func(_ *finder, c *findCtx) bool {
			if c.fi.IsDir {
				return false
			}
			return compareNum(cmp, (c.fi.Size+unit-1)/unit, n)
		}, nil
	case "-mtime", "-mmin":
		v, err := arg()
		if err != nil {
			return nil, err
		}
		cmp, n, err := parseFindNum(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		unit := 24 * time.Hour
		if name == "-mmin" {
			unit = time.Minute
		}
		now := p.f.now
		return func(_ *finder, c *findCtx) bool {
			return compareNum(cmp, int64(now.Sub(c.fi.ModTime)/unit), n)
		}, nil
	case "-newer":
		ref, err := arg()
		if err != nil {
			return nil, err
		}
		fi, err := p.f.s.fs.Stat(p.f.s.toVirtualPath(p.f.dir, ref))
		if err != nil {
			return nil, fmt.Errorf("-newer %s: %w", ref, err)
		}
		t := fi.ModTime
		return func(_ *finder, c *findCtx) bool { return c.fi.ModTime.After(t) }, nil
	case "-empty":
		return func(f *finder, c *findCtx) bool {
			if !c.fi.IsDir {
				return c.fi.Size == 0
			}
			entries, err := f.s.fs.List(c.path)
			if err != nil {
				f.failed = true
				return false
			}
			return len(entries) == 0
		}, nil
	case "-maxdepth", "-mindepth":
		v, err := arg()
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%s: invalid depth %q", name, v)
		}
		if name == "-maxdepth" {
			p.f.maxDepth = n
		} else {
			p.f.minDepth = n
		}
		return nil, nil
	case "-print":
		p.f.hasAction = true
		return printExpr("\n"), nil
	case "-print0":
		p.f.hasAction = true
		return printExpr("\x00"), nil
	case "-prune":
		return func(f *finder, c *findCtx) bool {
			f.prune = true
			return true
		}, nil
	case "-delete":
		p.f.hasAction = true
		return deleteExpr, nil
	case "-true":
		return func(*finder, *findCtx) bool { return true }, nil
	case "-false":
		return func(*finder, *findCtx) bool { return false }, nil
	case "-exec", "-execdir", "-ok":
		return nil, fmt.Errorf("%s is not supported; pipe -print0 to xargs -0", name)
	}
	return nil, fmt.Errorf("unknown predicate %s", name)
}

func printExpr(term string) findExpr {
	return func(f *finder, c *findCtx) bool {
		f.out.WriteString(c.display)
		f.out.WriteString(term)
		return true
	}
}

// deleteExpr removes a file, or a directory only when it is empty: the VFS
// deletes a directory with everything in it, and find -delete must not.
func deleteExpr(f *finder, c *findCtx) bool {
	if c.fi.IsDir {
		entries, err := f.s.fs.List(c.path)
		if err != nil {
			f.reportf("find: cannot delete %s: %v\n", c.display, err)
			return false
		}
		if len(entries) > 0 {
			f.reportf("find: cannot delete %s: directory not empty\n", c.display)
			return false
		}
	}
	if err := f.s.fs.Delete(c.path); err != nil {
		f.reportf("find: cannot delete %s: %v\n", c.display, err)
		return false
	}
	if c.fi.IsDir {
		f.prune = true
	}
	return true
}

func (f *finder) reportf(format string, a ...any) {
	f.failed = true
	// Keep what was printed before the error ahead of it.
	if err := f.out.Flush(); err != nil {
		f.s.log.Warn("find: flush", "err", err)
	}
	f.s.errorf(f.stderr, format, a...)
}

func baseName(p string) string {
	if i := strings.LastIndexByte(strings.TrimRight(p, "/"), '/'); i >= 0 {
		return strings.TrimRight(p[i+1:], "/")
	}
	return p
}

// globRegexp compiles a shell glob for find, where * and ? match a slash
// too, as fnmatch does without FNM_PATHNAME.
func globRegexp(pat string, fold bool) (*regexp.Regexp, error) {
	var b strings.Builder
	if fold {
		b.WriteString("(?i)")
	}
	b.WriteString("^(?s:")
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '\\':
			if i+1 < len(pat) {
				i++
				b.WriteString(regexp.QuoteMeta(pat[i : i+1]))
			} else {
				b.WriteString(`\\`)
			}
		case '[':
			end := strings.IndexByte(pat[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			// A ] first in the class is a member, not its end.
			if end == 0 || (end == 1 && (pat[i+1] == '!' || pat[i+1] == '^')) {
				if next := strings.IndexByte(pat[i+end+2:], ']'); next >= 0 {
					end += next + 1
				}
			}
			class := pat[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(")$")
	return regexp.Compile(b.String())
}

// parseFindNum parses find's +N (more than), -N (less than) or N (exactly).
func parseFindNum(v string) (cmp byte, n int64, err error) {
	if v != "" && (v[0] == '+' || v[0] == '-') {
		cmp, v = v[0], v[1:]
	}
	n, err = strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, 0, fmt.Errorf("invalid number %q", v)
	}
	return cmp, n, nil
}

func parseFindSize(v string) (cmp byte, n, unit int64, err error) {
	unit = 512
	if v != "" {
		switch v[len(v)-1] {
		case 'b':
			v = v[:len(v)-1]
		case 'c':
			unit, v = 1, v[:len(v)-1]
		case 'w':
			unit, v = 2, v[:len(v)-1]
		case 'k':
			unit, v = 1<<10, v[:len(v)-1]
		case 'M':
			unit, v = 1<<20, v[:len(v)-1]
		case 'G':
			unit, v = 1<<30, v[:len(v)-1]
		}
	}
	cmp, n, err = parseFindNum(v)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("-size: %w", err)
	}
	return cmp, n, unit, nil
}

func compareNum(cmp byte, have, want int64) bool {
	switch cmp {
	case '+':
		return have > want
	case '-':
		return have < want
	}
	return have == want
}
