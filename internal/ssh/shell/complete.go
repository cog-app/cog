package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// completer implements readline.AutoCompleter for the shell using cobra's
// hidden __complete command.
type completer struct {
	shell *Shell
	dir   func() string // returns current working directory

	// problem is why the last Tab completed nothing, for the next redraw to
	// show below the line. Readline calls Do and the painter on one goroutine.
	problem string
	// shown is set once a problem has been drawn, so it can be cleared from
	// the screen when the line is submitted.
	shown bool
}

// Do returns completion candidates for the current line and cursor position.
func (c *completer) Do(line []rune, pos int) ([][]rune, int) {
	c.problem = ""
	text := string(line[:pos])
	words := strings.Fields(text)
	atNewWord := len(text) > 0 && text[len(text)-1] == ' '

	compArgs := []string{"__complete"}
	compArgs = append(compArgs, words...)
	if atNewWord || len(words) == 0 {
		compArgs = append(compArgs, "")
	}

	root := c.shell.newRootCmd("")
	partial := ""
	if !atNewWord && len(words) > 0 {
		partial = words[len(words)-1]
	}
	if err := completable(root, compArgs[1:len(compArgs)-1], partial); err != nil {
		c.problem = err.Error()
		return nil, 0
	}
	root.SetContext(withShellDir(context.Background(), c.dir()))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(io.Discard)
	root.SetArgs(compArgs)
	root.Execute()

	output := strings.TrimRight(buf.String(), "\n")
	if output == "" {
		return nil, 0
	}
	lines := strings.Split(output, "\n")

	// Last line is a ":N" directive — remove it.
	if len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], ":") {
		lines = lines[:len(lines)-1]
	}

	// Strip tab-separated descriptions.
	for i, l := range lines {
		if before, _, ok := strings.Cut(l, "\t"); ok {
			lines[i] = before
		}
	}

	// Remove empty entries.
	var filtered []string
	for _, l := range lines {
		if l != "" {
			filtered = append(filtered, l)
		}
	}

	return toRuneSuffix(filtered, len(partial))
}

// completable returns why cobra's __complete would fail on the line, if it
// would. On failure cobra writes the error to os.Stderr — the server's, not
// the session's — so a line it would reject is never handed to it.
func completable(root *cobra.Command, args []string, partial string) error {
	cmd, rest, err := root.Find(args)
	if err != nil {
		return fmt.Errorf("%s: command not found", args[0])
	}
	if cmd.DisableFlagParsing {
		return nil
	}
	cmd.InitDefaultHelpFlag()
	if err := cmd.ParseFlags(rest); err != nil {
		// "head -n <TAB>" is completing the flag's value.
		if _, ok := errors.AsType[*pflag.ValueRequiredError](err); !ok {
			return fmt.Errorf("%s: %w", cmd.Name(), err)
		}
	}
	name, _, hasValue := strings.Cut(partial, "=")
	if !hasValue || !strings.HasPrefix(name, "-") {
		return nil
	}
	if long, ok := strings.CutPrefix(name, "--"); ok {
		if cmd.Flags().Lookup(long) == nil {
			return fmt.Errorf("%s: unknown flag: %s", cmd.Name(), name)
		}
		return nil
	}
	if cmd.Flags().ShorthandLookup(name[len(name)-1:]) == nil {
		return fmt.Errorf("%s: unknown shorthand flag: %s", cmd.Name(), name)
	}
	return nil
}

// paint appends the pending problem to line on the row below it and returns
// the cursor to the end of line, where readline expects it. Readline's next
// full redraw clears below the prompt, which takes the message away with it.
func (c *completer) paint(prompt string, line []rune, width int) []rune {
	if c.problem == "" {
		return line
	}
	problem := c.problem
	c.problem = ""
	col := (runewidth.StringWidth(stripANSI(prompt)) + runewidth.StringWidth(string(line))) % width
	// At the right edge readline parks the cursor on the next row itself.
	if width < 2 || col == 0 {
		return line
	}
	c.shown = true
	msg := c.shell.colorize(ansiRed, runewidth.Truncate(problem, width-1, ""))
	return append(line, []rune(fmt.Sprintf("\n\033[K%s\033[1A\033[%dG", msg, col+1))...)
}

// clearShown erases a problem still drawn below the line. Readline leaves the
// cursor at the start of that row after a line is submitted.
func (c *completer) clearShown(w io.Writer) error {
	if !c.shown {
		return nil
	}
	c.shown = false
	_, err := io.WriteString(w, "\033[J")
	return err
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// toRuneSuffix converts candidate strings to the readline format:
// each candidate is the suffix after removing `shared` common prefix characters.
func toRuneSuffix(candidates []string, shared int) ([][]rune, int) {
	result := make([][]rune, len(candidates))
	for i, c := range candidates {
		suffix := c[shared:]
		if !strings.HasSuffix(suffix, "/") {
			suffix += " "
		}
		result[i] = []rune(suffix)
	}
	return result, shared
}
