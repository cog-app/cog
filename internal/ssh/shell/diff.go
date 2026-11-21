package shell

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"
	"unsafe"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/vfs"
)

type diffOpts struct {
	unified   bool
	context   int
	brief     bool
	recursive bool
}

// diffRun carries one diff invocation. status is the exit status so far: 1
// once anything differs, 2 once anything could not be compared.
type diffRun struct {
	s      *Shell
	opts   diffOpts
	out    *bufio.Writer
	stderr io.Writer
	stdin  io.Reader
	status uint8
	buf1   []byte
	buf2   []byte
}

func (s *Shell) newDiffCmd() *cobra.Command {
	var opts diffOpts
	var unifiedN int
	cmd := &cobra.Command{
		Use:               "diff [flags] <file1> <file2>",
		Short:             "Compare files line by line",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, stdin, dir := getHC(cmd)
			if cmd.Flags().Changed("unified") {
				opts.unified = true
				opts.context = unifiedN
			} else if opts.unified {
				opts.context = 3
			}
			d := &diffRun{s: s, opts: opts, out: bufio.NewWriter(stdout), stderr: stderr, stdin: stdin}
			d.compareArgs(s.diffPath(dir, args[0]), s.diffPath(dir, args[1]), args[0], args[1])
			if err := d.out.Flush(); err != nil {
				return err
			}
			if d.status != 0 {
				return exitErr(d.status)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&opts.unified, "u", "u", false, "Output 3 lines of unified context")
	cmd.Flags().IntVarP(&unifiedN, "unified", "U", 3, "Output N lines of unified context")
	cmd.Flags().BoolVarP(&opts.brief, "brief", "q", false, "Report only whether files differ")
	cmd.Flags().BoolVarP(&opts.recursive, "recursive", "r", false, "Recursively compare subdirectories")
	return cmd
}

func (s *Shell) diffPath(dir, p string) string {
	if p == "-" {
		return "-"
	}
	return s.toVirtualPath(dir, p)
}

func (d *diffRun) trouble(format string, a ...any) {
	d.s.errorf(d.stderr, "diff: "+format+"\n", a...)
	d.status = 2
}

func (d *diffRun) differ() {
	if d.status == 0 {
		d.status = 1
	}
}

func (d *diffRun) stat(p string) (*vfs.FileInfo, bool) {
	if p == "-" {
		return &vfs.FileInfo{Name: "-"}, true
	}
	fi, err := d.s.fs.Stat(p)
	if err != nil {
		d.trouble("%s: %v", p, err)
		return nil, false
	}
	return fi, true
}

func (d *diffRun) compareArgs(a, b, nameA, nameB string) {
	fa, ok := d.stat(a)
	if !ok {
		return
	}
	fb, ok := d.stat(b)
	if !ok {
		return
	}
	switch {
	case fa.IsDir && fb.IsDir:
		d.compareDirs(a, b, nameA, nameB)
	case fa.IsDir:
		a = path.Join(a, path.Base(b))
		nameA = path.Join(nameA, path.Base(b))
		if fa, ok = d.stat(a); ok {
			d.compareFiles(a, b, nameA, nameB, fa, fb)
		}
	case fb.IsDir:
		b = path.Join(b, path.Base(a))
		nameB = path.Join(nameB, path.Base(a))
		if fb, ok = d.stat(b); ok {
			d.compareFiles(a, b, nameA, nameB, fa, fb)
		}
	default:
		d.compareFiles(a, b, nameA, nameB, fa, fb)
	}
}

// compareDirs lists both directories once and merges the sorted names, so a
// tree is compared with one List per directory and no Stat per entry.
func (d *diffRun) compareDirs(a, b, nameA, nameB string) {
	la, err := d.s.fs.List(a)
	if err != nil {
		d.trouble("%s: %v", a, err)
		return
	}
	lb, err := d.s.fs.List(b)
	if err != nil {
		d.trouble("%s: %v", b, err)
		return
	}
	byName := func(x, y vfs.FileInfo) int { return strings.Compare(x.Name, y.Name) }
	slices.SortFunc(la, byName)
	slices.SortFunc(lb, byName)
	i, j := 0, 0
	for i < len(la) || j < len(lb) {
		var c int
		switch {
		case i == len(la):
			c = 1
		case j == len(lb):
			c = -1
		default:
			c = strings.Compare(la[i].Name, lb[j].Name)
		}
		switch {
		case c < 0:
			fmt.Fprintf(d.out, "Only in %s: %s\n", nameA, la[i].Name)
			d.differ()
			i++
		case c > 0:
			fmt.Fprintf(d.out, "Only in %s: %s\n", nameB, lb[j].Name)
			d.differ()
			j++
		default:
			ea, eb := &la[i], &lb[j]
			pa, pb := path.Join(a, ea.Name), path.Join(b, eb.Name)
			na, nb := path.Join(nameA, ea.Name), path.Join(nameB, eb.Name)
			switch {
			case ea.IsDir && eb.IsDir:
				if d.opts.recursive {
					d.compareDirs(pa, pb, na, nb)
				} else {
					fmt.Fprintf(d.out, "Common subdirectories: %s and %s\n", na, nb)
				}
			case ea.IsDir != eb.IsDir:
				kind := func(dir bool) string {
					if dir {
						return "directory"
					}
					return "regular file"
				}
				fmt.Fprintf(d.out, "File %s is a %s while file %s is a %s\n", na, kind(ea.IsDir), nb, kind(eb.IsDir))
				d.differ()
			default:
				d.compareFiles(pa, pb, na, nb, ea, eb)
			}
			i++
			j++
		}
	}
}

func (d *diffRun) open(p string) (io.ReadCloser, error) {
	if p == "-" {
		return io.NopCloser(d.stdin), nil
	}
	return d.s.fs.Get(p)
}

// same reports whether two files have the same bytes, reading both in step
// so a difference near the start costs one buffer.
func (d *diffRun) same(a, b string) (bool, error) {
	ra, err := d.open(a)
	if err != nil {
		return false, err
	}
	defer ra.Close()
	rb, err := d.open(b)
	if err != nil {
		return false, err
	}
	defer rb.Close()
	if d.buf1 == nil {
		d.buf1, d.buf2 = make([]byte, 64<<10), make([]byte, 64<<10)
	}
	for {
		na, errA := io.ReadFull(ra, d.buf1)
		nb, errB := io.ReadFull(rb, d.buf2)
		if !bytes.Equal(d.buf1[:na], d.buf2[:nb]) {
			return false, nil
		}
		doneA := errors.Is(errA, io.EOF) || errors.Is(errA, io.ErrUnexpectedEOF)
		doneB := errors.Is(errB, io.EOF) || errors.Is(errB, io.ErrUnexpectedEOF)
		if errA != nil && !doneA {
			return false, errA
		}
		if errB != nil && !doneB {
			return false, errB
		}
		if doneA || doneB {
			return doneA == doneB, nil
		}
	}
}

func (d *diffRun) readAll(p string) ([]byte, error) {
	if p == "-" {
		return io.ReadAll(d.stdin)
	}
	return d.s.fs.ReadFile(p)
}

func (d *diffRun) compareFiles(a, b, nameA, nameB string, fa, fb *vfs.FileInfo) {
	stdinInvolved := a == "-" || b == "-"
	if !stdinInvolved && fa.Size != fb.Size && d.opts.brief {
		fmt.Fprintf(d.out, "Files %s and %s differ\n", nameA, nameB)
		d.differ()
		return
	}
	if !stdinInvolved && fa.Size == fb.Size {
		same, err := d.same(a, b)
		if err != nil {
			d.trouble("%v", err)
			return
		}
		if same {
			return
		}
		if d.opts.brief {
			fmt.Fprintf(d.out, "Files %s and %s differ\n", nameA, nameB)
			d.differ()
			return
		}
	}
	da, err := d.readAll(a)
	if err != nil {
		d.trouble("%s: %v", nameA, err)
		return
	}
	db, err := d.readAll(b)
	if err != nil {
		d.trouble("%s: %v", nameB, err)
		return
	}
	if bytes.Equal(da, db) {
		return
	}
	d.differ()
	if d.opts.brief || isBinary(da) || isBinary(db) {
		fmt.Fprintf(d.out, "Files %s and %s differ\n", nameA, nameB)
		return
	}
	if d.opts.recursive && !stdinInvolved {
		fmt.Fprintf(d.out, "diff -r %s %s\n", nameA, nameB)
	}
	la, lb := splitLines(da), splitLines(db)
	changedA, changedB := diffLines(la.lines, lb.lines)
	if d.opts.unified {
		writeUnified(d.out, la, lb, changedA, changedB, d.opts.context, nameA, nameB, fa.ModTime, fb.ModTime)
	} else {
		writeNormal(d.out, la, lb, changedA, changedB)
	}
}

func isBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0
}

