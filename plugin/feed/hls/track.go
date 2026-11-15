// Package hls reads the fragment index of a fragmented MP4 track and writes
// the HLS playlists that present it.
package hls

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Kind is what a track carries.
type Kind string

// The kinds of track a presentation has.
const (
	Audio Kind = "audio"
	Video Kind = "video"
)

// Fragment is one entry of a track's sidx: a moof and its mdat.
type Fragment struct {
	Start, Duration float64 // seconds
	Offset, Length  int64   // bytes in the track file
	Keyframe        bool    // sidx starts_with_SAP
}

// Track is what a playlist needs of one fragmented MP4 track file.
type Track struct {
	Kind                   Kind
	Codecs                 string // RFC 6381; "" when unknown
	Width, Height          int    // video only
	InitOffset, InitLength int64  // ftyp+moov
	Fragments              []Fragment
}

var (
	// ErrNoIndex is a file that is not a fragmented MP4 with a sidx.
	ErrNoIndex = errors.New("hls: no sidx")
	// ErrMultiTrack is an MP4 that holds more than one track.
	ErrMultiTrack = errors.New("hls: more than one track")
)

// maxHeaderBox bounds the moov and sidx read into memory. A moov of a long
// progressive MP4 runs to a few MB; anything far past that is not a file this
// package can use.
const maxHeaderBox = 64 << 20

// ReadTrack reads the index of a single-track fragmented MP4 of size bytes.
// It reads the boxes up to the end of the sidx, never the media.
func ReadTrack(r io.ReaderAt, size int64) (Track, error) {
	var moov, sidx *box
	for off := int64(0); off < size && (moov == nil || sidx == nil); {
		b, err := readBoxHeader(r, off, size)
		if err != nil {
			if off == 0 {
				return Track{}, fmt.Errorf("%w: not an MP4: %v", ErrNoIndex, err)
			}
			return Track{}, err
		}
		if off == 0 && b.typ != "ftyp" {
			return Track{}, fmt.Errorf("%w: not an MP4 (starts with %q)", ErrNoIndex, b.typ)
		}
		switch b.typ {
		case "moov":
			moov = &b
		case "sidx":
			sidx = &b
		case "moof":
			// A sidx that indexes the fragments comes before them.
			if sidx == nil {
				off = size
				continue
			}
		}
		off = b.end
	}
	if moov == nil {
		return Track{}, errors.New("hls: no moov box")
	}
	moovData, err := readBody(r, *moov)
	if err != nil {
		return Track{}, err
	}
	t, err := parseMoov(moovData)
	if err != nil {
		return Track{}, err
	}
	if sidx == nil {
		return Track{}, ErrNoIndex
	}
	t.InitOffset, t.InitLength = 0, moov.end
	sidxData, err := readBody(r, *sidx)
	if err != nil {
		return Track{}, err
	}
	if t.Fragments, err = parseSidx(sidxData, sidx.end, size); err != nil {
		return Track{}, err
	}
	return t, nil
}

func readBody(r io.ReaderAt, b box) ([]byte, error) {
	n := b.end - b.body
	if n > maxHeaderBox {
		return nil, fmt.Errorf("hls: %s box is %d bytes, over %d", b.typ, n, maxHeaderBox)
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, b.body); err != nil {
		return nil, fmt.Errorf("hls: read %s box: %w", b.typ, err)
	}
	return buf, nil
}

