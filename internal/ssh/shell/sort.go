package shell

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// sortOrder is how keys compare. A key with ordering letters of its own
// (-k2,2nr) uses only those; one without takes the global flags, as in GNU sort.
type sortOrder struct {
	numeric, human, fold, reverse, skipBlanks bool
}

func (o sortOrder) parses() bool { return o.numeric || o.human }

type sortOptions struct {
	global sortOrder
	unique bool
	sep    int // field separator, or -1 for blank runs
	keys   []sortKey
}

// sortKey is a -k field range, 1-based; endField 0 is the end of the line.
type sortKey struct {
	startField, endField int
	order                sortOrder
}

// sortVal is a line's key, extracted once so comparisons never re-parse it.
// Offsets are into the input rather than slices of it, which keeps a million
// keys in 28 MB rather than 80.
type sortVal struct {
	start, end         int32 // the key's text
	intStart, intEnd   int32 // integer digits without leading zeros
	fracStart, fracEnd int32 // fraction digits without trailing zeros
	neg                bool
	suffix             int8 // -h: 0 for none, then K M G T P E Z Y
}

// errSortTooLarge: offsets into the input are int32.
var errSortTooLarge = errors.New("input too large to sort (over 2 GiB)")

func (s *Shell) newSortCmd() *cobra.Command {
	var o sortOptions
	var keySpecs []string
	var sep string
	cmd := &cobra.Command{
		Use:               "sort [flags] [file...]",
		Short:             "Sort lines of text",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, _ := getHC(cmd)
			o.sep = -1
			if sep != "" {
				if len(sep) != 1 {
					s.errorf(stderr, "sort: multi-character tab %q\n", sep)
					return exitErr(2)
				}
				o.sep = int(sep[0])
			}
			for _, spec := range keySpecs {
				k, err := parseSortKey(spec, o.global)
				if err != nil {
					s.errorf(stderr, "sort: %v\n", err)
					return exitErr(2)
				}
				o.keys = append(o.keys, k)
			}
			var data bytes.Buffer
			ok, err := s.eachInput("sort", cmd, args, func(r io.Reader) error {
				if st, ok := r.(interface{ Stat() (fs.FileInfo, error) }); ok {
					if fi, err := st.Stat(); err == nil && fi.Mode().IsRegular() {
						data.Grow(int(fi.Size()) + 1)
					}
				}
				if _, err := data.ReadFrom(r); err != nil {
					return err
				}
				if b := data.Bytes(); len(b) > 0 && b[len(b)-1] != '\n' {
					data.WriteByte('\n')
				}
				if data.Len() > math.MaxInt32 {
					return errSortTooLarge
				}
				return nil
			})
			if errors.Is(err, errSortTooLarge) {
				s.errorf(stderr, "sort: %v\n", err)
				return exitErr(2)
			}
			if err != nil {
				return err
			}
			w := bufio.NewWriterSize(stdout, textBufSize)
			sortLines(data.Bytes(), &o, func(line []byte) { w.Write(line) })
			if err := w.Flush(); err != nil {
				return err
			}
			if !ok {
				return exitErr(2)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&o.global.reverse, "reverse", "r", false, "Reverse the result of comparisons")
	f.BoolVarP(&o.global.numeric, "numeric-sort", "n", false, "Compare by numerical value")
	f.BoolVarP(&o.global.human, "human-numeric-sort", "h", false, "Compare human readable numbers (2K 1G)")
	f.BoolVarP(&o.global.fold, "ignore-case", "f", false, "Fold lower case to upper case")
	f.BoolVarP(&o.global.skipBlanks, "ignore-leading-blanks", "b", false, "Ignore leading blanks in keys")
	f.BoolVarP(&o.unique, "unique", "u", false, "Output only the first of an equal run")
	f.StringArrayVarP(&keySpecs, "key", "k", nil, "Sort by fields N[,M], each optionally followed by bfhnr")
	f.StringVarP(&sep, "field-separator", "t", "", "Use SEP instead of blanks between fields")
	// Declaring help without a shorthand leaves -h for human-numeric, as in
	// GNU sort.
	f.Bool("help", false, "Help for sort")
	return cmd
}

// parseSortKey parses N[,M], each number optionally followed by ordering
// letters.
func parseSortKey(spec string, global sortOrder) (sortKey, error) {
	var k sortKey
	own := false
	field := func(s string) (int, error) {
		s = strings.TrimRightFunc(s, func(r rune) bool {
			switch r {
			case 'n':
				k.order.numeric = true
			case 'r':
				k.order.reverse = true
			case 'f':
				k.order.fold = true
			case 'h':
				k.order.human = true
			case 'b':
				k.order.skipBlanks = true
			default:
				return false
			}
			own = true
			return true
		})
		if strings.Contains(s, ".") {
			return 0, fmt.Errorf("key %q: character positions are not supported", spec)
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("invalid key %q", spec)
		}
		return n, nil
	}
	start, end, hasEnd := strings.Cut(spec, ",")
	var err error
	if k.startField, err = field(start); err != nil {
		return k, err
	}
	if hasEnd {
		if k.endField, err = field(end); err != nil {
			return k, err
		}
	}
	if !own {
		k.order = global
	}
	return k, nil
}

// sortItem is what is sorted: small, so most comparisons, settled by the
// prefix alone, never touch the keys and lines scattered through memory.
type sortItem struct {
	prefix uint64
	line   int32
}

// sortLines calls emit with each line of data, which is at most 2 GiB and
// ends in a newline, in sorted order; each line keeps its newline.
func sortLines(data []byte, o *sortOptions, emit func([]byte)) {
	n := bytes.Count(data, []byte{'\n'})
	keys := o.keys
	if len(keys) == 0 {
		keys = []sortKey{{startField: 1, order: o.global}}
	}
	nk := len(keys)
	starts := make([]int32, n+1)
	vals := make([]sortVal, n*nk)
	items := make([]sortItem, n)
	pos := 0
	for i := range n {
		end := pos + bytes.IndexByte(data[pos:], '\n')
		starts[i] = int32(pos)
		for j := range keys {
			fs, fe := o.field(data[pos:end], &keys[j])
			vals[i*nk+j] = keyValue(data, pos+fs, pos+fe, keys[j].order)
		}
		items[i] = sortItem{prefix: keyPrefix(data, &vals[i*nk], keys[0].order), line: int32(i)}
		pos = end + 1
	}
	starts[n] = int32(pos)
	line := func(i int32) []byte { return data[starts[i] : starts[i+1]-1] }

	// Equal keys fall back to the whole line, as GNU sort's last-resort
	// comparison does, except under -u, where key equality is what counts.
	g := o.global
	lastResort := !o.unique && (len(o.keys) > 0 || g.parses() || g.fold || g.skipBlanks)
	first := keys[0].order
	keyCmp := func(a, b sortItem) int {
		if a.prefix != b.prefix {
			if (a.prefix < b.prefix) != first.reverse {
				return -1
			}
			return 1
		}
		va, vb := vals[int(a.line)*nk:], vals[int(b.line)*nk:]
		for j := range keys {
			c := compareKeys(data, &va[j], &vb[j], keys[j].order)
			if keys[j].order.reverse {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		if !lastResort {
			return 0
		}
		if g.reverse {
			return bytes.Compare(line(b.line), line(a.line))
		}
		return bytes.Compare(line(a.line), line(b.line))
	}
	cmp := keyCmp
	if o.unique {
		// -u keeps the first of each equal run in input order; breaking ties
		// by position does that and is faster than a stable sort.
		cmp = func(a, b sortItem) int {
			if c := keyCmp(a, b); c != 0 {
				return c
			}
			return int(a.line - b.line)
		}
	}
	slices.SortFunc(items, cmp)

	for i, it := range items {
		if o.unique && i > 0 && keyCmp(items[i-1], it) == 0 {
			continue
		}
		emit(data[starts[it.line]:starts[it.line+1]])
	}
}

// keyPrefix maps v to a number that orders as compareKeys does, except that
// different keys may share one. For text it is the first eight bytes; for
// numbers the sign, -h suffix, count of integer digits and the first fifteen
// significant digits.
func keyPrefix(data []byte, v *sortVal, ord sortOrder) uint64 {
	if !ord.parses() {
		text := data[v.start:v.end]
		var p uint64
		for i := range 8 {
			p <<= 8
			if i < len(text) {
				c := text[i]
				if ord.fold && c >= 'a' && c <= 'z' {
					c -= 'a' - 'A'
				}
				p |= uint64(c)
			}
		}
		return p
	}
	var digits uint64
	n := 0
	for _, part := range [][]byte{data[v.intStart:v.intEnd], data[v.fracStart:v.fracEnd]} {
		for _, c := range part {
			if n == 15 {
				break
			}
			digits = digits*10 + uint64(c-'0')
			n++
		}
	}
	for ; n < 15; n++ {
		digits *= 10
	}
	mag := uint64(v.suffix)<<59 | uint64(min(v.intEnd-v.intStart, 63))<<53 | digits
	if v.neg {
		return 1<<63 - 1 - mag
	}
	return 1<<63 | mag
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

// field returns the offsets in line of the part k selects. Without -t a field
// is a run of blanks and the non-blanks after it, so the blanks belong to the
// field, as in GNU sort.
func (o *sortOptions) field(line []byte, k *sortKey) (start, end int) {
	start = o.skipFields(line, 0, k.startField-1)
	end = len(line)
	if k.endField > 0 {
		end = o.skipFields(line, 0, k.endField)
		if o.sep >= 0 && end > start && line[end-1] == byte(o.sep) {
			end--
		}
	}
	if start >= end {
		return start, start
	}
	if k.order.skipBlanks {
		for start < end && isBlank(line[start]) {
			start++
		}
	}
	return start, end
}

// skipFields returns the offset after n fields from pos.
func (o *sortOptions) skipFields(line []byte, pos, n int) int {
	for ; n > 0 && pos < len(line); n-- {
		if o.sep >= 0 {
			i := bytes.IndexByte(line[pos:], byte(o.sep))
			if i < 0 {
				return len(line)
			}
			pos += i + 1
			continue
		}
		for pos < len(line) && isBlank(line[pos]) {
			pos++
		}
		for pos < len(line) && !isBlank(line[pos]) {
			pos++
		}
	}
	return pos
}

func keyValue(data []byte, start, end int, ord sortOrder) sortVal {
	v := sortVal{start: int32(start), end: int32(end)}
	if !ord.parses() {
		return v
	}
	i := start
	for i < end && isBlank(data[i]) {
		i++
	}
	if i < end && data[i] == '-' {
		v.neg = true
		i++
	}
	for i < end && data[i] == '0' {
		i++
	}
	v.intStart = int32(i)
	for i < end && data[i] >= '0' && data[i] <= '9' {
		i++
	}
	v.intEnd = int32(i)
	v.fracStart, v.fracEnd = int32(i), int32(i)
	if i < end && data[i] == '.' {
		i++
		v.fracStart = int32(i)
		for i < end && data[i] >= '0' && data[i] <= '9' {
			i++
		}
		f := i
		for f > int(v.fracStart) && data[f-1] == '0' {
			f--
		}
		v.fracEnd = int32(f)
	}
	if ord.human && i < end {
		if data[i] == 'k' {
			v.suffix = 1
		} else {
			v.suffix = int8(max(strings.IndexByte(" KMGTPEZY", data[i]), 0))
		}
	}
	if v.intStart == v.intEnd && v.fracStart == v.fracEnd {
		v.neg, v.suffix = false, 0
	}
	return v
}

func compareKeys(data []byte, a, b *sortVal, ord sortOrder) int {
	if ord.parses() {
		if a.neg != b.neg {
			if a.neg {
				return -1
			}
			return 1
		}
		c := 0
		if ord.human {
			c = int(a.suffix) - int(b.suffix)
		}
		if c == 0 {
			c = int(a.intEnd-a.intStart) - int(b.intEnd-b.intStart)
		}
		if c == 0 {
			c = bytes.Compare(data[a.intStart:a.intEnd], data[b.intStart:b.intEnd])
		}
		if c == 0 {
			c = bytes.Compare(data[a.fracStart:a.fracEnd], data[b.fracStart:b.fracEnd])
		}
		if a.neg {
			return -c
		}
		return c
	}
	if ord.fold {
		return foldCompare(data[a.start:a.end], data[b.start:b.end])
	}
	return bytes.Compare(data[a.start:a.end], data[b.start:b.end])
}

// foldCompare compares as bytes with ASCII lower case taken as upper case,
// as GNU sort -f does in the C locale.
func foldCompare(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		ca, cb := a[i], b[i]
		if ca >= 'a' && ca <= 'z' {
			ca -= 'a' - 'A'
		}
		if cb >= 'a' && cb <= 'z' {
			cb -= 'a' - 'A'
		}
		if ca != cb {
			return int(ca) - int(cb)
		}
	}
	return len(a) - len(b)
}
