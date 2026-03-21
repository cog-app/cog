package shell

import (
	"bytes"
	"context"
	"io"
	"strings"
)

// completer implements readline.AutoCompleter for the shell using cobra's
// hidden __complete command.
type completer struct {
	shell *Shell
	dir   func() string // returns current working directory
}

// Do returns completion candidates for the current line and cursor position.
func (c *completer) Do(line []rune, pos int) ([][]rune, int) {
	text := string(line[:pos])
	words := strings.Fields(text)
	atNewWord := len(text) > 0 && text[len(text)-1] == ' '

	compArgs := []string{"__complete"}
	compArgs = append(compArgs, words...)
	if atNewWord || len(words) == 0 {
		compArgs = append(compArgs, "")
	}

	root := c.shell.newRootCmd()
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

	partial := ""
	if !atNewWord && len(words) > 0 {
		partial = words[len(words)-1]
	}
	return toRuneSuffix(filtered, len(partial))
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