func parseMoov(moov []byte) (Track, error) {
	kids, err := children(moov)
	if err != nil {
		return Track{}, fmt.Errorf("hls: moov: %w", err)
	}
	var trak []byte
	for _, k := range kids {
		if k.typ != "trak" {
			continue
		}
		if trak != nil {
			return Track{}, ErrMultiTrack
		}
		trak = k.data
	}
	if trak == nil {
		return Track{}, errors.New("hls: moov has no trak")
	}
	mdia, err := path(trak, "mdia")
	if err != nil {
		return Track{}, fmt.Errorf("hls: trak: %w", err)
	}
	hdlr, err := path(mdia, "hdlr")
	if err != nil {
		return Track{}, fmt.Errorf("hls: mdia: %w", err)
	}
	if len(hdlr) < 12 {
		return Track{}, errors.New("hls: hdlr box is short")
	}
	var t Track
	switch h := string(hdlr[8:12]); h {
	case "vide":
		t.Kind = Video
	case "soun":
		t.Kind = Audio
	default:
		return Track{}, fmt.Errorf("hls: track handler %q is neither video nor audio", h)
	}
	stsd, err := path(mdia, "minf", "stbl", "stsd")
	if err != nil {
		return Track{}, fmt.Errorf("hls: mdia: %w", err)
	}
	if len(stsd) < 8 {
		return Track{}, errors.New("hls: stsd box is short")
	}
	entries, err := children(stsd[8:])
	if err != nil {
		return Track{}, fmt.Errorf("hls: stsd: %w", err)
	}
	if len(entries) == 0 {
		return Track{}, errors.New("hls: stsd has no sample entry")
	}
	if err := sampleEntry(&t, entries[0]); err != nil {
		return Track{}, fmt.Errorf("hls: %s sample entry: %w", entries[0].typ, err)
	}
	return t, nil
}

// sampleEntry fills in the codec and picture size from the first sample
// entry. A codec it does not know leaves Codecs empty.
func sampleEntry(t *Track, e memBox) error {
	switch e.typ {
	case "avc1", "avc3":
		// SampleEntry (8) + VisualSampleEntry fields up to width (16).
		const fixed = 78
		if len(e.data) < fixed {
			return errors.New("short")
		}
		t.Width = int(binary.BigEndian.Uint16(e.data[24:]))
		t.Height = int(binary.BigEndian.Uint16(e.data[26:]))
		avcC, ok, err := child(e.data[fixed:], "avcC")
		if err != nil {
			return err
		}
		if ok && len(avcC.data) >= 4 {
			t.Codecs = fmt.Sprintf("%s.%02X%02X%02X", e.typ, avcC.data[1], avcC.data[2], avcC.data[3])
		}
	case "mp4a":
		// SampleEntry (8) + AudioSampleEntry (20), longer in QuickTime's
		// versions 1 and 2.
		if len(e.data) < 28 {
			return errors.New("short")
		}
		fixed := 28
		switch binary.BigEndian.Uint16(e.data[8:]) {
		case 1:
			fixed += 16
		case 2:
			fixed += 36
		}
		if len(e.data) < fixed {
			return errors.New("short")
		}
		esds, ok, err := child(e.data[fixed:], "esds")
		if err != nil {
			return err
		}
		if ok {
			codecs, err := esdsCodecs(esds.data)
			if err != nil {
				return fmt.Errorf("esds: %w", err)
			}
			t.Codecs = codecs
		}
	}
	return nil
}

// esdsCodecs names the codec an esds box describes: mp4a.40.<audio object
// type> for AAC, mp4a.<object type> otherwise (ISO 14496-1 and RFC 6381).
func esdsCodecs(b []byte) (string, error) {
	if len(b) < 4 {
		return "", errors.New("short")
	}
	d := descReader{b: b[4:]} // version and flags
	if err := d.enter(0x03); err != nil {
		return "", err
	}
	es, err := d.take(3)
	if err != nil {
		return "", err
	}
	flags := es[2]
	if flags&0x80 != 0 { // streamDependenceFlag
		if _, err := d.take(2); err != nil {
			return "", err
		}
	}
	if flags&0x40 != 0 { // URL_Flag
		n, err := d.take(1)
		if err != nil {
			return "", err
		}
		if _, err := d.take(int(n[0])); err != nil {
			return "", err
		}
	}
	if flags&0x20 != 0 { // OCRstreamFlag
		if _, err := d.take(2); err != nil {
			return "", err
		}
	}
	if err := d.enter(0x04); err != nil {
		return "", err
	}
	dc, err := d.take(13)
	if err != nil {
		return "", err
	}
	oti := dc[0]
	if oti != 0x40 {
		return fmt.Sprintf("mp4a.%02X", oti), nil
	}
	if err := d.enter(0x05); err != nil {
		return "", err
	}
	asc, err := d.take(1)
	if err != nil {
		return "", err
	}
	aot := int(asc[0] >> 3)
	if aot == 31 {
		more, err := d.take(1)
		if err != nil {
			return "", err
		}
		aot = 32 + int(asc[0]&0x07)<<3 | int(more[0]>>5)
	}
	return fmt.Sprintf("mp4a.40.%d", aot), nil
}

