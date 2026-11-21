package hls

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// ParamSets are the H.264 sequence and picture parameter sets (NAL units,
// without start codes) an avcC box carries.
type ParamSets struct {
	SPS, PPS [][]byte
}

// InitParamSets reads the parameter sets of the H.264 track whose init
// section (ftyp and moov, as Track.InitOffset and InitLength give it) is
// init.
func InitParamSets(init []byte) (ParamSets, error) {
	moov, err := topBox(init, "moov")
	if err != nil {
		return ParamSets{}, err
	}
	var sets ParamSets
	_, err = rewriteAVCC(moov, func(avcC []byte) ([]byte, error) {
		c, err := parseAVCC(avcC)
		sets = ParamSets{SPS: c.sps, PPS: c.pps}
		return avcC, err
	})
	return sets, err
}

// AddParamSets returns file, a single-track fragmented H.264 MP4, with sets
// added to its avcC. A set whose id the avcC already has is left out when it
// is the same, and an error when it differs.
//
// A player that keeps one init for a playlist whose inits differ (ffmpeg's
// HLS demuxer, so mpv) decodes every segment under it: an edge's init that
// also carries its source's sets decodes the source's segments after it.
//
// The moov grows, so everything after it moves. Nothing in a fragmented
// file points at it from the front: a sidx counts from its own end, and a
// moof's data offsets from the moof (default-base-is-moof). The one table of
// absolute offsets, an mfra's tfra, is moved with it. A moov with sample
// offsets of its own (stco, co64) is not fragmented and is refused.
func AddParamSets(file []byte, sets ParamSets) ([]byte, error) {
	var out []byte
	delta := 0
	moovSeen := false
	for off := 0; off < len(file); {
		b, err := boxAt(file, off)
		if err != nil {
			return nil, err
		}
		raw := file[off : off+b.size]
		switch {
		case b.typ == "moov":
			if moovSeen {
				return nil, errors.New("hls: two moov boxes")
			}
			moovSeen = true
			if err := noSampleOffsets(raw[b.hdr:]); err != nil {
				return nil, err
			}
			body, err := rewriteAVCC(raw[b.hdr:], func(avcC []byte) ([]byte, error) {
				c, err := parseAVCC(avcC)
				if err != nil {
					return nil, err
				}
				if err := c.add(sets); err != nil {
					return nil, err
				}
				return c.bytes()
			})
			if err != nil {
				return nil, err
			}
			moov := boxBytes("moov", body)
			delta = len(moov) - len(raw)
			out = append(out, moov...)
		case b.typ == "mfra" && delta != 0:
			body, err := shiftTfra(raw[b.hdr:], delta)
			if err != nil {
				return nil, err
			}
			out = append(out, boxBytes("mfra", body)...)
		default:
			out = append(out, raw...)
		}
		off += b.size
	}
	if !moovSeen {
		return nil, errors.New("hls: no moov box")
	}
	return out, nil
}

// fileBox is a box found in a byte slice.
type fileBox struct {
	typ       string
	hdr, size int
}

// boxAt reads the header of the box at off in buf. A 64-bit size is read; a
// size of 0 runs to the end.
func boxAt(buf []byte, off int) (fileBox, error) {
	if len(buf)-off < 8 {
		return fileBox{}, fmt.Errorf("hls: %d trailing bytes, short of a box header", len(buf)-off)
	}
	b := fileBox{typ: string(buf[off+4 : off+8]), hdr: 8, size: int(binary.BigEndian.Uint32(buf[off:]))}
	switch b.size {
	case 0:
		b.size = len(buf) - off
	case 1:
		if len(buf)-off < 16 {
			return fileBox{}, fmt.Errorf("hls: box %q: short of its 64-bit size", b.typ)
		}
		u := binary.BigEndian.Uint64(buf[off+8:])
		if u > uint64(len(buf)-off) {
			return fileBox{}, fmt.Errorf("hls: box %q: size %d runs past the end", b.typ, u)
		}
		b.size, b.hdr = int(u), 16
	}
	if b.size < b.hdr || b.size > len(buf)-off {
		return fileBox{}, fmt.Errorf("hls: box %q: size %d runs past the end", b.typ, b.size)
	}
	return b, nil
}

// topBox is the payload of the first box of type typ in buf.
func topBox(buf []byte, typ string) ([]byte, error) {
	for off := 0; off < len(buf); {
		b, err := boxAt(buf, off)
		if err != nil {
			return nil, err
		}
		if b.typ == typ {
			return buf[off+b.hdr : off+b.size], nil
		}
		off += b.size
	}
	return nil, fmt.Errorf("hls: no %s box", typ)
}

