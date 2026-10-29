package jscal

import (
	"bytes"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func encodeICS(t *testing.T, o *ical.Object) string {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, contentline.Write(&b, []*contentline.Component{o.Encode(o.IANAZones)}))
	return b.String()
}

func decodeICS(t *testing.T, text string, kind ical.Kind) *ical.Object {
	t.Helper()
	comps, err := contentline.Parse(strings.NewReader(text))
	require.NoError(t, err)
	o, err := ical.Decode(comps[0], kind)
	require.NoError(t, err)
	return o
}
