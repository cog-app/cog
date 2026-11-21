package shell

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/vfs"
)

type grepOpts struct {
	patterns                []string
	patternFiles            []string
	ignoreCase, invert      bool
	lineNumber, count       bool
	filesWith, filesWithout bool
	recursive, word, line   bool
	fixed, extended         bool
	noFilename, filename    bool
	onlyMatching, quiet     bool
	noMessages              bool
	maxCount                int
	include, exclude        []string
}

func (s *Shell) newGrepCmd() *cobra.Command {
	var o grepOpts
	cmd := &cobra.Command{
		Use:               "grep [flags] PATTERN [file...]",
		Short:             "Print lines that match a pattern",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, stdin, dir := getHC(cmd)
			if len(o.patterns) == 0 && len(o.patternFiles) == 0 {
				if len(args) == 0 {
					s.errorf(stderr, "grep: no pattern\n")
					return exitErr(2)
				}
				o.patterns, args = []string{args[0]}, args[1:]
			}
			for _, pf := range o.patternFiles {
				data, err := s.fs.ReadFile(s.toVirtualPath(dir, pf))
				if err != nil {
					s.errorf(stderr, "grep: %s: %v\n", pf, err)
					return exitErr(2)
				}
				o.patterns = append(o.patterns, strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")...)
			}
			m, err := newGrepMatcher(&o)
			if err != nil {
				s.errorf(stderr, "grep: %v\n", err)
				return exitErr(2)
			}
			g := &grepper{s: s, o: &o, m: m, out: bufio.NewWriterSize(stdout, 64<<10), stderr: stderr}
			if o.recursive && len(args) == 0 {
				args = []string{"."}
			}
			g.showName = !o.noFilename && (o.filename || o.recursive || len(args) > 1)
			if len(args) == 0 {
				g.file("(standard input)", stdin)
			}
			for _, name := range args {
				if g.done {
					break
				}
				g.path(name, s.toVirtualPath(dir, name))
			}
			if err := g.out.Flush(); err != nil {
				return err
			}
			switch {
			case g.matched && (o.quiet || !g.failed):
				return nil
			case g.failed:
				return exitErr(2)
			case g.matched:
				return nil
			}
			return exitErr(1)
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&o.patterns, "regexp", "e", nil, "Use PATTERN for matching")
	f.StringArrayVarP(&o.patternFiles, "file", "f", nil, "Take patterns from FILE")
	f.BoolVarP(&o.ignoreCase, "ignore-case", "i", false, "Ignore case distinctions")
	f.BoolVarP(&o.invert, "invert-match", "v", false, "Select non-matching lines")
	f.BoolVarP(&o.lineNumber, "line-number", "n", false, "Print line numbers")
	f.BoolVarP(&o.count, "count", "c", false, "Print only a count of matching lines per file")
	f.BoolVarP(&o.filesWith, "files-with-matches", "l", false, "Print only names of files with matches")
	f.BoolVarP(&o.filesWithout, "files-without-match", "L", false, "Print only names of files without matches")
	f.BoolVarP(&o.recursive, "recursive", "r", false, "Read directories recursively")
	f.BoolVarP(&o.recursive, "dereference-recursive", "R", false, "Same as -r")
	f.BoolVarP(&o.word, "word-regexp", "w", false, "Match only whole words")
	f.BoolVarP(&o.line, "line-regexp", "x", false, "Match only whole lines")
	f.BoolVarP(&o.fixed, "fixed-strings", "F", false, "PATTERN is a set of fixed strings")
	f.BoolVarP(&o.extended, "extended-regexp", "E", false, "PATTERN is an extended regular expression")
	f.BoolVarP(&o.noFilename, "no-filename", "h", false, "Suppress file name prefixes")
	f.BoolVarP(&o.filename, "with-filename", "H", false, "Print file name prefixes")
	f.BoolVarP(&o.onlyMatching, "only-matching", "o", false, "Print only the matched parts of lines")
	f.BoolVarP(&o.quiet, "quiet", "q", false, "Print nothing; exit 0 on the first match")
	f.BoolVarP(&o.noMessages, "no-messages", "s", false, "Suppress messages about unreadable files")
	f.IntVarP(&o.maxCount, "max-count", "m", -1, "Stop after NUM matching lines per file")
	f.StringArrayVar(&o.include, "include", nil, "Search only files whose base name matches GLOB")
	f.StringArrayVar(&o.exclude, "exclude", nil, "Skip files whose base name matches GLOB")
	// -h is --no-filename, as in GNU grep.
	cmd.Flags().BoolP("help", "", false, "Help for grep")
	return cmd
}

// grepMatcher finds matches in a line. A literal pattern with no options
// skips the regexp engine.
type grepMatcher struct {
	re      *regexp.Regexp
	literal []byte
	// required is a string every match of re contains, found with
	// bytes.Index across many lines before re looks at a line. Go's regexp
	// is fast on a short line but not over a large buffer.
	required []byte
}

func newGrepMatcher(o *grepOpts) (*grepMatcher, error) {
	if o.fixed && len(o.patterns) == 1 && !o.ignoreCase && !o.word && !o.line && o.patterns[0] != "" {
		return &grepMatcher{literal: []byte(o.patterns[0])}, nil
	}
	alts := make([]string, len(o.patterns))
	for i, p := range o.patterns {
		switch {
		case o.fixed:
			alts[i] = regexp.QuoteMeta(p)
		case o.extended:
			alts[i] = p
		default:
			alts[i] = breToRE2(p)
		}
	}
	expr := "(?:" + strings.Join(alts, ")|(?:") + ")"
	switch {
	case o.line:
		expr = "^(?:" + expr + ")$"
	case o.word:
		expr = `(?:^|\b)(?:` + expr + `)(?:\b|$)`
	}
	if o.ignoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, err
	}
	if lit, ok := plainLiteral(expr); ok {
		return &grepMatcher{literal: []byte(lit)}, nil
	}
	return &grepMatcher{re: re, required: []byte(requiredLiteral(expr))}, nil
}

