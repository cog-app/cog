package profanity

import (
	"io"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/plugin"
)

func mustParseWordList(t *testing.T, r io.Reader) *WordList {
	t.Helper()
	wl, err := ParseWordList(r)
	if err != nil {
		t.Fatal(err)
	}
	return wl
}

func TestParseWordList(t *testing.T) {
	input := `
# Comment
damn=darn
hell=heck
shit=crap

ass=butt
`
	wl := mustParseWordList(t, strings.NewReader(input))
	if wl.Empty() {
		t.Fatal("word list should not be empty")
	}
	if len(wl.entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(wl.entries))
	}
}

func TestFilterText(t *testing.T) {
	wl := mustParseWordList(t, strings.NewReader("damn=darn\nhell=heck"))

	got := wl.FilterText("What the damn hell is going on")
	want := "What the [darn] [heck] is going on"
	if got != want {
		t.Errorf("FilterText = %q, want %q", got, want)
	}
}

func TestFilterTextCaseInsensitive(t *testing.T) {
	wl := mustParseWordList(t, strings.NewReader("damn=darn"))

	got := wl.FilterText("DAMN it")
	want := "[darn] it"
	if got != want {
		t.Errorf("FilterText = %q, want %q", got, want)
	}
}

func TestFilterTextWholeWord(t *testing.T) {
	wl := mustParseWordList(t, strings.NewReader("ass=butt"))

	got := wl.FilterText("The class was assembled")
	// "ass" should not match inside "class" or "assembled"
	if strings.Contains(got, "[butt]") {
		t.Errorf("FilterText matched partial word: %q", got)
	}
}

func TestFilterTextDefaultReplacement(t *testing.T) {
	wl := mustParseWordList(t, strings.NewReader("damn"))

	got := wl.FilterText("damn it")
	want := "[***] it"
	if got != want {
		t.Errorf("FilterText = %q, want %q", got, want)
	}
}

func TestFilterHTML(t *testing.T) {
	wl := mustParseWordList(t, strings.NewReader("damn=darn"))

	got := wl.FilterHTML("<p>What the <b>damn</b> thing</p>")
	if !strings.Contains(got, "[darn]") {
		t.Errorf("FilterHTML should contain [darn]: %q", got)
	}
	if !strings.Contains(got, "<b>") {
		t.Errorf("FilterHTML should preserve tags: %q", got)
	}
}

func TestFilterHTMLPreservesTags(t *testing.T) {
	wl := mustParseWordList(t, strings.NewReader("damn=darn"))

	got := wl.FilterHTML(`<a href="http://example.com/damn">damn link</a>`)
	// Should not filter inside attribute values, only text nodes.
	if !strings.Contains(got, `href="http://example.com/damn"`) {
		t.Errorf("FilterHTML should preserve attribute values: %q", got)
	}
	if !strings.Contains(got, "[darn] link") {
		t.Errorf("FilterHTML should filter text node: %q", got)
	}
}

func TestEmptyWordList(t *testing.T) {
	wl := mustParseWordList(t, strings.NewReader(""))
	if !wl.Empty() {
		t.Fatal("should be empty")
	}
	if got := wl.FilterText("hello"); got != "hello" {
		t.Errorf("FilterText on empty list should be identity: %q", got)
	}
}

func TestAudioRanges(t *testing.T) {
	wl := mustParseWordList(t, strings.NewReader("damn=darn\nhell=heck"))

	captions := []plugin.CaptionSegment{
		{Start: 0, End: 3, Text: "Hello world"},
		{Start: 3, End: 6, Text: "What the damn hell"},
		{Start: 6, End: 9, Text: "is going on"},
	}

	ranges := wl.AudioRanges(captions)
	if len(ranges) != 1 {
		t.Fatalf("expected 1 range, got %d", len(ranges))
	}
	if ranges[0].Start != 3 || ranges[0].End != 6 {
		t.Errorf("range = [%.1f, %.1f], want [3.0, 6.0]", ranges[0].Start, ranges[0].End)
	}
	if ranges[0].ReplacementText != "What the darn heck" {
		t.Errorf("replacement = %q, want %q", ranges[0].ReplacementText, "What the darn heck")
	}
}

func TestParseVTT(t *testing.T) {
	vtt := `WEBVTT

00:00:01.000 --> 00:00:04.000
Hello world

00:00:05.000 --> 00:00:08.000
This is a <c>test</c>
with two lines

`
	segs := ParseVTT(strings.NewReader(vtt))
	if len(segs) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segs))
	}
	if segs[0].Text != "Hello world" {
		t.Errorf("seg[0].Text = %q", segs[0].Text)
	}
	if segs[0].Start != 1.0 || segs[0].End != 4.0 {
		t.Errorf("seg[0] timing = [%.1f, %.1f]", segs[0].Start, segs[0].End)
	}
	if segs[1].Text != "This is a test with two lines" {
		t.Errorf("seg[1].Text = %q", segs[1].Text)
	}
}
