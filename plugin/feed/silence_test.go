package feed

import (
	"testing"
)

func TestParseSilenceDetect(t *testing.T) {
	stderr := `
[silencedetect @ 0x55] silence_start: 1.234
[silencedetect @ 0x55] silence_end: 2.567 | silence_duration: 1.333
size=N/A time=00:00:05.00 bitrate=N/A
[silencedetect @ 0x55] silence_start: 10.0
[silencedetect @ 0x55] silence_end: 11.5 | silence_duration: 1.5
`
	got := parseSilenceDetect(stderr)
	want := []Range{
		{Start: 1.234, End: 2.567, Action: "skip"},
		{Start: 10.0, End: 11.5, Action: "skip"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d ranges, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("range %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseSilenceDetect_DanglingStart(t *testing.T) {
	stderr := "[silencedetect @ 0x1] silence_start: 4.0\n"
	got := parseSilenceDetect(stderr)
	if len(got) != 0 {
		t.Errorf("dangling start should yield 0 ranges, got %+v", got)
	}
}

func TestParseSilenceDetect_Empty(t *testing.T) {
	if got := parseSilenceDetect(""); len(got) != 0 {
		t.Errorf("empty input should yield 0 ranges, got %+v", got)
	}
}
