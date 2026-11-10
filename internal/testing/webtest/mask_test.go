package webtest

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The screenshot shows what the golden says: a temp directory or a port on
// the page is masked while the picture is taken, and back afterwards, so the
// test goes on with the real values.
func TestScreenshotMasksVolatileValues(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "TestSomething123/001")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body><p id="dir">Data in %s</p><p id="url">http://127.0.0.1:43215/</p>
			<input id="f" value="%s/x"><script>var keep = "127.0.0.1:1";</script></body></html>`, dir, dir)
	})
	eng := New(mux)
	t.Cleanup(eng.Close)
	b := eng.Start(t)
	b.Navigate(t, "/")

	masks := make([][2]string, len(volatileMasks))
	for i, m := range volatileMasks {
		masks[i] = [2]string{m.re.String(), m.with}
	}
	if _, err := b.page.Eval(maskJS, masks); err != nil {
		t.Fatal(err)
	}
	for sel, want := range map[string]string{"#dir": "Data in «tmp»/001", "#url": "http://127.0.0.1:«port»/"} {
		if got := b.Text(t, sel); got != want {
			t.Errorf("masked %s = %q, want %q", sel, got, want)
		}
	}
	if got := b.Value(t, "#f"); got != "«tmp»/001/x" {
		t.Errorf("masked field = %q", got)
	}
	if got := b.Eval(t, "keep"); got != "127.0.0.1:1" {
		t.Errorf("script changed: %v", got)
	}

	if _, err := b.page.Eval(unmaskJS); err != nil {
		t.Fatal(err)
	}
	if got := b.Text(t, "#dir"); got != "Data in "+dir {
		t.Errorf("unmasked text = %q", got)
	}
	if got := b.Value(t, "#f"); got != dir+"/x" {
		t.Errorf("unmasked field = %q", got)
	}

	// And through the screenshot itself.
	if err := b.captureImage(filepath.Join(t.TempDir(), "shot.png")); err != nil {
		t.Fatal(err)
	}
	if got := b.Text(t, "#url"); got != "http://127.0.0.1:43215/" {
		t.Errorf("after screenshot = %q", got)
	}
}