func (m *grepMatcher) match(line []byte) bool {
	if m.re == nil {
		return bytes.Contains(line, m.literal)
	}
	return m.re.Match(line)
}

// index returns where the first match in a run of lines starts, or -1.
func (m *grepMatcher) index(b []byte) int {
	if m.re == nil {
		return bytes.Index(b, m.literal)
	}
	return bytes.Index(b, m.required)
}

// all returns the [start, end) of each match in line, for -o and colour.
func (m *grepMatcher) all(line []byte) [][]int {
	if m.re != nil {
		return m.re.FindAllIndex(line, -1)
	}
	var locs [][]int
	for off := 0; off <= len(line); {
		i := bytes.Index(line[off:], m.literal)
		if i < 0 {
			break
		}
		start := off + i
		locs = append(locs, []int{start, start + len(m.literal)})
		off = start + len(m.literal)
	}
	return locs
}

// plainLiteral reports whether expr matches exactly one fixed,
// case-sensitive string anywhere in a line. Regexp.LiteralPrefix is no use
// here: it calls ^(?:x)$ a complete literal.
func plainLiteral(expr string) (string, bool) {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return "", false
	}
	re = re.Simplify()
	for re.Op == syntax.OpCapture && len(re.Sub) == 1 {
		re = re.Sub[0]
	}
	if re.Op != syntax.OpLiteral || re.Flags&syntax.FoldCase != 0 || len(re.Rune) == 0 {
		return "", false
	}
	return string(re.Rune), true
}

// requiredLiteral returns the longest case-sensitive literal that a
// concatenation at the top of expr requires, or "".
func requiredLiteral(expr string) string {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return ""
	}
	re = re.Simplify()
	for re.Op == syntax.OpCapture && len(re.Sub) == 1 {
		re = re.Sub[0]
	}
	if re.Op != syntax.OpConcat {
		return ""
	}
	best := ""
	for _, sub := range re.Sub {
		for sub.Op == syntax.OpCapture && len(sub.Sub) == 1 {
			sub = sub.Sub[0]
		}
		if sub.Op == syntax.OpLiteral && sub.Flags&syntax.FoldCase == 0 && len(sub.Rune) > len(best) {
			best = string(sub.Rune)
		}
	}
	return best
}

// breToRE2 turns a POSIX basic regular expression into RE2 syntax: in a BRE
// \( \) \{ \} \| \+ \? are the operators and the bare characters literals.
func breToRE2(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p):
			i++
			switch n := p[i]; n {
			case '(', ')', '{', '}', '|', '+', '?':
				b.WriteByte(n)
			default:
				b.WriteByte('\\')
				b.WriteByte(n)
			}
		case c == '(' || c == ')' || c == '{' || c == '}' || c == '|' || c == '+' || c == '?':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '*' && (i == 0 || p[i-1] == '^' && i == 1):
			b.WriteString(`\*`)
		case c == '[':
			end := bracketEnd(p, i)
			b.WriteString(p[i:end])
			i = end - 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// bracketEnd returns the index just past the bracket expression at p[i].
func bracketEnd(p string, i int) int {
	j := i + 1
	if j < len(p) && p[j] == '^' {
		j++
	}
	if j < len(p) && p[j] == ']' {
		j++
	}
	for ; j < len(p); j++ {
		if p[j] == '[' && j+1 < len(p) && (p[j+1] == ':' || p[j+1] == '.' || p[j+1] == '=') {
			if k := strings.Index(p[j+2:], string(p[j+1])+"]"); k >= 0 {
				j += k + 3
				continue
			}
		}
		if p[j] == ']' {
			return j + 1
		}
	}
	return len(p)
}

