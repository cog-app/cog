package hls

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// FirstDecodeTime is the decode time, in seconds, of a fragmented MP4
// track's first fragment: its tfdt over the track's timescale. Players
// place fragments by it, while ReadTrack's times come from the sidx, which
// a muxer may write independently.
func FirstDecodeTime(r io.ReaderAt, size int64) (float64, error) {
	var timescale uint32
	for off := int64(0); off < size; {
		b, err := readBoxHeader(r, off, size)
		if err != nil {
			return 0, fmt.Errorf("hls: %w", err)
		}
		switch b.typ {
		case "moov":
			moov, err := readBody(r, b)
			if err != nil {
				return 0, err
			}
			mdhd, err := path(moov, "trak", "mdia", "mdhd")
			if err != nil {
				return 0, fmt.Errorf("hls: moov: %w", err)
			}
			if timescale, err = mdhdTimescale(mdhd); err != nil {
				return 0, err
			}
		case "moof":
			if timescale == 0 {
				return 0, errors.New("hls: moof before a moov with a timescale")
			}
			moof, err := readBody(r, b)
			if err != nil {
				return 0, err
			}
			tfdt, err := path(moof, "traf", "tfdt")
			if err != nil {
				return 0, fmt.Errorf("hls: moof: %w", err)
			}
			t, err := tfdtTime(tfdt)
			if err != nil {
				return 0, err
			}
			return float64(t) / float64(timescale), nil
		}
		off = b.end
	}
	return 0, errors.New("hls: no fragment")
}

func mdhdTimescale(b []byte) (uint32, error) {
	at := 12 // version 0: version+flags, creation and modification times (32-bit)
	if len(b) > 0 && b[0] == 1 {
		at = 20 // version 1: 64-bit times
	}
	if len(b) < at+4 {
		return 0, errors.New("hls: mdhd box is short")
	}
	ts := binary.BigEndian.Uint32(b[at:])
	if ts == 0 {
		return 0, errors.New("hls: mdhd timescale is 0")
	}
	return ts, nil
}

func tfdtTime(b []byte) (uint64, error) {
	switch {
	case len(b) >= 12 && b[0] == 1:
		return binary.BigEndian.Uint64(b[4:]), nil
	case len(b) >= 8 && b[0] == 0:
		return uint64(binary.BigEndian.Uint32(b[4:])), nil
	}
	return 0, errors.New("hls: tfdt box is short or of an unknown version")
}