// fileLines holds a file's lines as substrings of one string, so splitting
// allocates the slice headers and nothing per line.
type fileLines struct {
	lines []string
	// noEOL is set when the last line has no terminating newline.
	noEOL bool
}

// splitLines takes ownership of data, which must not change afterwards: the
// lines alias it rather than copy the whole file.
func splitLines(data []byte) fileLines {
	text := unsafe.String(unsafe.SliceData(data), len(data))
	var fl fileLines
	fl.lines = make([]string, 0, strings.Count(text, "\n")+1)
	for len(text) > 0 {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			fl.lines = append(fl.lines, text)
			fl.noEOL = true
			break
		}
		fl.lines = append(fl.lines, text[:i])
		text = text[i+1:]
	}
	return fl
}

// diffLines marks which lines of a and b are not part of a longest common
// subsequence, found with Myers' linear-space O(ND) algorithm over interned
// line ids. A line found in only one file cannot be in any common
// subsequence, so it is marked before the search and left out of it, which
// takes the cost of unrelated stretches from O(ND) to O(N).
func diffLines(a, b []string) (changedA, changedB []bool) {
	ids := make(map[string]int32, len(a))
	var inA, inB []bool
	intern := func(lines []string, seen *[]bool) []int32 {
		out := make([]int32, len(lines))
		for i, l := range lines {
			id, ok := ids[l]
			if !ok {
				id = int32(len(ids))
				ids[l] = id
				inA = append(inA, false)
				inB = append(inB, false)
			}
			(*seen)[id] = true
			out[i] = id
		}
		return out
	}
	idsA := intern(a, &inA)
	idsB := intern(b, &inB)
	changedA = make([]bool, len(a))
	changedB = make([]bool, len(b))
	keep := func(ids []int32, other, changed []bool) (kept []int32, at []int) {
		kept = make([]int32, 0, len(ids))
		at = make([]int, 0, len(ids))
		for i, id := range ids {
			if other[id] {
				kept = append(kept, id)
				at = append(at, i)
			} else {
				changed[i] = true
			}
		}
		return kept, at
	}
	keptA, atA := keep(idsA, inB, changedA)
	keptB, atB := keep(idsB, inA, changedB)
	m := &myers{a: keptA, b: keptB, changedA: make([]bool, len(keptA)), changedB: make([]bool, len(keptB))}
	m.compare(0, len(keptA), 0, len(keptB))
	for i, c := range m.changedA {
		changedA[atA[i]] = c
	}
	for j, c := range m.changedB {
		changedB[atB[j]] = c
	}
	return changedA, changedB
}

