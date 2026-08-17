package plugin

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// captureHandler records the records it handles, so a test can assert on the
// message and attributes rather than on formatted text.
type captureHandler struct {
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) messages() []string {
	out := make([]string, len(h.records))
	for i, r := range h.records {
		out[i] = r.Message
	}
	return out
}

func TestProcLogWriter_SplitsLines(t *testing.T) {
	h := &captureHandler{}
	w := newProcLogWriter(slog.New(h), slog.LevelError, "recipes")

	// Output arrives in whatever chunks the pipe delivers, not in whole lines.
	n, err := w.Write([]byte("panic: boom\n\ngoroutine 1 [run"))
	require.NoError(t, err)
	assert.Equal(t, 29, n)
	n, err = w.Write([]byte("ning]:\r\nmain.go:12\n"))
	require.NoError(t, err)
	assert.Equal(t, 19, n)

	// Blank lines are dropped and the \r of a \r\n does not survive.
	assert.Equal(t, 3, len(h.records))
	assert.Equal(t, "panic: boom", h.messages()[0])
	assert.Equal(t, "goroutine 1 [running]:", h.messages()[1])
	assert.Equal(t, "main.go:12", h.messages()[2])

	// The source attribute is what attributes the entry in the log viewer.
	var plugin string
	h.records[0].Attrs(func(a slog.Attr) bool {
		if a.Key == "plugin" {
			plugin = a.Value.String()
		}
		return true
	})
	assert.Equal(t, "recipes", plugin)
	assert.Equal(t, slog.LevelError, h.records[0].Level)
}

// A crashing process often ends mid-line, and that last line is the one worth
// reading, so it must not be lost when the process is reaped.
func TestProcLogWriter_FlushesPartialLine(t *testing.T) {
	h := &captureHandler{}
	w := newProcLogWriter(slog.New(h), slog.LevelError, "recipes")

	_, err := w.Write([]byte("exit status 2, no newline"))
	require.NoError(t, err)
	assert.Equal(t, 0, len(h.records))

	w.Flush()
	require.Equal(t, 1, len(h.records))
	assert.Equal(t, "exit status 2, no newline", h.messages()[0])

	// A second flush has nothing left to say.
	w.Flush()
	assert.Equal(t, 1, len(h.records))
}

// A plugin writing an endless unterminated line must not grow the host's memory
// without limit.
func TestProcLogWriter_BoundsUnterminatedLine(t *testing.T) {
	h := &captureHandler{}
	w := newProcLogWriter(slog.New(h), slog.LevelInfo, "recipes")

	_, err := w.Write([]byte(strings.Repeat("x", procLogMaxLine+10)))
	require.NoError(t, err)
	require.Equal(t, 1, len(h.records))
	assert.Equal(t, procLogMaxLine+10, len(h.messages()[0]))
}

func TestProcLogWriter_SetPlugin(t *testing.T) {
	h := &captureHandler{}
	w := newProcLogWriter(slog.New(h), slog.LevelError, "cog-plugin-recipes")
	w.setPlugin("recipes")

	_, err := w.Write([]byte("hello\n"))
	require.NoError(t, err)
	require.Equal(t, 1, len(h.records))

	var plugin string
	h.records[0].Attrs(func(a slog.Attr) bool {
		if a.Key == "plugin" {
			plugin = a.Value.String()
		}
		return true
	})
	assert.Equal(t, "recipes", plugin)
}
