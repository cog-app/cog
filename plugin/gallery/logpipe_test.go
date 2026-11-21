package gallery

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
)

// The worker's records reach the server's log with every attribute, not
// just their message.
func TestLogPipeForwardsAttributes(t *testing.T) {
	var out bytes.Buffer
	lp := &logPipe{logger: slog.New(slog.NewTextHandler(&out, nil))}
	lp.Write([]byte(`{"time":"2026-11-21T04:56:24Z","level":"WARN","msg":"extract metadata","plugin":"gallery","path":"Photos/a.jpg","err":"boom","n":3}` + "\n"))
	lp.Write([]byte("not json\n"))

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	assert.Equal(t, 2, len(lines))
	assert.Contains(t, lines[0], `level=WARN msg="extract metadata" source=scan-worker path=Photos/a.jpg err=boom n=3`)
	assert.NotContains(t, lines[0], "plugin=")
	assert.Contains(t, lines[1], `msg="not json" source=scan-worker`)
}
