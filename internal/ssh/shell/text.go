package shell

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/vfs"
)

const textBufSize = 64 << 10

// eachInput calls fn with the content of each named input in turn, or of
// stdin when there are none; "-" also names stdin. An input that cannot be
// opened is reported and skipped; ok is false if that happened.
func (s *Shell) eachInput(name string, cmd *cobra.Command, args []string, fn func(io.Reader) error) (ok bool, err error) {
	_, stderr, stdin, dir := getHC(cmd)
	if len(args) == 0 {
		args = []string{"-"}
	}
	ok = true
	for _, a := range args {
		if a == "-" {
			if err := fn(stdin); err != nil {
				return false, err
			}
			continue
		}
		rc, err := s.fs.Get(s.toVirtualPath(dir, a))
		if err != nil {
			s.errorf(stderr, "%s: %s: %v\n", name, a, err)
			ok = false
			continue
		}
		err = fn(rc)
		if cerr := rc.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return false, err
		}
	}
	return ok, nil
}

// scanLines is bufio.ScanLines without dropping a trailing \r: sort and uniq
// pass lines through, so CRLF input stays CRLF.
func scanLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func newLineScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, textBufSize), math.MaxInt32)
	sc.Split(scanLines)
	return sc
}

func (s *Shell) newUniqCmd() *cobra.Command {
	var count, repeated, unique, ignoreCase bool
	cmd := &cobra.Command{
		Use:               "uniq [flags] [input]",
		Short:             "Report or omit repeated lines",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, _, _, _ := getHC(cmd)
			w := bufio.NewWriterSize(stdout, textBufSize)
			var prev []byte
			num := make([]byte, 0, 20)
			n := 0
			emit := func() {
				if n == 0 || (repeated && n < 2) || (unique && n > 1) {
					return
				}
				if count {
					num = strconv.AppendInt(num[:0], int64(n), 10)
					for range 7 - len(num) {
						w.WriteByte(' ')
					}
					w.Write(num)
					w.WriteByte(' ')
				}
				w.Write(prev)
				w.WriteByte('\n')
			}
			same := bytes.Equal
			if ignoreCase {
				same = bytes.EqualFold
			}
			ok, err := s.eachInput("uniq", cmd, args, func(r io.Reader) error {
				sc := newLineScanner(r)
				for sc.Scan() {
					line := sc.Bytes()
					if n > 0 && same(prev, line) {
						n++
						continue
					}
					emit()
					prev = append(prev[:0], line...)
					n = 1
				}
				return sc.Err()
			})
			if err != nil {
				return err
			}
			emit()
			if err := w.Flush(); err != nil {
				return err
			}
			if !ok {
				return exitErr(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&count, "count", "c", false, "Prefix lines by the number of occurrences")
	cmd.Flags().BoolVarP(&repeated, "repeated", "d", false, "Only print duplicate lines, one for each group")
	cmd.Flags().BoolVarP(&unique, "unique", "u", false, "Only print unique lines")
	cmd.Flags().BoolVarP(&ignoreCase, "ignore-case", "i", false, "Ignore differences in case when comparing")
	return cmd
}

func (s *Shell) newTeeCmd() *cobra.Command {
	var appendTo bool
	cmd := &cobra.Command{
		Use:               "tee [flags] [file...]",
		Short:             "Copy standard input to standard output and files",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, stdin, dir := getHC(cmd)
			type output struct {
				name string
				w    io.Writer
				f    vfs.File
			}
			outs := []output{{name: "standard output", w: stdout}}
			failed := false
			if len(args) > 0 {
				opener, ok := s.fs.(vfs.FileOpener)
				if !ok {
					s.errorf(stderr, "tee: %v\n", errors.ErrUnsupported)
					return exitErr(1)
				}
				flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
				if appendTo {
					flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
				}
				for _, name := range args {
					f, err := opener.OpenFile(s.toVirtualPath(dir, name), flag, 0o644)
					if err != nil {
						s.errorf(stderr, "tee: %s: %v\n", name, err)
						failed = true
						continue
					}
					outs = append(outs, output{name: name, w: f, f: f})
				}
			}
			buf := make([]byte, textBufSize)
			var readErr error
			for {
				n, err := stdin.Read(buf)
				if n > 0 {
					// A failed output is dropped and the rest still get
					// everything, as GNU tee does.
					for i := range outs {
						if outs[i].w == nil {
							continue
						}
						if _, werr := outs[i].w.Write(buf[:n]); werr != nil {
							s.errorf(stderr, "tee: %s: %v\n", outs[i].name, werr)
							outs[i].w = nil
							failed = true
						}
					}
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					readErr = err
					break
				}
			}
			for _, o := range outs {
				if o.f == nil {
					continue
				}
				if err := o.f.Close(); err != nil {
					s.errorf(stderr, "tee: %s: %v\n", o.name, err)
					failed = true
				}
			}
			if readErr != nil {
				return readErr
			}
			if failed {
				return exitErr(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&appendTo, "append", "a", false, "Append to the files rather than overwrite them")
	return cmd
}

func (s *Shell) newSeqCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "seq [-w] [-s sep] [first [increment]] last",
		Short: "Print a sequence of numbers",
		// Negative numbers are operands, not flags.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, _ := getHC(cmd)
			sep, equalWidth := "\n", false
			var nums []string
			for i := 0; i < len(args); i++ {
				a := args[i]
				switch {
				case a == "-w" || a == "--equal-width":
					equalWidth = true
				case a == "-s" && i+1 < len(args):
					i++
					sep = args[i]
				case strings.HasPrefix(a, "-s") && !isNumber(a):
					sep = a[2:]
				case strings.HasPrefix(a, "--separator="):
					sep = strings.TrimPrefix(a, "--separator=")
				case a == "--help":
					fmt.Fprint(stdout, cmd.UsageString())
					return nil
				default:
					nums = append(nums, a)
				}
			}
			if len(nums) < 1 || len(nums) > 3 {
				s.errorf(stderr, "seq: expected 1 to 3 numbers, got %d\n", len(nums))
				return exitErr(1)
			}
			first, inc, last := "1", "1", nums[len(nums)-1]
			if len(nums) >= 2 {
				first = nums[0]
			}
			if len(nums) == 3 {
				inc = nums[1]
			}
			w := bufio.NewWriterSize(stdout, textBufSize)
			if err := writeSeq(w, first, inc, last, sep, equalWidth); err != nil {
				s.errorf(stderr, "seq: %v\n", err)
				return exitErr(1)
			}
			return w.Flush()
		},
	}
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func writeSeq(w *bufio.Writer, firstS, incS, lastS, sep string, equalWidth bool) error {
	fi, err1 := strconv.ParseInt(firstS, 10, 64)
	ii, err2 := strconv.ParseInt(incS, 10, 64)
	li, err3 := strconv.ParseInt(lastS, 10, 64)
	if err1 == nil && err2 == nil && err3 == nil {
		if ii == 0 {
			return errors.New("increment must not be 0")
		}
		width := 0
		if equalWidth {
			width = max(len(strconv.FormatInt(fi, 10)), len(strconv.FormatInt(li, 10)))
		}
		var num [24]byte
		wrote := false
		for v := fi; (ii > 0 && v <= li) || (ii < 0 && v >= li); v += ii {
			if wrote {
				w.WriteString(sep)
			}
			wrote = true
			b := strconv.AppendInt(num[:0], v, 10)
			writePadded(w, b, width)
			if (ii > 0 && v > math.MaxInt64-ii) || (ii < 0 && v < math.MinInt64-ii) {
				break
			}
		}
		if wrote {
			w.WriteByte('\n')
		}
		return nil
	}

	var vals [3]float64
	for i, s := range []string{firstS, incS, lastS} {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(v) {
			return fmt.Errorf("invalid floating point argument: %q", s)
		}
		vals[i] = v
	}
	first, inc, last := vals[0], vals[1], vals[2]
	if inc == 0 {
		return errors.New("increment must not be 0")
	}
	prec := max(decimals(firstS), decimals(incS))
	width := 0
	if equalWidth {
		width = max(len(strconv.FormatFloat(first, 'f', prec, 64)), len(strconv.FormatFloat(last, 'f', prec, 64)))
	}
	// Each value is computed from first, not accumulated, so error does not
	// build up; the tolerance lets 0.1 steps reach a last of 1.
	tol := math.Abs(inc) * 1e-9
	var num [64]byte
	wrote := false
	for i := 0; ; i++ {
		v := first + float64(i)*inc
		if (inc > 0 && v > last+tol) || (inc < 0 && v < last-tol) {
			break
		}
		if wrote {
			w.WriteString(sep)
		}
		wrote = true
		writePadded(w, strconv.AppendFloat(num[:0], v, 'f', prec, 64), width)
	}
	if wrote {
		w.WriteByte('\n')
	}
	return nil
}

func writePadded(w *bufio.Writer, b []byte, width int) {
	if len(b) > 0 && b[0] == '-' {
		w.WriteByte('-')
		b = b[1:]
		width--
	}
	for range width - len(b) {
		w.WriteByte('0')
	}
	w.Write(b)
}

func decimals(s string) int {
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return len(s) - i - 1
	}
	return 0
}
