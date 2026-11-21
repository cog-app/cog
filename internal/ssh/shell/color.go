package shell

import (
	"fmt"
	"io"
)

const (
	ansiReset     = "\033[0m"
	ansiBold      = "\033[1m"
	ansiDim       = "\033[2m"
	ansiRed       = "\033[31m"
	ansiGreen     = "\033[32m"
	ansiYellow    = "\033[33m"
	ansiBlue      = "\033[34m"
	ansiMagenta   = "\033[35m"
	ansiCyan      = "\033[36m"
	ansiUnderline = "\033[4m"
	ansiBoldRed   = "\033[1;31m"
	ansiBoldGreen = "\033[1;32m"
	ansiBoldBlue  = "\033[1;34m"
)

// colorize wraps text in ANSI color codes if the shell has colors enabled.
func (s *Shell) colorize(code, text string) string {
	if !s.color {
		return text
	}
	return code + text + ansiReset
}

// lsName returns the entry name, colorized bold blue if it's a directory.
func (s *Shell) lsName(name string, isDir bool) string {
	if isDir {
		return s.colorize(ansiBoldBlue, name)
	}
	return name
}

// errorf writes a formatted error message to w, colored red if colors are enabled.
func (s *Shell) errorf(w io.Writer, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if s.color {
		fmt.Fprint(w, ansiRed+msg+ansiReset)
	} else {
		fmt.Fprint(w, msg)
	}
}