// boxBytes is a box of type typ around body, with a 32-bit size.
func boxBytes(typ string, body []byte) []byte {
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:], typ)
	return append(out, body...)
}

// avcPath is where a moov keeps the avcC, and how many bytes of fields come
// before each box's children: stsd's version, flags and entry count, and a
// VisualSampleEntry's fixed fields.
var avcPath = []struct {
	typ    string
	prefix int
}{{"trak", 0}, {"mdia", 0}, {"minf", 0}, {"stbl", 0}, {"stsd", 8}, {"avc1", 78}}

// rewriteAVCC returns moov's payload with fn applied to the payload of the
// avcC of its first track's first sample entry, and every enclosing box
// resized.
func rewriteAVCC(moov []byte, fn func(avcC []byte) ([]byte, error)) ([]byte, error) {
	var walk func(buf []byte, depth int) ([]byte, error)
	walk = func(buf []byte, depth int) ([]byte, error) {
		want, prefix := "avcC", 0
		if depth < len(avcPath) {
			want = avcPath[depth].typ
		}
		var out []byte
		found := false
		for off := 0; off < len(buf); {
			b, err := boxAt(buf, off)
			if err != nil {
				return nil, err
			}
			raw := buf[off : off+b.size]
			if found || b.typ != want {
				out = append(out, raw...)
				off += b.size
				continue
			}
			found = true
			body := raw[b.hdr:]
			var nb []byte
			if depth == len(avcPath) {
				nb, err = fn(body)
			} else {
				prefix = avcPath[depth].prefix
				if len(body) < prefix {
					return nil, fmt.Errorf("hls: %s box is short", want)
				}
				var kids []byte
				kids, err = walk(body[prefix:], depth+1)
				nb = append(append([]byte{}, body[:prefix]...), kids...)
			}
			if err != nil {
				return nil, err
			}
			out = append(out, boxBytes(b.typ, nb)...)
			off += b.size
		}
		if !found {
			return nil, fmt.Errorf("hls: no %s box (not an avc1 track)", want)
		}
		return out, nil
	}
	return walk(moov, 0)
}

// noSampleOffsets reports an error when moov's first track has stco or co64
// entries: offsets into the file that a bigger moov would break.
func noSampleOffsets(moov []byte) error {
	stbl, err := path(moov, "trak", "mdia", "minf", "stbl")
	if err != nil {
		return fmt.Errorf("hls: moov: %w", err)
	}
	for _, typ := range []string{"stco", "co64"} {
		b, ok, err := child(stbl, typ)
		if err != nil {
			return fmt.Errorf("hls: stbl: %w", err)
		}
		if ok && (len(b.data) < 8 || binary.BigEndian.Uint32(b.data[4:]) != 0) {
			return fmt.Errorf("hls: %s has sample offsets: not a fragmented track", typ)
		}
	}
	return nil
}

// shiftTfra returns an mfra's payload with the moof offsets of its tfra
// boxes moved by delta.
func shiftTfra(mfra []byte, delta int) ([]byte, error) {
	out := append([]byte{}, mfra...)
	for off := 0; off < len(out); {
		b, err := boxAt(out, off)
		if err != nil {
			return nil, fmt.Errorf("hls: mfra: %w", err)
		}
		if b.typ == "tfra" {
			if err := shiftTfraEntries(out[off+b.hdr:off+b.size], delta); err != nil {
				return nil, err
			}
		}
		off += b.size
	}
	return out, nil
}

func shiftTfraEntries(t []byte, delta int) error {
	if len(t) < 16 {
		return errors.New("hls: tfra is short")
	}
	wide := t[0] == 1
	sizes := binary.BigEndian.Uint32(t[8:])
	trailing := int((sizes>>4)&3+1) + int((sizes>>2)&3+1) + int(sizes&3+1)
	n := int(binary.BigEndian.Uint32(t[12:]))
	field := 4
	if wide {
		field = 8
	}
	entry := 2*field + trailing
	if n < 0 || len(t)-16 < n*entry {
		return errors.New("hls: tfra entries run past the box")
	}
	for i := range n {
		at := 16 + i*entry + field // past the time, at moof_offset
		if wide {
			binary.BigEndian.PutUint64(t[at:], uint64(int64(binary.BigEndian.Uint64(t[at:]))+int64(delta)))
		} else {
			v := int64(binary.BigEndian.Uint32(t[at:])) + int64(delta)
			if v < 0 || v > 1<<32-1 {
				return errors.New("hls: tfra offset out of range")
			}
			binary.BigEndian.PutUint32(t[at:], uint32(v))
		}
	}
	return nil
}

// avcConfig is a parsed AVCDecoderConfigurationRecord.
type avcConfig struct {
	head     [5]byte // version, profile, compatibility, level, NAL length size
	sps, pps [][]byte
	rest     []byte // the High profiles' chroma and bit depth fields
}

