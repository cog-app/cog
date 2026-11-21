package shell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/vfs"
)

// SpaceFunc reports, for a path in the session's VFS, what the account it is
// charged to holds and that account's quota, or -1 when nothing limits it.
type SpaceFunc func(path string) (used, quota int64, err error)

// SetSpace gives df its source of usage and quotas.
func (s *Shell) SetSpace(f SpaceFunc) { s.space = f }

type sizeFormat struct {
	human, bytes bool
}

// format renders n bytes as du and df do: 1K blocks rounded up by default.
func (f sizeFormat) format(n int64) string {
	switch {
	case f.human:
		return humanSize(n)
	case f.bytes:
		return strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt((n+1023)/1024, 10)
}

// humanSize renders n as GNU's -h does: powers of 1024, rounded up, with one
// decimal below 10.
func humanSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10)
	}
	const units = "KMGTPE"
	v := float64(n)
	i := -1
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v < 10 {
		// Round up to one decimal, as GNU does, so 1025 bytes is 1.1K.
		t := float64(int64(v*10)) / 10
		if t < v {
			t += 0.1
		}
		if t < 10 {
			return strconv.FormatFloat(t, 'f', 1, 64) + units[i:i+1]
		}
		v = t
	}
	r := int64(v)
	if float64(r) < v {
		r++
	}
	if r >= 1024 && i < len(units)-1 {
		return "1.0" + units[i+1:i+2]
	}
	return strconv.FormatInt(r, 10) + units[i:i+1]
}

func (s *Shell) newDuCmd() *cobra.Command {
	var (
		f                     sizeFormat
		summarize, all, total bool
		maxDepth              int
	)
	cmd := &cobra.Command{
		Use:               "du [flags] [path...]",
		Short:             "Estimate file space usage",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, dir := getHC(cmd)
			if len(args) == 0 {
				args = []string{"."}
			}
			if summarize {
				maxDepth = 0
			}
			out := bufio.NewWriter(stdout)
			failed := false
			var grand int64
			for _, arg := range args {
				n, ok := s.du(out, stderr, arg, s.toVirtualPath(dir, arg), f, all, maxDepth)
				grand += n
				failed = failed || !ok
			}
			if total {
				fmt.Fprintf(out, "%s\ttotal\n", f.format(grand))
			}
			if err := out.Flush(); err != nil {
				return err
			}
			if failed {
				return exitErr(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&f.human, "human-readable", "h", false, "Print sizes like 1K 234M 2G")
	cmd.Flags().BoolVarP(&f.bytes, "bytes", "b", false, "Print sizes in bytes")
	cmd.Flags().BoolVarP(&summarize, "summarize", "s", false, "Display only a total for each argument")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "Write counts for all files, not just directories")
	cmd.Flags().BoolVarP(&total, "total", "c", false, "Produce a grand total")
	cmd.Flags().IntVarP(&maxDepth, "max-depth", "d", -1, "Print a directory's total only if it is N or fewer levels below the argument")
	cmd.Flags().BoolP("help", "", false, "Help for du")
	return cmd
}

// du prints the usage below vp, children before their directory, and
// returns its total. The walk is depth first, so a directory's total is
// complete once the walk leaves it: open directories are kept on a stack and
// printed as they are popped.
func (s *Shell) du(out *bufio.Writer, stderr io.Writer, arg, vp string, f sizeFormat, all bool, maxDepth int) (int64, bool) {
	type open struct {
		display string
		depth   int
		size    int64
	}
	var stack []open
	ok := true
	display := func(p string) string {
		if p == vp {
			return arg
		}
		return strings.TrimSuffix(arg, "/") + p[len(vp):]
	}
	shown := func(depth int) bool { return maxDepth < 0 || depth <= maxDepth }
	pop := func() {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if shown(top.depth) {
			out.WriteString(f.format(top.size))
			out.WriteByte('\t')
			out.WriteString(top.display)
			out.WriteByte('\n')
		}
		if len(stack) > 0 {
			stack[len(stack)-1].size += top.size
		}
	}
	var root int64
	err := s.walk(vp, func(p string, fi *vfs.FileInfo, depth int) error {
		for len(stack) > 0 && stack[len(stack)-1].depth >= depth {
			pop()
		}
		if fi.IsDir {
			stack = append(stack, open{display: display(p), depth: depth})
			return nil
		}
		if len(stack) > 0 {
			stack[len(stack)-1].size += fi.Size
		} else {
			root = fi.Size
		}
		if depth == 0 || (all && shown(depth)) {
			out.WriteString(f.format(fi.Size))
			out.WriteByte('\t')
			out.WriteString(display(p))
			out.WriteByte('\n')
		}
		return nil
	}, func(p string, err error) {
		if ferr := out.Flush(); ferr != nil {
			s.log.Warn("du: flush", "err", ferr)
		}
		s.errorf(stderr, "du: %s: %v\n", display(p), err)
		ok = false
	})
	for len(stack) > 0 {
		if len(stack) == 1 {
			root = stack[0].size
		}
		pop()
	}
	if err != nil {
		if ferr := out.Flush(); ferr != nil {
			s.log.Warn("du: flush", "err", ferr)
		}
		s.errorf(stderr, "du: %s: %v\n", arg, err)
		return 0, false
	}
	return root, ok
}

func (s *Shell) newDfCmd() *cobra.Command {
	var f sizeFormat
	cmd := &cobra.Command{
		Use:               "df [flags] [path...]",
		Short:             "Report storage space used and available",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, dir := getHC(cmd)
			if s.space == nil {
				s.errorf(stderr, "df: %v\n", errors.ErrUnsupported)
				return exitErr(1)
			}
			if len(args) == 0 {
				args = []string{home}
			}
			sizeHead := "1K-blocks"
			if f.human {
				sizeHead = "Size"
			}
			out := bufio.NewWriter(stdout)
			fmt.Fprintf(out, "%-10s %10s %10s %10s %5s %s\n", "Filesystem", sizeHead, "Used", "Avail", "Use%", "Mounted on")
			failed := false
			for _, arg := range args {
				vp := s.toVirtualPath(dir, arg)
				used, quota, err := s.space(vp)
				if err != nil {
					if ferr := out.Flush(); ferr != nil {
						return ferr
					}
					s.errorf(stderr, "df: %s: %v\n", arg, err)
					failed = true
					continue
				}
				size, avail, pct := "-", "-", "-"
				if quota >= 0 {
					size = f.format(quota)
					avail = f.format(max(quota-used, 0))
					pct = "100%"
					if quota > 0 {
						pct = strconv.FormatInt((used*100+quota-1)/quota, 10) + "%"
					}
				}
				fmt.Fprintf(out, "%-10s %10s %10s %10s %5s %s\n", "cog", size, f.format(used), avail, pct, vp)
			}
			if err := out.Flush(); err != nil {
				return err
			}
			if failed {
				return exitErr(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&f.human, "human-readable", "h", false, "Print sizes like 1K 234M 2G")
	cmd.Flags().BoolP("help", "", false, "Help for df")
	return cmd
}
