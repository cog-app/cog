package vcard

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
)

func TestParseDate(t *testing.T) {
	omit := []contentline.Param{{Name: "X-APPLE-OMIT-YEAR", Values: []string{"1604"}}}
	for _, tc := range []struct {
		value  string
		params []contentline.Param
		want   Date
		ok     bool
	}{
		// 4.0 basic forms
		{"19950415", nil, Date{1995, 4, 15}, true},
		{"--0415", nil, Date{0, 4, 15}, true},
		{"19961022T140000", nil, Date{1996, 10, 22}, true},
		{"20090808T1430-0500", nil, Date{2009, 8, 8}, true},
		// 3.0 extended forms
		{"1995-04-15", nil, Date{1995, 4, 15}, true},
		{"--04-15", nil, Date{0, 4, 15}, true},
		{"1953-10-15T23:10:00Z", nil, Date{1953, 10, 15}, true},
		{"1987-09-27T08:30:00-06:00", nil, Date{1987, 9, 27}, true},
		{"2019-02-10T00:00:33", nil, Date{2019, 2, 10}, true},
		{" 1912-06-23 ", nil, Date{1912, 6, 23}, true},
		// Apple's yearless date, with and without its parameter
		{"1604-03-01", omit, Date{0, 3, 1}, true},
		{"16040301", nil, Date{0, 3, 1}, true},
		{"2000-03-01", []contentline.Param{{Name: "x-apple-omit-year", Values: []string{"2000"}}}, Date{0, 3, 1}, true},
		// leap days
		{"--0229", nil, Date{0, 2, 29}, true},
		{"19960229", nil, Date{1996, 2, 29}, true},
		{"19970229", nil, Date{}, false},
		// not dates
		{"19723101", nil, Date{}, false},
		{"--04", nil, Date{}, false},
		{"---15", nil, Date{}, false},
		{"1995-04", nil, Date{}, false},
		{"1995", nil, Date{}, false},
		{"circa 1800", nil, Date{}, false},
		{"", nil, Date{}, false},
		{"1995-0415", nil, Date{}, false},
	} {
		got, ok := ParseDate(tc.value, tc.params)
		assert.Equal(t, tc.ok, ok, tc.value)
		assert.Equal(t, tc.want, got, tc.value)
	}
}
