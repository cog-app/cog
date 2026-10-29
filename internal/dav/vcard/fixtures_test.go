package vcard

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
	"../../../testdata/dav/vcf",
	"../../../tmp/dav-export",
	"../../../tmp/dav-fixtures-gpl",
}

// Fixtures returns every VCARD in the corpus, by file and position.
func fixtures(t *testing.T) map[string]*contentline.Component {
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
			if ext := strings.ToLower(filepath.Ext(path)); (ext != ".vcf" && ext != ".vcard") || strings.Contains(filepath.ToSlash(path), "/invalid/") {
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
			for i, c := range comps {
				if c.Is("VCARD") {
					out[filepath.ToSlash(path)+"#"+strconv.Itoa(i)] = c
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
	return out
}

func write(t *testing.T, c *Card) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, contentline.Write(&b, []*contentline.Component{c.Encode()}))
	return b.Bytes()
}

// TestFixturesRoundTrip checks, for every card in the corpus, that decoding
// what Cog writes gives back exactly what it decoded, that writing is
// deterministic and a fixed point, that the version is kept, and that no
// property name or group a client sent is missing from the output.
func TestFixturesRoundTrip(t *testing.T) {
	cards := fixtures(t)
	if len(cards) < 100 {
		t.Fatalf("only %d cards found in the corpus", len(cards))
	}
	for name, in := range cards {
		t.Run(name, func(t *testing.T) {
			c := Decode(in)
			out := write(t, c)
			require.Equal(t, string(out), string(write(t, c)), "encoding is not deterministic")

			comps, err := contentline.Parse(bytes.NewReader(out))
			require.NoError(t, err)
			require.Equal(t, 1, len(comps))
			again := Decode(comps[0])
			if !reflect.DeepEqual(c, again) {
				t.Fatalf("decode(encode(x)) != x\nencoded:\n%s", out)
			}
			require.Equal(t, string(out), string(write(t, again)), "not a fixed point")

			if v := in.Prop("VERSION"); v != nil && v.Group == "" {
				require.Equal(t, strings.TrimSpace(v.Value), again.Version)
			}
			have := map[string]int{}
			for _, p := range comps[0].Props {
				have[strings.ToUpper(p.Group+"."+p.Name)]++
			}
			for _, p := range in.Props {
				if have[strings.ToUpper(p.Group+"."+p.Name)] == 0 {
					t.Errorf("%s.%s lost\n%s", p.Group, p.Name, out)
				}
			}
			require.Equal(t, len(in.Components), len(comps[0].Components))
		})
	}
}

// TestFixturesModelled checks that the corpus exercises what the codec
// models, so a change that quietly stops claiming a property shows up as a
// count dropping rather than as leftovers that still round-trip. It counts
// the committed corpus only, so the result is the same with or without the
// local-only directories (CI has none of them).
func TestFixturesModelled(t *testing.T) {
	n := map[string]int{}
	for key, in := range fixtures(t) {
		if !strings.HasPrefix(key, fixtureRoots[0]+"/") {
			continue
		}
		c := Decode(in)
		count := func(key string, ok bool) {
			if ok {
				n[key]++
			}
		}
		count("fn", c.FN != "")
		count("n", c.N != nil)
		count("org", c.Org != nil)
		count("email", len(c.Emails) > 0)
		count("tel", len(c.Phones) > 0)
		count("adr", len(c.Addresses) > 0)
		count("photo", c.Photo != nil)
		count("member", len(c.Members) > 0)
		count("label", labelled(c))
		count("v21", c.Version == V21)
		count("v40", c.Version == V40)
	}
	for key, min := range map[string]int{
		"fn": 75, "n": 70, "org": 40, "email": 50, "tel": 60, "adr": 40,
		"photo": 20, "member": 3, "label": 10, "v21": 35, "v40": 8,
	} {
		if n[key] < min {
			t.Errorf("%s: %d cards, want at least %d", key, n[key], min)
		}
	}
	t.Logf("%v", n)
}

func labelled(c *Card) bool {
	for _, m := range c.children() {
		if m.Label != "" {
			return true
		}
	}
	return false
}
