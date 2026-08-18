package plugin

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
)

// procLogWriter turns a plugin subprocess's output into host log records, one
// per line.
//
// A process plugin used to inherit the host's own stdout and stderr file
// descriptors. Anything it wrote — a panic trace, a log.Fatalf from plugcmd —
// went straight to the host's stderr without passing through slog, so it never
// reached the ring buffer behind the admin log viewer. An operator looking
// there after a crash saw only "plugin started" and no crash at all.
type procLogWriter struct {
	log   *slog.Logger
	level slog.Level

	mu     sync.Mutex
	plugin string
	buf    []byte
}

// procLogMaxLine bounds how much output is buffered while waiting for a
// newline, so a plugin that writes an endless unterminated line cannot grow the
// host's memory without limit.
const procLogMaxLine = 64 << 10

func newProcLogWriter(log *slog.Logger, level slog.Level, plugin string) *procLogWriter {
	return &procLogWriter{log: log, level: level, plugin: plugin}
}

func (w *procLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) >= procLogMaxLine {
		w.emit(w.buf)
		w.buf = nil
	}
	return len(p), nil
}

// Flush emits whatever the process wrote without a trailing newline. A crashing
// process often ends mid-line, and that last line is the interesting one.
func (w *procLogWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(w.buf)
		w.buf = nil
	}
}

// setPlugin renames the log source once GetID has reported the real plugin ID.
// Until then the binary's file name is the best label available.
func (w *procLogWriter) setPlugin(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.plugin = id
}

// emit logs one line. The "plugin" attribute is what the log buffer's tee
// handler reads to attribute the entry to a source.
func (w *procLogWriter) emit(line []byte) {
	line = bytes.TrimRight(line, "\r")
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	w.log.Log(context.Background(), w.level, string(line), "plugin", w.plugin)
}
