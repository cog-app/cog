package shell

import (
	"os"
	"sort"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin"
)

// completer implements readline.AutoCompleter for the shell.
type completer struct {
	shell *Shell
	dir   func() string // returns current working directory (real path)
}

// Do returns completion candidates for the current line and cursor position.
func (c *completer) Do(line []rune, pos int) ([][]rune, int) {
	text := string(line[:pos])
	words := strings.Fields(text)
	atNewWord := len(text) > 0 && text[len(text)-1] == ' '

	// Command position: first word or empty line.
	if len(words) == 0 || (len(words) == 1 && !atNewWord) {
		prefix := ""
		if len(words) == 1 {
			prefix = words[0]
		}
		return c.completeCommand(prefix)
	}

	// If the first word is a plugin ID, complete subcommands or args.
	if cmds, ok := c.shell.pluginCmds[words[0]]; ok {
		return c.completePlugin(cmds, words[1:], atNewWord)
	}

	// Default: file path completion.
	partial := ""
	if !atNewWord {
		partial = words[len(words)-1]
	}
	return c.completePath(partial)
}

// completeCommand returns matching builtins and plugin command names.
func (c *completer) completeCommand(prefix string) ([][]rune, int) {
	var candidates []string
	for name := range c.shell.builtins {
		if strings.HasPrefix(name, prefix) {
			candidates = append(candidates, name)
		}
	}
	for name := range c.shell.pluginCmds {
		if strings.HasPrefix(name, prefix) {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)
	return toRuneSuffix(candidates, len(prefix))
}

// completePlugin walks the plugin command tree and completes either subcommand
// names or typed arguments (e.g. "username" → user list, "path" → file paths).
func (c *completer) completePlugin(cmds []plugin.CommandDef, args []string, atNewWord bool) ([][]rune, int) {
	// Walk resolved intermediate subcommands.
	sub := cmds
	consumed := args
	if !atNewWord && len(consumed) > 0 {
		consumed = consumed[:len(consumed)-1]
	}
	var matched *plugin.CommandDef
	for _, w := range consumed {
		found := false
		for i := range sub {
			if sub[i].Name == w {
				matched = &sub[i]
				if len(sub[i].Sub) > 0 {
					sub = sub[i].Sub
					matched = nil
				}
				found = true
				break
			}
		}
		if !found {
			return nil, 0
		}
	}

	// If we matched a leaf command, complete its args.
	if matched != nil && matched.Run != nil {
		argIdx := len(args) - len(consumed) - 1
		if atNewWord {
			argIdx++
		}
		partial := ""
		if !atNewWord && len(args) > 0 {
			partial = args[len(args)-1]
		}
		return c.completeArg(matched, argIdx, partial)
	}

	// Still at subcommand level.
	partial := ""
	if !atNewWord && len(args) > 0 {
		partial = args[len(args)-1]
	}
	var candidates []string
	for _, def := range sub {
		if strings.HasPrefix(def.Name, partial) {
			candidates = append(candidates, def.Name)
		}
	}
	sort.Strings(candidates)
	return toRuneSuffix(candidates, len(partial))
}

// completeArg completes a positional argument based on the arg name convention.
func (c *completer) completeArg(cmd *plugin.CommandDef, argIdx int, partial string) ([][]rune, int) {
	if argIdx < 0 || argIdx >= len(cmd.Args) {
		return nil, 0
	}
	argName := strings.ToLower(cmd.Args[argIdx])
	switch argName {
	case "username", "user":
		return c.completeUsernames(partial)
	case "path", "file", "dir", "directory":
		return c.completePath(partial)
	default:
		return nil, 0
	}
}

// completeUsernames completes against all known usernames.
func (c *completer) completeUsernames(partial string) ([][]rune, int) {
	if c.shell.usernames == nil {
		return nil, 0
	}
	names, err := c.shell.usernames()
	if err != nil {
		return nil, 0
	}
	var candidates []string
	for _, name := range names {
		if strings.HasPrefix(name, partial) {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)
	return toRuneSuffix(candidates, len(partial))
}

// completePath completes file paths relative to the current directory.
func (c *completer) completePath(partial string) ([][]rune, int) {
	dir := c.dir()

	// Split partial into directory and prefix components.
	dirPart := ""
	prefixPart := partial
	if i := strings.LastIndex(partial, "/"); i >= 0 {
		dirPart = partial[:i+1]
		prefixPart = partial[i+1:]
	}

	// Resolve the directory to search in.
	searchDir := dir
	if dirPart != "" {
		resolved, err := c.shell.resolve(dir, dirPart)
		if err != nil {
			return nil, 0
		}
		searchDir = resolved
	}

	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return nil, 0
	}

	var candidates []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefixPart, ".") {
			continue
		}
		if !strings.HasPrefix(name, prefixPart) {
			continue
		}
		display := dirPart + name
		if e.IsDir() {
			display += "/"
		}
		candidates = append(candidates, display)
	}
	sort.Strings(candidates)
	return toRuneSuffix(candidates, len(partial))
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
