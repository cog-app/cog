package logbuf

import (
	"bytes"
	"context"
	"log/slog"
)

// TeeHandler is an slog.Handler that delegates to an inner handler and
// simultaneously captures each record into a ring buffer.
type TeeHandler struct {
	inner slog.Handler
	buf   *Buffer
	// attrs and group state for formatting
	attrs  []slog.Attr
	group  string
	source string // cached "plugin" attr value from WithAttrs
}

// NewTeeHandler wraps inner so that every handled record is also captured in buf.
func NewTeeHandler(inner slog.Handler, buf *Buffer) *TeeHandler {
	return &TeeHandler{inner: inner, buf: buf}
}

func (h *TeeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *TeeHandler) Handle(ctx context.Context, r slog.Record) error {
	// Delegate to the real handler first.
	err := h.inner.Handle(ctx, r)

	// Format the record via a throwaway TextHandler for the ring buffer.
	var b bytes.Buffer
	fmt := slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})
	// Replay attrs/group so the formatted line includes them.
	var fh slog.Handler = fmt
	if h.group != "" {
		fh = fh.WithGroup(h.group)
	}
	if len(h.attrs) > 0 {
		fh = fh.WithAttrs(h.attrs)
	}
	_ = fh.Handle(ctx, r)

	// Extract source from pre-attached attrs or record attrs.
	source := h.source
	if source == "" {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "plugin" || a.Key == "source" {
				source = a.Value.String()
				return false
			}
			return true
		})
	}

	h.buf.Add(Entry{
		Time:    r.Time,
		Level:   r.Level,
		Source:  source,
		Message: b.String(),
	})
	return err
}

func (h *TeeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	source := h.source
	for _, a := range attrs {
		if a.Key == "plugin" || a.Key == "source" {
			source = a.Value.String()
		}
	}
	return &TeeHandler{
		inner:  h.inner.WithAttrs(attrs),
		buf:    h.buf,
		attrs:  append(h.attrs[:len(h.attrs):len(h.attrs)], attrs...),
		group:  h.group,
		source: source,
	}
}

func (h *TeeHandler) WithGroup(name string) slog.Handler {
	return &TeeHandler{
		inner:  h.inner.WithGroup(name),
		buf:    h.buf,
		attrs:  h.attrs,
		group:  name,
		source: h.source,
	}
}
