package jscontact

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
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

var fixtureRoots = []string{
	"../../../testdata/dav/vcf",
	"../../../tmp/dav-export",
	"../../../tmp/dav-fixtures-gpl",
}

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
				return nil
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

// TestFixturesRoundTrip checks, for every card in the corpus, that the
// JSContact Cog writes reads back, through JSON text, as exactly the model
// it was written from, and that writing it is deterministic.
func TestFixturesRoundTrip(t *testing.T) {
	cards := fixtures(t)
	if len(cards) < 100 {
		t.Fatalf("only %d cards found", len(cards))
	}
	for name, in := range cards {
		t.Run(name, func(t *testing.T) {
			c := vcard.Decode(in)
			if c.UID == "" {
				// The store gives every card a UID.
				c.UID = "uid-" + name
			}
			js, err := Encode(c)
			require.NoError(t, err)
			text, err := json.Marshal(js)
			require.NoError(t, err)
			js2, err := Encode(c)
			require.NoError(t, err)
			text2, err := json.Marshal(js2)
			require.NoError(t, err)
			require.Equal(t, string(text), string(text2), "encoding is not deterministic")
			parsed, err := jsobj.ParseObject(text)
			require.NoError(t, err)
			back, err := Decode(parsed)
			if err != nil {
				t.Fatalf("decode: %v\n%s", err, pretty(js))
			}
			if c.Version == "" {
				// A card without VERSION reads as the default.
				c.Version = DefaultVersion
			}
			if want, got := pretty(c), pretty(back); want != got {
				t.Fatalf("decode(encode(x)) != x\njson:\n%s\ndiff:\n%s", pretty(js), lineDiff(want, got))
			}
		})
	}
}

func pretty(v any) string {
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