func parseAVCC(b []byte) (avcConfig, error) {
	var c avcConfig
	if len(b) < 6 || b[0] != 1 {
		return c, errors.New("hls: avcC is short or not version 1")
	}
	copy(c.head[:], b)
	at := 5
	read := func(n int) ([][]byte, error) {
		var out [][]byte
		for range n {
			if len(b)-at < 2 {
				return nil, errors.New("hls: avcC parameter set runs past the box")
			}
			l := int(binary.BigEndian.Uint16(b[at:]))
			if len(b)-at-2 < l {
				return nil, errors.New("hls: avcC parameter set runs past the box")
			}
			out = append(out, append([]byte{}, b[at+2:at+2+l]...))
			at += 2 + l
		}
		return out, nil
	}
	var err error
	nsps := int(b[at] & 0x1f)
	at++
	if c.sps, err = read(nsps); err != nil {
		return c, err
	}
	if at >= len(b) {
		return c, errors.New("hls: avcC has no PPS count")
	}
	npps := int(b[at])
	at++
	if c.pps, err = read(npps); err != nil {
		return c, err
	}
	c.rest = append([]byte{}, b[at:]...)
	return c, nil
}

func (c avcConfig) bytes() ([]byte, error) {
	if len(c.sps) > 31 || len(c.pps) > 255 {
		return nil, errors.New("hls: too many parameter sets for an avcC")
	}
	out := append([]byte{}, c.head[:]...)
	out = append(out, 0xe0|byte(len(c.sps)))
	put := func(sets [][]byte) error {
		for _, s := range sets {
			if len(s) > 0xffff {
				return errors.New("hls: parameter set too long for an avcC")
			}
			out = binary.BigEndian.AppendUint16(out, uint16(len(s)))
			out = append(out, s...)
		}
		return nil
	}
	if err := put(c.sps); err != nil {
		return nil, err
	}
	out = append(out, byte(len(c.pps)))
	if err := put(c.pps); err != nil {
		return nil, err
	}
	return append(out, c.rest...), nil
}

// add appends sets to c, by id: one c has the same is left out, and one
// that differs is an error.
func (c *avcConfig) add(sets ParamSets) error {
	merge := func(have [][]byte, add [][]byte, id func([]byte) (uint, error), kind string) ([][]byte, error) {
		ids := map[uint][]byte{}
		for _, s := range have {
			n, err := id(s)
			if err != nil {
				return nil, err
			}
			ids[n] = s
		}
		for _, s := range add {
			n, err := id(s)
			if err != nil {
				return nil, err
			}
			if old, ok := ids[n]; ok {
				if !bytes.Equal(old, s) {
					return nil, fmt.Errorf("hls: %s id %d is in the avcC already, with other contents", kind, n)
				}
				continue
			}
			ids[n] = s
			have = append(have, s)
		}
		return have, nil
	}
	var err error
	if c.sps, err = merge(c.sps, sets.SPS, spsID, "SPS"); err != nil {
		return err
	}
	c.pps, err = merge(c.pps, sets.PPS, ppsID, "PPS")
	return err
}

// spsID is seq_parameter_set_id: after the NAL header, profile_idc,
// constraint flags and level_idc.
func spsID(nal []byte) (uint, error) {
	if len(nal) < 5 || nal[0]&0x1f != 7 {
		return 0, errors.New("hls: not an SPS")
	}
	return firstUE(unescape(nal[4:]))
}

// ppsID is pic_parameter_set_id, the PPS's first field.
func ppsID(nal []byte) (uint, error) {
	if len(nal) < 2 || nal[0]&0x1f != 8 {
		return 0, errors.New("hls: not a PPS")
	}
	return firstUE(unescape(nal[1:]))
}

// unescape removes the emulation prevention bytes of a NAL unit's payload.
func unescape(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, x := range b {
		if zeros >= 2 && x == 3 {
			zeros = 0
			continue
		}
		if x == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, x)
	}
	return out
}

// firstUE reads the unsigned Exp-Golomb number at the start of b.
func firstUE(b []byte) (uint, error) {
	bit := func(i int) uint { return uint(b[i/8]>>(7-i%8)) & 1 }
	n := len(b) * 8
	zeros := 0
	for zeros < n && bit(zeros) == 0 {
		zeros++
	}
	if zeros > 31 || 2*zeros+1 > n {
		return 0, errors.New("hls: bad Exp-Golomb number")
	}
	v := uint(1)
	for i := zeros + 1; i <= 2*zeros; i++ {
		v = v<<1 | bit(i)
	}
	return v - 1, nil
}