type myers struct {
	a, b               []int32
	changedA, changedB []bool
	v1, v2             []int
}

// maxMyersCost bounds the edit distance searched for an exact middle snake.
// Past it the furthest-reaching forward point is used as the split, which
// keeps wholly different large files from costing O(N²) at the price of a
// diff that may not be minimal, as GNU diff's heuristic does.
const maxMyersCost = 4096

func (m *myers) compare(aLo, aHi, bLo, bHi int) {
	for aLo < aHi && bLo < bHi && m.a[aLo] == m.b[bLo] {
		aLo++
		bLo++
	}
	for aLo < aHi && bLo < bHi && m.a[aHi-1] == m.b[bHi-1] {
		aHi--
		bHi--
	}
	switch {
	case aLo == aHi:
		for j := bLo; j < bHi; j++ {
			m.changedB[j] = true
		}
	case bLo == bHi:
		for i := aLo; i < aHi; i++ {
			m.changedA[i] = true
		}
	default:
		x, y := m.split(aLo, aHi, bLo, bHi)
		if (x == aLo && y == bLo) || (x == aHi && y == bHi) {
			for i := aLo; i < aHi; i++ {
				m.changedA[i] = true
			}
			for j := bLo; j < bHi; j++ {
				m.changedB[j] = true
			}
			return
		}
		m.compare(aLo, x, bLo, y)
		m.compare(x, aHi, y, bHi)
	}
}

