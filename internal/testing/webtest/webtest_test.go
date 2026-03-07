package webtest_test

import (
	"fmt"
	"net/http"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
)

func TestSmoke(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Hello</title></head><body><h1 id="msg">Hi</h1></body></html>`)
	})

	eng := webtest.New(mux)
	t.Cleanup(eng.Close)

	b := eng.StartT(t)
	b.Navigate(t, "/")

	if got := b.Title(t); got != "Hello" {
		t.Fatalf("title = %q, want %q", got, "Hello")
	}
	if got := b.Text(t, "#msg"); got != "Hi" {
		t.Fatalf("text = %q, want %q", got, "Hi")
	}
	if !b.HasElement(t, "h1") {
		t.Fatal("expected h1 element")
	}
	if b.Count(t, "h1") != 1 {
		t.Fatalf("count = %d, want 1", b.Count(t, "h1"))
	}
}
