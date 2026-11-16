package hls

import (
	"encoding/binary"
	"fmt"
	"io"
)

// box is an ISO BMFF box: where it starts and ends in its file, and where its
// payload starts.
type box struct {
	typ              string
	start, body, end int64
}

// readBoxHeader reads the header of the box at off, in a file (or parent
// box) that ends at limit.
func readBoxHeader(r io.ReaderAt, off, limit int64) (box, error) {
	var h [16]byte
	if limit-off < 8 {
		return box{}, fmt.Errorf("box at %d: %d bytes left, short of a header", off, limit-off)
	}
	if _, err := r.ReadAt(h[:8], off); err != nil {
		return box{}, fmt.Errorf("box at %d: %w", off, err)
	}
	b := box{typ: string(h[4:8]), start: off, body: off + 8}
	size := int64(binary.BigEndian.Uint32(h[:4]))
	switch size {
	case 0:
		b.end = limit
	case 1:
		if limit-off < 16 {
			return box{}, fmt.Errorf("box %q at %d: short of its 64-bit size", b.typ, off)
		}
		if _, err := r.ReadAt(h[8:16], off+8); err != nil {
			return box{}, fmt.Errorf("box %q at %d: %w", b.typ, off, err)
		}
		u := binary.BigEndian.Uint64(h[8:16])
		if u > uint64(limit-off) {
			return box{}, fmt.Errorf("box %q at %d: size %d runs past the end at %d", b.typ, off, u, limit)
		}
		b.body = off + 16
		b.end = off + int64(u)
	default:
		b.end = off + size
	}
	if b.end < b.body || b.end > limit {
		return box{}, fmt.Errorf("box %q at %d: size %d runs past the end at %d", b.typ, off, size, limit)
	}
	return b, nil
}

// children lists the boxes in buf, a box's payload held in memory.
func children(buf []byte) ([]memBox, error) {
	var out []memBox
	for off := 0; off < len(buf); {
		if len(buf)-off < 8 {
			return nil, fmt.Errorf("%d trailing bytes, short of a box header", len(buf)-off)
		}
		size := int(binary.BigEndian.Uint32(buf[off:]))
		typ := string(buf[off+4 : off+8])
		hdr := 8
		switch size {
		case 0:
			size = len(buf) - off
		case 1:
			if len(buf)-off < 16 {
				return nil, fmt.Errorf("box %q: short of its 64-bit size", typ)
			}
			u := binary.BigEndian.Uint64(buf[off+8:])
			if u > uint64(len(buf)-off) {
				return nil, fmt.Errorf("box %q: size %d runs past its parent", typ, u)
			}
			size, hdr = int(u), 16
		}
		if size < hdr || size > len(buf)-off {
			return nil, fmt.Errorf("box %q: size %d runs past its parent", typ, size)
		}
		out = append(out, memBox{typ: typ, data: buf[off+hdr : off+size]})
		off += size
	}
	return out, nil
}

// memBox is a box whose payload is in memory.
type memBox struct {
	typ  string
	data []byte
}

// child returns the first child box of buf with type typ.
func child(buf []byte, typ string) (memBox, bool, error) {
	kids, err := children(buf)
	if err != nil {
		return memBox{}, false, err
	}
	for _, k := range kids {
		if k.typ == typ {
			return k, true, nil
		}
	}
	return memBox{}, false, nil
}

// path walks down from buf through the named boxes, each the first of its
// type in its parent.
func path(buf []byte, types ...string) ([]byte, error) {
	for _, typ := range types {
		k, ok, err := child(buf, typ)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("no %s box", typ)
		}
		buf = k.data
	}
	return buf, nil
}