// split returns a point on an optimal edit path through the box, where the
// forward and reverse searches meet.
func (m *myers) split(aLo, aHi, bLo, bHi int) (int, int) {
	n, mm := aHi-aLo, bHi-bLo
	maxD := (n + mm + 1) / 2
	vOff := maxD
	vLen := 2*maxD + 2
	if cap(m.v1) < vLen {
		m.v1 = make([]int, vLen)
		m.v2 = make([]int, vLen)
	}
	v1, v2 := m.v1[:vLen], m.v2[:vLen]
	for i := range v1 {
		v1[i] = -1
		v2[i] = -1
	}
	v1[vOff+1] = 0
	v2[vOff+1] = 0
	delta := n - mm
	front := delta%2 != 0
	k1start, k1end, k2start, k2end := 0, 0, 0, 0
	a, b := m.a, m.b
	for d := range maxD {
		if d > maxMyersCost {
			bestX, bestY := 0, 0
			for k := -d + k1start; k <= d-k1end; k += 2 {
				x := v1[vOff+k]
				y := x - k
				if x >= 0 && x <= n && y >= 0 && y <= mm && x+y > bestX+bestY {
					bestX, bestY = x, y
				}
			}
			return aLo + bestX, bLo + bestY
		}
		for k1 := -d + k1start; k1 <= d-k1end; k1 += 2 {
			k1Off := vOff + k1
			var x1 int
			if k1 == -d || (k1 != d && v1[k1Off-1] < v1[k1Off+1]) {
				x1 = v1[k1Off+1]
			} else {
				x1 = v1[k1Off-1] + 1
			}
			y1 := x1 - k1
			for x1 < n && y1 < mm && a[aLo+x1] == b[bLo+y1] {
				x1++
				y1++
			}
			v1[k1Off] = x1
			switch {
			case x1 > n:
				k1end += 2
			case y1 > mm:
				k1start += 2
			case front:
				k2Off := vOff + delta - k1
				if k2Off >= 0 && k2Off < vLen && v2[k2Off] != -1 && x1 >= n-v2[k2Off] {
					return aLo + x1, bLo + y1
				}
			}
		}
		for k2 := -d + k2start; k2 <= d-k2end; k2 += 2 {
			k2Off := vOff + k2
			var x2 int
			if k2 == -d || (k2 != d && v2[k2Off-1] < v2[k2Off+1]) {
				x2 = v2[k2Off+1]
			} else {
				x2 = v2[k2Off-1] + 1
			}
			y2 := x2 - k2
			for x2 < n && y2 < mm && a[aHi-x2-1] == b[bHi-y2-1] {
				x2++
				y2++
			}
			v2[k2Off] = x2
			switch {
			case x2 > n:
				k2end += 2
			case y2 > mm:
				k2start += 2
			case !front:
				k1Off := vOff + delta - k2
				if k1Off >= 0 && k1Off < vLen && v1[k1Off] != -1 {
					x1 := v1[k1Off]
					y1 := vOff + x1 - k1Off
					if x1 >= n-x2 {
						return aLo + x1, bLo + y1
					}
				}
			}
		}
	}
	return aLo, bLo
}

