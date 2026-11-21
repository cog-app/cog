package shell

import (
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// highlighter colours the line being edited, as zsh-syntax-highlighting
// does: the command green when the shell has it and red when not, flags,
// quoting and expansions, and an argument underlined when it names a path
// that exists.
//
// Readline paints only the runes it appends when typing at the end of the
// line, so a word could not change colour as it grows; listen forces a full
// redraw instead. Paths are looked up while a key is handled, for no longer
// than lookupBudget: an answer that comes later is painted on the next
// keystroke, never by redrawing from another goroutine, which would erase
// readline's completion menu or search prompt from under it.
type highlighter struct {
	shell *Shell
	dir   func() string

	// searching is set while readline's incremental search has the line.
	searching bool

	mu       sync.Mutex
	gen      int // bumped by reset, so a late lookup does not refill the cache
	paths    map[string]pathState
	commands map[string]bool
}

type pathState uint8

const (
	pathPending pathState = iota + 1
	pathExists
	pathMissing
)

const (
	// lookupBudget is how long a keystroke may wait on path lookups: far
	// more than a local disk needs, and short enough that a slow remote
	// volume does not hold up echo.
	lookupBudget = 50 * time.Millisecond
	// maxLookups bounds the paths looked up for one keystroke, for a
	// pasted line of many words.
	maxLookups = 16
)

const (
	keyCtrlH      = 8
	keyBackSearch = 18
	keyFwdSearch  = 19
	keyBackspace  = 127
)

const (
	styleKnown      = ansiGreen
	styleUnknown    = ansiRed
	styleFlag       = ansiCyan
	styleQuoted     = ansiYellow
	styleExpansion  = ansiMagenta
	styleComment    = ansiDim
	styleExistsPath = ansiUnderline
)

func newHighlighter(s *Shell, dir func() string) *highlighter {
	h := &highlighter{shell: s, dir: dir}
	h.reset()
	return h
}

// reset forgets what was looked up, after a command that may have changed
// the files or, by su, which commands there are.
func (h *highlighter) reset() {
	commands := map[string]bool{"cd": true, "pwd": true, "exit": true, "help": true, "umask": true, "command": true}
	for _, c := range h.shell.newRootCmd("").Commands() {
		commands[c.Name()] = true
		for _, a := range c.Aliases {
			commands[a] = true
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gen++
	h.paths = make(map[string]pathState)
	h.commands = commands
}

// listen is the readline Listener. It looks up the paths the line names and
// asks for a redraw when the line was painted a piece at a time, or when a
// lookup changed what it shows.
func (h *highlighter) listen(line []rune, pos int, key rune) ([]rune, int, bool) {
	if line == nil && key == 0 {
		return nil, 0, false // before the prompt is drawn
	}
	switch {
	case key == keyBackSearch || key == keyFwdSearch:
		h.searching = true
	case h.searching && (key >= ' ' || key == keyBackspace || key == keyCtrlH):
		// Still typing the search.
	default:
		h.searching = false
	}
	changed := h.resolve(h.pathWords(line))
	appended := pos == len(line) && key >= ' ' && key != keyBackspace
	if h.searching || !(appended || changed) {
		return nil, 0, false
	}
	return line, pos, true
}

// resolve looks up paths not yet known, waiting up to lookupBudget, and
// reports whether any answer came in that time.
func (h *highlighter) resolve(paths []string) bool {
	h.mu.Lock()
	var todo []string
	for _, p := range paths {
		if _, ok := h.paths[p]; !ok && len(todo) < maxLookups {
			h.paths[p] = pathPending
			todo = append(todo, p)
		}
	}
	gen := h.gen
	h.mu.Unlock()
	if len(todo) == 0 {
		return false
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, p := range todo {
			st := pathMissing
			if _, err := h.shell.fs.Stat(p); err == nil {
				st = pathExists
			}
			h.mu.Lock()
			if h.gen == gen {
				h.paths[p] = st
			}
			h.mu.Unlock()
		}
	}()
	timer := time.NewTimer(lookupBudget)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		h.mu.Lock()
		defer h.mu.Unlock()
		return slices.ContainsFunc(todo, func(p string) bool { return h.paths[p] != pathPending })
	}
}

// span is a style over runes [start, end) of the line.
type span struct {
	start, end int
	style      string
}

// paint returns line with the escapes that colour it. The visible runes are
// unchanged, so readline's cursor arithmetic still holds.
func (h *highlighter) paint(line []rune) []rune {
	spans := h.spans(string(line))
	if len(spans) == 0 {
		return line
	}
	styles := make([]string, len(line))
	for _, sp := range spans {
		for i := max(sp.start, 0); i < min(sp.end, len(line)); i++ {
			styles[i] = sp.style
		}
	}
	out := make([]rune, 0, len(line)+len(spans)*8)
	cur := ""
	for i, r := range line {
		if styles[i] != cur {
			if cur != "" {
				out = append(out, []rune(ansiReset)...)
			}
			out = append(out, []rune(styles[i])...)
			cur = styles[i]
		}
		out = append(out, r)
	}
	if cur != "" {
		out = append(out, []rune(ansiReset)...)
	}
	return out
}

// spans parses text and styles its words. A line that does not parse even
// with the parser recovering what is still to be typed is left plain.
func (h *highlighter) spans(text string) []span {
	f := parseLine(text)
	if f == nil {
		return nil
	}
	runeAt := runeOffsets(text)
	// A quote left open ends where the line does.
	of := func(n syntax.Node, style string) span {
		start, end := n.Pos(), n.End()
		if end.IsRecovered() || end.Offset() < start.Offset() {
			return span{runeAt(int(start.Offset())), runeAt(len(text)), style}
		}
		return span{runeAt(int(start.Offset())), runeAt(int(end.Offset())), style}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	var spans []span
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr:
			for i, w := range n.Args {
				if style := h.wordStyle(w, i == 0); style != "" {
					spans = append(spans, of(w, style))
				}
			}
		case *syntax.Redirect:
			if n.Word != nil && h.pathState(n.Word) == pathExists {
				spans = append(spans, of(n.Word, styleExistsPath))
			}
		case *syntax.Comment:
			spans = append(spans, of(n, styleComment))
		// Quotes and expansions are walked after the word holding them, so
		// they keep their own colour inside it.
		case *syntax.SglQuoted, *syntax.DblQuoted:
			spans = append(spans, of(n, styleQuoted))
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp:
			spans = append(spans, of(n, styleExpansion))
			return false
		}
		return true
	})
	return spans
}