type grepper struct {
	s        *Shell
	o        *grepOpts
	m        *grepMatcher
	out      *bufio.Writer
	stderr   io.Writer
	showName bool
	matched  bool
	failed   bool
	// done is set by -q on the first match: nothing more needs reading.
	done   bool
	reader *bufio.Reader
	long   []byte
	buf    []byte
}

func (g *grepper) warn(format string, a ...any) {
	g.failed = true
	if g.o.noMessages {
		return
	}
	if err := g.out.Flush(); err != nil {
		g.s.log.Warn("grep: flush", "err", err)
	}
	g.s.errorf(g.stderr, format, a...)
}

func (g *grepper) path(name, vp string) {
	fi, err := g.s.fs.Stat(vp)
	if err != nil {
		g.warn("grep: %s: %v\n", name, err)
		return
	}
	if !fi.IsDir {
		g.open(name, vp)
		return
	}
	if !g.o.recursive {
		g.warn("grep: %s: Is a directory\n", name)
		return
	}
	err = g.s.walk(vp, func(p string, fi *vfs.FileInfo, depth int) error {
		if g.done {
			return errStopWalk
		}
		if fi.IsDir || !g.included(fi.Name) {
			return nil
		}
		display := name + p[len(vp):]
		if strings.HasSuffix(name, "/") {
			display = name + p[len(vp)+1:]
		}
		g.open(display, p)
		return nil
	}, func(p string, err error) {
		g.warn("grep: %s: %v\n", p, err)
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		g.warn("grep: %s: %v\n", name, err)
	}
}

var errStopWalk = errors.New("stop walk")

func (g *grepper) included(name string) bool {
	for _, pat := range g.o.exclude {
		if ok, _ := path.Match(pat, name); ok {
			return false
		}
	}
	if len(g.o.include) == 0 {
		return true
	}
	for _, pat := range g.o.include {
		if ok, _ := path.Match(pat, name); ok {
			return true
		}
	}
	return false
}

func (g *grepper) open(name, vp string) {
	rc, err := g.s.fs.Get(vp)
	if err != nil {
		g.warn("grep: %s: %v\n", name, err)
		return
	}
	g.file(name, rc)
	if err := rc.Close(); err != nil {
		g.warn("grep: %s: %v\n", name, err)
	}
}

// fileState is what a search of one file has found so far.
type fileState struct {
	name   string
	binary bool
	count  int
}

// hit records a selected line and reports whether the file is finished.
func (g *grepper) hit(st *fileState, lineNo int, text []byte) (stop bool) {
	o := g.o
	st.count++
	g.matched = true
	if o.quiet {
		g.done = true
		return true
	}
	if o.filesWith || o.filesWithout {
		return true
	}
	if !o.count {
		if st.binary {
			fmt.Fprintf(g.out, "Binary file %s matches\n", st.name)
			return true
		}
		g.printLine(st.name, lineNo, text, g.s.color)
	}
	return o.maxCount >= 0 && st.count >= o.maxCount
}

func (g *grepper) file(name string, r io.Reader) {
	st := &fileState{name: name}
	var err error
	if g.o.invert || (g.m.re != nil && len(g.m.required) == 0) {
		err = g.scanLines(st, r)
	} else {
		err = g.scanChunks(st, r)
	}
	if err != nil {
		g.warn("grep: %s: %v\n", name, err)
	}
	if g.done {
		return
	}
	o, color := g.o, g.s.color
	switch {
	case o.filesWith && st.count > 0, o.filesWithout && st.count == 0:
		g.out.WriteString(g.name(name, color))
		g.out.WriteByte('\n')
	case o.count:
		if g.showName {
			g.out.WriteString(g.name(name, color))
			g.out.WriteString(g.sep(":", color))
		}
		g.out.WriteString(strconv.Itoa(st.count))
		g.out.WriteByte('\n')
	}
}

// scanChunks searches whole buffers of complete lines at a time, as GNU grep
// does: one bytes.Index over a chunk instead of a search per line, then only
// the line a candidate lands in is examined, by the regexp if there is one.
func (g *grepper) scanChunks(st *fileState, r io.Reader) error {
	if g.buf == nil {
		g.buf = make([]byte, 256<<10)
	}
	buf := g.buf
	n, lineNo := 0, 1
	first := true
	for {
		m, err := io.ReadFull(r, buf[n:])
		n += m
		eof := err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF)
		if err != nil && !eof {
			return err
		}
		if first {
			first = false
			st.binary = bytes.IndexByte(buf[:min(n, 8<<10)], 0) >= 0
		}
		data := buf[:n]
		end := n
		if !eof {
			end = bytes.LastIndexByte(data, '\n') + 1
			if end == 0 {
				// One line fills the buffer: grow it and read on.
				grown := make([]byte, 2*len(buf))
				copy(grown, data)
				buf, g.buf = grown, grown
				continue
			}
		}
		stop, counted := g.searchChunk(st, data[:end], lineNo)
		if stop {
			return nil
		}
		if g.o.lineNumber {
			lineNo = counted + bytes.Count(data[:end], []byte{'\n'})
		}
		if eof {
			return nil
		}
		n = copy(buf, data[end:])
	}
}

