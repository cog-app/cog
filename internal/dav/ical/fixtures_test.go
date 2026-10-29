package ical

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// fixtureRoots holds the committed corpus, plus local-only directories that
// are checked when present (see testdata/dav/README.md).
var fixtureRoots = []string{
	"../../../testdata/dav/ics",
	"../../../tmp/dav-export",
	"../../../tmp/dav-fixtures-gpl",
}

// resources splits every fixture into resources the way a client would PUT
// them: the components of one kind and UID with the file's VTIMEZONEs.
func resources(t *testing.T, kind Kind) map[string]*contentline.Component {
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
				return nil // contentline's own tests cover unreadable files
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
					out[filepath.ToSlash(path)+"#"+itoa(ci)+"/"+itoa(i)] = r
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
	return out
}

func itoa(i int) string { return strconv.Itoa(i) }

func encode(t *testing.T, o *Object) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, contentline.Write(&b, []*contentline.Component{o.Encode(o.IANAZones)}))
	return b.Bytes()
}

// TestFixturesRoundTrip checks, for every event, task and journal resource
// in the corpus, that decoding what Cog writes gives back exactly what it
// decoded, that writing is deterministic, and that no property name the
// client sent is missing from the output.
func TestFixturesRoundTrip(t *testing.T) {
	for _, k := range []struct {
		kind Kind
		min  int // the committed corpus alone has at least this many
	}{{VEVENT, 500}, {VTODO, 20}, {VJOURNAL, 5}} {
		t.Run(string(k.kind), func(t *testing.T) {
			res := resources(t, k.kind)
			if len(res) < k.min {
				t.Fatalf("only %d %s resources found in the corpus", len(res), k.kind)
			}
			for name, cal := range res {
				t.Run(name, func(t *testing.T) { roundTrip(t, cal, k.kind) })
			}
		})
	}
}

func roundTrip(t *testing.T, cal *contentline.Component, kind Kind) {
	o, err := Decode(cal, kind)
	require.NoError(t, err)
	out := encode(t, o)
	require.Equal(t, string(out), string(encode(t, o)), "encoding is not deterministic")

	comps, err := contentline.Parse(bytes.NewReader(out))
	require.NoError(t, err)
	again, err := Decode(comps[0], kind)
	require.NoError(t, err)
	if !hasProp(o.Props, "PRODID") {
		o.Props = append([]contentline.Property{{Name: "PRODID", Value: ProdID}}, o.Props...)
	}
	if !reflect.DeepEqual(o, again) {
		t.Fatalf("decode(encode(x)) != x\nencoded:\n%s", out)
	}
	require.Equal(t, string(out), string(encode(t, again)), "not a fixed point")

	// Every property name a client sent survives somewhere in its
	// component.
	n := len(o.Components)
	for i, ev := range cal.Components[len(cal.Components)-n:] {
		missing(t, ev, comps[0].Components[len(comps[0].Components)-n+i], out)
	}
}

func hasProp(props []contentline.Property, name string) bool {
	for _, p := range props {
		if p.Is(name) {
			return true
		}
	}
	return false
}

func missing(t *testing.T, in, out *contentline.Component, encoded []byte) {
	t.Helper()
	have := map[string]int{}
	for _, p := range out.Props {
		have[strings.ToUpper(p.Group+"."+p.Name)]++
	}
	for _, p := range in.Props {
		if have[strings.ToUpper(p.Group+"."+p.Name)] == 0 {
			t.Errorf("%s: %s lost\n%s", in.Name, p.Name, encoded)
		}
	}
	if len(in.Components) != len(out.Components) {
		t.Errorf("%s: %d subcomponents in, %d out", in.Name, len(in.Components), len(out.Components))
		return
	}
	for i := range in.Components {
		missing(t, in.Components[i], out.Components[i], encoded)
	}
}
