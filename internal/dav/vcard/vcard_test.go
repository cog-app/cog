package vcard

import (
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func parse(t *testing.T, lines ...string) *Card {
	t.Helper()
	text := "BEGIN:VCARD\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VCARD\r\n"
	comps, err := contentline.Parse(strings.NewReader(text))
	require.NoError(t, err)
	return Decode(comps[0])
}

func lines(t *testing.T, c *Card) []string {
	t.Helper()
	out := strings.Split(strings.TrimSuffix(string(write(t, c)), "\r\n"), "\r\n")
	return out[1 : len(out)-1]
}

func TestAppleLabels(t *testing.T) {
	c := parse(t,
		"VERSION:3.0",
		"UID:a",
		"FN:A",
		"item1.EMAIL;type=INTERNET;type=pref:a@example.com",
		"item1.X-ABLabel:_$!<Other>!$_",
		"item2.TEL:555",
		"item2.X-ABLabel:Boat\\, mostly",
		// Two properties and a label: the label cannot pair, so the group
		// stays as received.
		"item3.ADR;type=HOME:;;Street;Town;;;",
		"item3.X-ABADR:us",
		"item3.X-ABLabel:Cabin",
		"item4.X-ABRELATEDNAMES:Bob",
		"item4.X-ABLabel:_$!<Brother>!$_",
	)
	require.Equal(t, 1, len(c.Emails))
	assert.Equal(t, "_$!<Other>!$_", c.Emails[0].Label)
	assert.Equal(t, "item1", c.Emails[0].Group)
	assert.Equal(t, []string{"INTERNET", "pref"}, c.Emails[0].Types)
	assert.Equal(t, "Boat, mostly", c.Phones[0].Label)
	assert.Equal(t, "", c.Addresses[0].Label)
	assert.Equal(t, "item3", c.Addresses[0].Group)
	assert.Equal(t, []string{"Street"}, c.Addresses[0].Street)
	assert.Equal(t, 4, len(c.Leftovers.Props), "X-ABADR, the unpaired label, and item4's two")

	assert.Equal(t, []string{
		"VERSION:3.0",
		"UID:a",
		"FN:A",
		"item1.EMAIL;TYPE=INTERNET,pref:a@example.com",
		"item1.X-ABLabel:_$!<Other>!$_",
		"item2.TEL:555",
		"item2.X-ABLabel:Boat\\, mostly",
		"item3.ADR;TYPE=HOME:;;Street;Town;;;",
		"item3.X-ABADR:us",
		"item3.X-ABLabel:Cabin",
		"item4.X-ABRELATEDNAMES:Bob",
		"item4.X-ABLabel:_$!<Brother>!$_",
	}, lines(t, c))
}

func TestNewLabelGetsGroup(t *testing.T) {
	c := parse(t, "VERSION:3.0", "UID:a", "item1.TEL:1", "item1.X-ABLabel:One", "item2.X-ABDATE:2000-01-01")
	c.Emails = append(c.Emails, Value{Meta: Meta{Label: "School"}, Value: "s@example.com"})
	c.AssignGroups()
	assert.Equal(t, "item3", c.Emails[0].Group)
	assert.Equal(t, "item3.X-ABLabel:School", lines(t, c)[3])
}

func TestGroupsAndMembers(t *testing.T) {
	v3 := parse(t,
		"VERSION:3.0",
		"UID:g",
		"FN:Family",
		"X-ADDRESSBOOKSERVER-KIND:group",
		"X-ADDRESSBOOKSERVER-MEMBER:urn:uuid:1",
		"X-ADDRESSBOOKSERVER-MEMBER:urn:uuid:2",
		"MEMBER:urn:uuid:3", // not 3.0's: a leftover
	)
	assert.True(t, v3.IsGroup())
	assert.Equal(t, 2, len(v3.Members))
	assert.Equal(t, "urn:uuid:2", v3.Members[1].URI)
	assert.True(t, v3.Leftovers.Has("MEMBER"))

	v4 := parse(t, "VERSION:4.0", "UID:g", "FN:Family", "KIND:group", "MEMBER:urn:uuid:1", "X-ADDRESSBOOKSERVER-MEMBER:x")
	assert.True(t, v4.IsGroup())
	assert.Equal(t, 1, len(v4.Members))
	assert.Equal(t, []string{
		"VERSION:4.0", "UID:g", "KIND:group", "FN:Family", "MEMBER:urn:uuid:1", "X-ADDRESSBOOKSERVER-MEMBER:x",
	}, lines(t, v4))
}

func TestPhotoByVersion(t *testing.T) {
	v3 := parse(t, "VERSION:3.0", "UID:p", "PHOTO;ENCODING=b;TYPE=JPEG;X-ABCROP-RECTANGLE=ABClipRect_1&0&0&1&1:AQID")
	require.True(t, v3.Photo != nil)
	assert.Equal(t, []byte{1, 2, 3}, v3.Photo.Data)
	assert.Equal(t, "image/jpeg", v3.Photo.MediaType())
	assert.Equal(t, "PHOTO;ENCODING=b;TYPE=JPEG;X-ABCROP-RECTANGLE=ABClipRect_1&0&0&1&1:AQID", lines(t, v3)[2])

	v4 := parse(t, "VERSION:4.0", "UID:p", "PHOTO:data:image/png;base64,AQID")
	require.True(t, v4.Photo != nil)
	assert.Equal(t, "image/png", v4.Photo.MediaType())
	assert.Equal(t, "PHOTO:data:image/png;base64,AQID", lines(t, v4)[2])

	v21 := parse(t, "VERSION:2.1", "PHOTO;JPEG;ENCODING=BASE64:AQID")
	require.True(t, v21.Photo != nil)
	assert.Equal(t, "PHOTO;ENCODING=BASE64;TYPE=JPEG:AQID", lines(t, v21)[1])

	// A URL is modelled and written as received.
	url := parse(t, "VERSION:3.0", "PHOTO;VALUE=uri;TYPE=JPEG:https://example.com/a.jpg")
	require.True(t, url.Photo != nil)
	assert.Equal(t, "https://example.com/a.jpg", url.Photo.URI)
	assert.Equal(t, "PHOTO;VALUE=uri;TYPE=JPEG:https://example.com/a.jpg", lines(t, url)[1])

	// A data: URI in 3.0, or a file: URL, is kept as a leftover.
	odd := parse(t, "VERSION:3.0", "PHOTO;ENCODING=b;TYPE=png:data:image/png;base64,AQID")
	assert.True(t, odd.Photo == nil)
	file := parse(t, "VERSION:2.1", "PHOTO;VALUE=URL:file:///jqpublic.gif")
	assert.True(t, file.Photo == nil)
	assert.True(t, file.Leftovers.Has("PHOTO"))
}

func TestVersion21(t *testing.T) {
	c := parse(t,
		"VERSION:2.1",
		"N;CHARSET=UTF-8;ENCODING=QUOTED-PRINTABLE:=C3=91a;Jo, Jr;;;",
		"TEL;CELL;PREF:123",
		"NOTE;ENCODING=QUOTED-PRINTABLE:line one=0D=0Aline two",
		"ADR;HOME:;;1 Main St\\; Rear;Town;;;",
	)
	assert.Equal(t, V21, c.Version)
	assert.Equal(t, []string{"Ña"}, c.N.Family)
	assert.Equal(t, []string{"Jo, Jr"}, c.N.Given, "2.1 has no lists")
	assert.Equal(t, []string{"CELL", "PREF"}, c.Phones[0].Types)
	assert.Equal(t, "line one\r\nline two", c.Note)
	assert.Equal(t, []string{"1 Main St; Rear"}, c.Addresses[0].Street)
	assert.Equal(t, []string{
		"VERSION:2.1",
		"N;CHARSET=UTF-8;ENCODING=QUOTED-PRINTABLE:=C3=91a;Jo, Jr;;;",
		"TEL;CELL;PREF:123",
		"ADR;HOME:;;1 Main St\\; Rear;Town;;;",
		"NOTE;ENCODING=QUOTED-PRINTABLE:line one=0D=0Aline two",
	}, lines(t, c))

	// A line break added by an edit needs quoted-printable in 2.1.
	c.FN = "Ña\nb"
	assert.Equal(t, "FN;ENCODING=QUOTED-PRINTABLE;CHARSET=UTF-8:=C3=91a=0Ab", lines(t, c)[1])

	// A charset Cog cannot read stays as received.
	sjis := parse(t, "VERSION:2.1", "FN;CHARSET=SHIFT_JIS:\x88\xc0")
	assert.Equal(t, "", sjis.FN)
	assert.True(t, sjis.Leftovers.Has("FN"))
}

func TestStructuredLists(t *testing.T) {
	c := parse(t, "VERSION:3.0", "N:Doe;John;Richter,James\\, Jr;Mr.;", "ORG:ABC\\, Inc.;Marketing", "NICKNAME:Jim,Jimmie", "ADR:;;Silicon Alley 5,;NY;;;")
	assert.Equal(t, []string{"Richter", "James, Jr"}, c.N.Additional)
	assert.Equal(t, "Mr. John Richter James, Jr Doe", c.N.Formatted())
	assert.Equal(t, []string{"ABC, Inc.", "Marketing"}, c.Org)
	assert.Equal(t, []string{"Jim", "Jimmie"}, c.Nicknames)
	assert.Equal(t, []string{"Silicon Alley 5", ""}, c.Addresses[0].Street)
	assert.Equal(t, []string{
		"VERSION:3.0",
		"N:Doe;John;Richter,James\\, Jr;Mr.;",
		"NICKNAME:Jim,Jimmie",
		"ORG:ABC\\, Inc.;Marketing",
		"ADR:;;Silicon Alley 5,;NY;;;",
	}, lines(t, c))
}

func TestChangedDropsDependents(t *testing.T) {
	c := parse(t,
		"VERSION:4.0",
		"FN:Ann",
		"N;SORT-AS=Ann;LANGUAGE=en:A;Ann;;;",
		"ORG;SORT-AS=X:Org",
		"X-PHONETIC-FIRST-NAME:an",
		"X-PHONETIC-ORG:org",
		"X-OTHER:keep",
	)
	c.Changed("N")
	assert.False(t, c.Leftovers.Has("X-PHONETIC-FIRST-NAME"))
	assert.True(t, c.Leftovers.Has("X-PHONETIC-ORG"))
	assert.Equal(t, []contentline.Param{{Name: "LANGUAGE", Values: []string{"en"}}}, c.Leftovers.Params["N"])
	c.Changed("ORG")
	assert.False(t, c.Leftovers.Has("X-PHONETIC-ORG"))
	assert.True(t, c.Leftovers.Has("X-OTHER"))
	_, ok := c.Leftovers.Params["ORG"]
	assert.False(t, ok)

	c.Stamp(time.Date(2026, 10, 26, 9, 30, 0, 0, time.FixedZone("x", 3600)))
	assert.Equal(t, "20261026T083000Z", c.Rev)
}

func TestQuotedPrintable(t *testing.T) {
	for _, s := range []string{"", "plain", "a=b", "Ñ\r\n", "trail ", "tab\tin"} {
		assert.Equal(t, s, qpDecode(qpEncode(s)))
	}
	assert.Equal(t, "=X", qpDecode("=X"))
	assert.Equal(t, "trail=20", qpEncode("trail "))
}