// descReader reads the nested descriptors of an esds box.
type descReader struct{ b []byte }

func (d *descReader) take(n int) ([]byte, error) {
	if len(d.b) < n {
		return nil, errors.New("short")
	}
	out := d.b[:n]
	d.b = d.b[n:]
	return out, nil
}

// enter reads a descriptor's tag and length, leaving the reader at its
// payload, which is limited to that length.
func (d *descReader) enter(tag byte) error {
	t, err := d.take(1)
	if err != nil {
		return err
	}
	if t[0] != tag {
		return fmt.Errorf("descriptor tag %#x, want %#x", t[0], tag)
	}
	n := 0
	for i := 0; ; i++ {
		c, err := d.take(1)
		if err != nil {
			return err
		}
		n = n<<7 | int(c[0]&0x7f)
		if c[0]&0x80 == 0 {
			break
		}
		if i == 3 {
			return errors.New("descriptor length over 4 bytes")
		}
	}
	if n > len(d.b) {
		return fmt.Errorf("descriptor %#x of %d bytes runs past its parent", tag, n)
	}
	d.b = d.b[:n]
	return nil
}

// parseSidx reads a sidx box's payload. end is where the box ends in the
// file, which its offsets count from, and size the file's size.
func parseSidx(b []byte, end, size int64) ([]Fragment, error) {
	if len(b) < 12 {
		return nil, errors.New("hls: sidx is short")
	}
	version := b[0]
	timescale := binary.BigEndian.Uint32(b[8:])
	if timescale == 0 {
		return nil, errors.New("hls: sidx timescale is 0")
	}
	b = b[12:]
	var ept, first uint64
	switch version {
	case 0:
		if len(b) < 8 {
			return nil, errors.New("hls: sidx is short")
		}
		ept, first = uint64(binary.BigEndian.Uint32(b)), uint64(binary.BigEndian.Uint32(b[4:]))
		b = b[8:]
	case 1:
		if len(b) < 16 {
			return nil, errors.New("hls: sidx is short")
		}
		ept, first = binary.BigEndian.Uint64(b), binary.BigEndian.Uint64(b[8:])
		b = b[16:]
	default:
		return nil, fmt.Errorf("hls: sidx version %d", version)
	}
	if len(b) < 4 {
		return nil, errors.New("hls: sidx is short")
	}
	count := int(binary.BigEndian.Uint16(b[2:]))
	b = b[4:]
	if len(b) < count*12 {
		return nil, fmt.Errorf("hls: sidx lists %d references in %d bytes", count, len(b))
	}
	if first > uint64(size-end) {
		return nil, fmt.Errorf("hls: sidx first_offset %d runs past the end of the file", first)
	}
	off := end + int64(first)
	t := ept
	frags := make([]Fragment, 0, count)
	for i := range count {
		r := b[i*12:]
		ref := binary.BigEndian.Uint32(r)
		if ref&0x80000000 != 0 {
			return nil, errors.New("hls: sidx references another sidx")
		}
		n := int64(ref & 0x7fffffff)
		dur := uint64(binary.BigEndian.Uint32(r[4:]))
		if off+n > size {
			return nil, fmt.Errorf("hls: sidx reference %d runs past the end of the file", i)
		}
		frags = append(frags, Fragment{
			Start:    float64(t) / float64(timescale),
			Duration: float64(dur) / float64(timescale),
			Offset:   off,
			Length:   n,
			Keyframe: binary.BigEndian.Uint32(r[8:])&0x80000000 != 0,
		})
		off += n
		t += dur
	}
	return frags, nil
}