// searchChunk handles the matching lines of chunk, a run of whole lines
// whose first is line number lineNo. It returns the line number at the
// start of chunk minus the newlines it has already counted, so the caller
// can count the rest in one pass.
func (g *grepper) searchChunk(st *fileState, chunk []byte, lineNo int) (stop bool, base int) {
	pos, counted := 0, 0
	for pos < len(chunk) {
		idx := g.m.index(chunk[pos:])
		if idx < 0 {
			break
		}
		at := pos + idx
		if at >= len(chunk) {
			break
		}
		lineStart := bytes.LastIndexByte(chunk[:at], '\n') + 1
		lineEnd := len(chunk)
		if e := bytes.IndexByte(chunk[at:], '\n'); e >= 0 {
			lineEnd = at + e
		}
		line := chunk[lineStart:lineEnd]
		pos = lineEnd + 1
		if g.m.re != nil && !g.m.re.Match(line) {
			continue
		}
		if g.o.lineNumber {
			lineNo += bytes.Count(chunk[counted:lineStart], []byte{'\n'})
			counted = lineStart
		}
		if g.hit(st, lineNo, line) {
			return true, 0
		}
	}
	return false, lineNo - bytes.Count(chunk[:counted], []byte{'\n'})
}

// scanLines reads line by line, for -v, where every line must be looked at,
// and for a regexp with no required literal to look for first.
// Lines are read in place from one reused buffer.
func (g *grepper) scanLines(st *fileState, r io.Reader) error {
	if g.reader == nil {
		g.reader = bufio.NewReaderSize(r, 64<<10)
	} else {
		g.reader.Reset(r)
	}
	br := g.reader
	if head, _ := br.Peek(8 << 10); bytes.IndexByte(head, 0) >= 0 {
		st.binary = true
	}
	lineNo := 0
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			g.long = append(g.long[:0], line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = br.ReadSlice('\n')
				g.long = append(g.long, line...)
			}
			line = g.long
		}
		if len(line) == 0 && err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		lineNo++
		text := line
		if n := len(text); n > 0 && text[n-1] == '\n' {
			text = text[:n-1]
		}
		if g.m.match(text) != g.o.invert && g.hit(st, lineNo, text) {
			return nil
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (g *grepper) name(name string, color bool) string {
	if color {
		return ansiMagenta + name + ansiReset
	}
	return name
}

func (g *grepper) sep(sep string, color bool) string {
	if color {
		return ansiCyan + sep + ansiReset
	}
	return sep
}

func (g *grepper) prefix(name string, lineNo int, color bool) {
	if g.showName {
		g.out.WriteString(g.name(name, color))
		g.out.WriteString(g.sep(":", color))
	}
	if g.o.lineNumber {
		if color {
			g.out.WriteString(ansiGreen)
		}
		g.out.WriteString(strconv.Itoa(lineNo))
		if color {
			g.out.WriteString(ansiReset)
		}
		g.out.WriteString(g.sep(":", color))
	}
}

func (g *grepper) printLine(name string, lineNo int, text []byte, color bool) {
	if g.o.onlyMatching {
		if g.o.invert {
			return
		}
		for _, loc := range g.m.all(text) {
			if loc[0] == loc[1] {
				continue
			}
			g.prefix(name, lineNo, color)
			if color {
				g.out.WriteString(ansiBoldRed)
			}
			g.out.Write(text[loc[0]:loc[1]])
			if color {
				g.out.WriteString(ansiReset)
			}
			g.out.WriteByte('\n')
		}
		return
	}
	g.prefix(name, lineNo, color)
	if !color || g.o.invert {
		g.out.Write(text)
		g.out.WriteByte('\n')
		return
	}
	last := 0
	for _, loc := range g.m.all(text) {
		if loc[0] == loc[1] {
			continue
		}
		g.out.Write(text[last:loc[0]])
		g.out.WriteString(ansiBoldRed)
		g.out.Write(text[loc[0]:loc[1]])
		g.out.WriteString(ansiReset)
		last = loc[1]
	}
	g.out.Write(text[last:])
	g.out.WriteByte('\n')
}
