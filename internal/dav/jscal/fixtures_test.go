package jscal

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// fixtureRoots holds the committed corpus, plus local-only directories that
// are checked when present (see testdata/dav/README.md).
var fixtureRoots = []string{
	"../../../testdata/dav/ics",
	"../../../tmp/dav-export",
	"../../../tmp/dav-fixtures-gpl",
}

// resources splits every fixture into resources of one kind, as a client
// would PUT them.
func resources(t *testing.T, kind ical.Kind) map[string]*contentline.Component {
	t.Helper()
	out := map[string]*contentline.Component{}
	for _, root := range fixtureRoots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if ext := strings.ToLower(filepath.Ext(path)); (ext != ".ics" && ext != ".ical") || strings.Contains(filepath.ToSlash(path), "/invalid/") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			comps, err := contentline.Parse(bytes.NewReader(data))
			if err != nil {
				return nil
			}
			for ci, cal := range comps {
				if !cal.Is("VCALENDAR") {
					continue
				}
				var zones []*contentline.Component
				byUID := map[string]*contentline.Component{}
				var order []string
				for _, sub := range cal.Components {
					switch {
					case sub.Is("VTIMEZONE"):
						zones = append(zones, sub)
					case sub.Is(string(kind)):
						uid := ""
						if p := sub.Prop("UID"); p != nil {
							uid = p.Value
						}
						r, ok := byUID[uid]
						if !ok {
							r = &contentline.Component{Name: "VCALENDAR", Props: cal.Props}
							byUID[uid] = r
							order = append(order, uid)
						}
						r.Components = append(r.Components, sub)
					}
				}
				for i, uid := range order {
					r := byUID[uid]
					r.Components = append(append([]*contentline.Component{}, zones...), r.Components...)
					out[filepath.ToSlash(path)+"#"+strconv.Itoa(ci)+"/"+strconv.Itoa(i)] = r
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
	return out
}

// TestFixturesRoundTrip checks, for every event, task and journal resource
// in the corpus, that the JSCalendar Cog writes reads back as exactly the
// model it was written from, through JSON text, and that writing it is
// deterministic.
func TestFixturesRoundTrip(t *testing.T) {
	for _, k := range []struct {
		kind ical.Kind
		min  int
	}{{ical.VEVENT, 500}, {ical.VTODO, 20}, {ical.VJOURNAL, 5}} {
		t.Run(string(k.kind), func(t *testing.T) {
			res := resources(t, k.kind)
			if len(res) < k.min {
				t.Fatalf("only %d %s resources found", len(res), k.kind)
			}
			for name, cal := range res {
				t.Run(name, func(t *testing.T) { roundTrip(t, cal, k.kind) })
			}
		})
	}
}

func roundTrip(t *testing.T, cal *contentline.Component, kind ical.Kind) {
	o, err := ical.Decode(cal, kind)
	if err != nil {
		t.Skip("not readable as", kind)
	}
	if o.UID == "" {
		t.Skip("no UID")
	}
	// Stores keep the master first (icalsql.Store), which is the order
	// JSCalendar has.
	o.SortMaster()
	js, err := Encode(o)
	require.NoError(t, err)
	text, err := json.Marshal(js)
	require.NoError(t, err)
	again, err := Encode(o)
	require.NoError(t, err)
	text2, err := json.Marshal(again)
	require.NoError(t, err)
	require.Equal(t, string(text), string(text2), "encoding is not deterministic")

	parsed, err := jsobj.ParseObject(text)
	require.NoError(t, err)
	back, err := Decode(parsed)
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, indent(js))
	}
	want, got := modelJSON(t, o), modelJSON(t, back)
	if want != got {
		t.Fatalf("decode(encode(x)) != x\njson:\n%s\ndiff:\n%s", indent(js), lineDiff(want, got))
	}
}

func modelJSON(t *testing.T, o *ical.Object) string {
	t.Helper()
	// Only the zones the components use are written.
	b, err := json.MarshalIndent(o, "", " ")
	require.NoError(t, err)
	return string(b)
}

func indent(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// lineDiff shows the lines of two texts from the first that differs.
func lineDiff(a, b string) string {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	i := 0
	for i < len(x) && i < len(y) && x[i] == y[i] {
		i++
	}
	lo := max(0, i-8)
	var out strings.Builder
	for j := lo; j < min(len(x), i+12); j++ {
		out.WriteString("- " + x[j] + "\n")
	}
	for j := lo; j < min(len(y), i+12); j++ {
		out.WriteString("+ " + y[j] + "\n")
	}
	return out.String()
}
