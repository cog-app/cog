package gallery

import (
	"io"
	"math"

	"github.com/rwcarlsen/goexif/exif"
)

// readEXIF extracts EXIF metadata from an image file and populates the Media struct.
func readEXIF(r io.Reader, m *Media) {
	x, err := exif.Decode(r)
	if err != nil {
		return
	}

	// Date taken
	if dt, err := x.DateTime(); err == nil {
		ts := dt.Unix()
		m.DateTaken = &ts
	}

	// GPS
	if lat, lon, err := x.LatLong(); err == nil {
		m.Latitude = &lat
		m.Longitude = &lon
	}

	// Camera info
	if tag, err := x.Get(exif.Make); err == nil {
		m.CameraMake = cleanString(tag.String())
	}
	if tag, err := x.Get(exif.Model); err == nil {
		m.CameraModel = cleanString(tag.String())
	}
	if tag, err := x.Get(exif.LensModel); err == nil {
		m.LensModel = cleanString(tag.String())
	}

	// Exposure info
	if tag, err := x.Get(exif.FocalLength); err == nil {
		if numer, denom, err := tag.Rat2(0); err == nil && denom != 0 {
			val := float64(numer) / float64(denom)
			m.FocalLength = &val
		}
	}
	if tag, err := x.Get(exif.FNumber); err == nil {
		if numer, denom, err := tag.Rat2(0); err == nil && denom != 0 {
			val := float64(numer) / float64(denom)
			val = math.Round(val*10) / 10
			m.FNumber = &val
		}
	}
	if tag, err := x.Get(exif.ExposureTime); err == nil {
		m.ExposureTime = cleanString(tag.String())
	}
	if tag, err := x.Get(exif.ISOSpeedRatings); err == nil {
		if v, err := tag.Int(0); err == nil {
			m.ISO = &v
		}
	}
	if tag, err := x.Get(exif.Flash); err == nil {
		if v, err := tag.Int(0); err == nil {
			m.Flash = &v
		}
	}

	// Orientation
	if tag, err := x.Get(exif.Orientation); err == nil {
		if v, err := tag.Int(0); err == nil && v >= 1 && v <= 8 {
			m.Orientation = v
		}
	}
}

// cleanString removes surrounding quotes from EXIF string values.
func cleanString(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return s
}