// hunk is a run of changed lines: a[a0:a1] replaced by b[b0:b1].
type hunk struct{ a0, a1, b0, b1 int }

func changeRuns(changedA, changedB []bool) []hunk {
	var hs []hunk
	i, j := 0, 0
	for i < len(changedA) || j < len(changedB) {
		if i < len(changedA) && j < len(changedB) && !changedA[i] && !changedB[j] {
			i++
			j++
			continue
		}
		h := hunk{a0: i, b0: j}
		for i < len(changedA) && changedA[i] {
			i++
		}
		for j < len(changedB) && changedB[j] {
			j++
		}
		h.a1, h.b1 = i, j
		hs = append(hs, h)
	}
	return hs
}

func writeRange(w *bufio.Writer, lo, hi int) {
	if hi-lo <= 1 {
		fmt.Fprintf(w, "%d", max(hi, lo))
		return
	}
	fmt.Fprintf(w, "%d,%d", lo+1, hi)
}

func writeLine(w *bufio.Writer, prefix string, fl fileLines, i int) {
	w.WriteString(prefix)
	w.WriteString(fl.lines[i])
	w.WriteByte('\n')
	if fl.noEOL && i == len(fl.lines)-1 {
		w.WriteString("\\ No newline at end of file\n")
	}
}

func writeNormal(w *bufio.Writer, a, b fileLines, changedA, changedB []bool) {
	for _, h := range changeRuns(changedA, changedB) {
		switch {
		case h.a0 == h.a1:
			fmt.Fprintf(w, "%d", h.a0)
			w.WriteByte('a')
			writeRange(w, h.b0, h.b1)
		case h.b0 == h.b1:
			writeRange(w, h.a0, h.a1)
			w.WriteByte('d')
			fmt.Fprintf(w, "%d", h.b0)
		default:
			writeRange(w, h.a0, h.a1)
			w.WriteByte('c')
			writeRange(w, h.b0, h.b1)
		}
		w.WriteByte('\n')
		for i := h.a0; i < h.a1; i++ {
			writeLine(w, "< ", a, i)
		}
		if h.a0 != h.a1 && h.b0 != h.b1 {
			w.WriteString("---\n")
		}
		for j := h.b0; j < h.b1; j++ {
			writeLine(w, "> ", b, j)
		}
	}
}

func unifiedRange(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if count == 1 {
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}

const diffTimeFormat = "2006-01-02 15:04:05.000000000 -0700"

func writeUnified(w *bufio.Writer, a, b fileLines, changedA, changedB []bool, ctx int, nameA, nameB string, timeA, timeB time.Time) {
	runs := changeRuns(changedA, changedB)
	if len(runs) == 0 {
		return
	}
	fmt.Fprintf(w, "--- %s\t%s\n+++ %s\t%s\n", nameA, timeA.Format(diffTimeFormat), nameB, timeB.Format(diffTimeFormat))
	for start := 0; start < len(runs); {
		end := start + 1
		for end < len(runs) && runs[end].a0-runs[end-1].a1 <= 2*ctx {
			end++
		}
		first, last := runs[start], runs[end-1]
		a0 := max(first.a0-ctx, 0)
		b0 := first.b0 - (first.a0 - a0)
		a1 := min(last.a1+ctx, len(a.lines))
		b1 := last.b1 + (a1 - last.a1)
		fmt.Fprintf(w, "@@ -%s +%s @@\n", unifiedRange(a0, a1-a0), unifiedRange(b0, b1-b0))
		i, j := a0, b0
		for _, h := range runs[start:end] {
			for ; i < h.a0; i, j = i+1, j+1 {
				writeLine(w, " ", a, i)
			}
			for ; i < h.a1; i++ {
				writeLine(w, "-", a, i)
			}
			for ; j < h.b1; j++ {
				writeLine(w, "+", b, j)
			}
		}
		for ; i < a1; i, j = i+1, j+1 {
			writeLine(w, " ", a, i)
		}
		start = end
	}
}