// wordStyle is the colour of a whole word: the command, a flag, or a path
// that exists. The caller holds h.mu.
func (h *highlighter) wordStyle(w *syntax.Word, command bool) string {
	lit := w.Lit()
	if command {
		if lit == "" {
			return ""
		}
		if h.commands[lit] || interp.IsBuiltin(lit) {
			return styleKnown
		}
		return styleUnknown
	}
	if strings.HasPrefix(lit, "-") {
		return styleFlag
	}
	if h.pathState(w) == pathExists {
		return styleExistsPath
	}
	return ""
}

// pathState is what is known of the path w names. The caller holds h.mu.
func (h *highlighter) pathState(w *syntax.Word) pathState {
	p, ok := h.wordPath(w)
	if !ok {
		return 0
	}
	return h.paths[p]
}

// wordPath is the path w names, if it is a plain word that could name one.
func (h *highlighter) wordPath(w *syntax.Word) (string, bool) {
	lit := w.Lit()
	if lit == "" || strings.HasPrefix(lit, "-") || strings.ContainsAny(lit, "*?[=") {
		return "", false
	}
	if lit == "~" || strings.HasPrefix(lit, "~/") {
		lit = home + lit[1:]
	}
	return h.shell.toVirtualPath(h.dir(), lit), true
}

// pathWords returns the paths the arguments and redirections of line name.
func (h *highlighter) pathWords(line []rune) []string {
	f := parseLine(string(line))
	if f == nil {
		return nil
	}
	var paths []string
	add := func(w *syntax.Word) {
		if p, ok := h.wordPath(w); ok && !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr:
			for _, w := range n.Args[min(1, len(n.Args)):] {
				add(w)
			}
		case *syntax.Redirect:
			if n.Word != nil {
				add(n.Word)
			}
		}
		return true
	})
	return paths
}

// parseLine parses a line as typed so far: an open quote or a pipe with
// nothing after it yet is recovered, not an error.
func parseLine(text string) *syntax.File {
	parser := syntax.NewParser(syntax.Variant(syntax.LangPOSIX), syntax.KeepComments(true), syntax.RecoverErrors(3))
	f, err := parser.Parse(strings.NewReader(text), "")
	if err != nil {
		return nil
	}
	return f
}

// runeOffsets returns a function from a byte offset in text to the index of
// the rune there.
func runeOffsets(text string) func(int) int {
	if utf8.RuneCountInString(text) == len(text) {
		return func(b int) int { return min(b, len(text)) }
	}
	idx := make([]int, len(text)+1)
	r := 0
	for b := range text {
		for i := b; i < len(text) && (i == b || !utf8.RuneStart(text[i])); i++ {
			idx[i] = r
		}
		r++
	}
	idx[len(text)] = r
	return func(b int) int { return idx[min(b, len(text))] }
}
