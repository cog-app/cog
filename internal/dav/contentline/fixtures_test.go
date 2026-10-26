package contentline

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// fixtureRoots holds the committed corpus, plus local-only directories
// (gitignored personal exports and GPL-licensed client output) that are
// checked when present.
var fixtureRoots = []string{
	"../../../testdata/dav",
	"../../../tmp/dav-export",
	"../../../tmp/dav-fixtures-gpl",
}

func fixtureFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, root := range fixtureRoots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".ics", ".vcf", ".ical", ".vcard", ".ifb":
				// Files under an "invalid" directory exist to be rejected.
				if !strings.Contains(filepath.ToSlash(path), "/invalid/") {
					files = append(files, path)
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
	return files
}

// TestFixturesRoundTrip checks Write(Parse(x)) against x for every fixture,
// up to line endings, folding, blank lines, and quoting of parameter values
// that don't need it.
func TestFixturesRoundTrip(t *testing.T) {
	files := fixtureFiles(t)
	if len(files) == 0 {
		t.Fatal("no fixtures found under testdata/dav")
	}
	for _, path := range files {
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			in, err := os.ReadFile(path)
			require.NoError(t, err)
			comps, err := Parse(bytes.NewReader(in))
			require.NoError(t, err)

			var out bytes.Buffer
			require.NoError(t, Write(&out, comps))

			want, got := normalise(in), normalise(out.Bytes())
			if !reflect.DeepEqual(want, got) {
				t.Fatal(firstDiff(want, got))
			}

			again, err := Parse(bytes.NewReader(out.Bytes()))
			require.NoError(t, err)
			if !reflect.DeepEqual(comps, again) {
				t.Fatal("Parse(Write(tree)) differs from tree")
			}

			var out2 bytes.Buffer
			require.NoError(t, Write(&out2, again))
			if !bytes.Equal(out.Bytes(), out2.Bytes()) {
				t.Fatal("Write is not deterministic")
			}
		})
	}
}

// normalise unfolds lines independently of the parser: space and tab
// continuations, and quoted-printable soft breaks. It drops blank lines, a
// byte-order mark and double quotes, which Write adds or removes around
// parameter values as needed. Within each component it moves properties
// ahead of subcomponents, the order the RFC grammars require and Write uses.
func normalise(b []byte) []string {
	if len(b) >= 2 && isUTF16(b[:2]) {
		b = []byte(decodeUTF16(b))
	}
	s := strings.TrimPrefix(string(b), "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var lines []string
	for raw := range strings.SplitSeq(s, "\n") {
		raw = strings.TrimRight(raw, "\r")
		n := len(lines)
		switch {
		case n > 0 && strings.HasSuffix(lines[n-1], "=") && isQPLine(lines[n-1]):
			lines[n-1] = lines[n-1][:len(lines[n-1])-1] + raw
		case raw == "":
		case n > 0 && (raw[0] == ' ' || raw[0] == '\t'):
			lines[n-1] += raw[1:]
		default:
			lines = append(lines, raw)
		}
	}
	for i := range lines {
		lines[i] = strings.ReplaceAll(lines[i], `= "`, `="`)
		lines[i] = strings.ReplaceAll(lines[i], `"`, "")
		// RFC 6868: "^^" before anything but n, ^ or ' is the same as "^",
		// and Write emits the shorter form.
		lines[i] = redundantCaret.ReplaceAllString(lines[i], "^$1")
	}
	lines = trailingIntoLast(lines)
	out, _ := propsFirst(lines)
	return out
}

// propsFirst reorders one level of BEGIN/END nesting, returning the
// reordered lines and how many input lines it consumed.
func propsFirst(lines []string) ([]string, int) {
	var props, subs []string
	for i := 0; i < len(lines); i++ {
		upper := strings.ToUpper(lines[i])
		switch {
		case strings.HasPrefix(upper, "BEGIN:"):
			inner, n := propsFirst(lines[i+1:])
			subs = append(subs, lines[i])
			subs = append(subs, inner...)
			i += n
		case strings.HasPrefix(upper, "END:"):
			return append(append(props, subs...), lines[i]), i + 1
		default:
			props = append(props, lines[i])
		}
	}
	return append(props, subs...), len(lines)
}

var redundantCaret = regexp.MustCompile(`\^\^([^nN^'])`)

// trailingIntoLast moves properties that follow the last top-level END inside
// that component, where Parse puts them.
func trailingIntoLast(lines []string) []string {
	lastEnd := -1
	depth := 0
	for i, l := range lines {
		upper := strings.ToUpper(l)
		switch {
		case strings.HasPrefix(upper, "BEGIN:"):
			depth++
		case strings.HasPrefix(upper, "END:"):
			depth--
			if depth == 0 {
				lastEnd = i
			}
		}
	}
	if lastEnd < 0 || lastEnd == len(lines)-1 || depth != 0 {
		return lines
	}
	out := append([]string{}, lines[:lastEnd]...)
	out = append(out, lines[lastEnd+1:]...)
	return append(out, lines[lastEnd])
}

func isQPLine(line string) bool {
	prefix, _, _ := strings.Cut(line, ":")
	return strings.Contains(strings.ToUpper(prefix), "QUOTED-PRINTABLE")
}

func firstDiff(want, got []string) string {
	for i := range min(len(want), len(got)) {
		if want[i] != got[i] {
			return "line " + strconv.Itoa(i+1) + ":\n want: " + want[i] + "\n  got: " + got[i]
		}
	}
	return "line count: want " + strconv.Itoa(len(want)) + ", got " + strconv.Itoa(len(got))
}

// TestFixturesInvalid checks that files under an "invalid" directory are
// rejected rather than silently misread.
func TestFixturesInvalid(t *testing.T) {
	for _, root := range fixtureRoots {
		matches, err := filepath.Glob(filepath.Join(root, "*", "invalid", "*", "*"))
		require.NoError(t, err)
		more, err := filepath.Glob(filepath.Join(root, "invalid", "*"))
		require.NoError(t, err)
		for _, path := range append(matches, more...) {
			t.Run(filepath.ToSlash(path), func(t *testing.T) {
				in, err := os.ReadFile(path)
				require.NoError(t, err)
				_, err = Parse(bytes.NewReader(in))
				require.Error(t, err)
			})
		}
	}
}
