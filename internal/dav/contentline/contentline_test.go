package contentline

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf16"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func parseString(t *testing.T, s string) []*Component {
	t.Helper()
	comps, err := Parse(strings.NewReader(s))
	require.NoError(t, err)
	return comps
}

func writeString(t *testing.T, comps []*Component) string {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, Write(&b, comps))
	return b.String()
}

func TestParseNested(t *testing.T) {
	comps := parseString(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:1\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	require.Equal(t, 1, len(comps))
	cal := comps[0]
	assert.Equal(t, "VCALENDAR", cal.Name)
	assert.Equal(t, []Property{{Name: "VERSION", Value: "2.0"}}, cal.Props)
	require.Equal(t, 1, len(cal.Components))
	ev := cal.Components[0]
	assert.Equal(t, "VEVENT", ev.Name)
	assert.Equal(t, "1", ev.Prop("uid").Value)
	require.Equal(t, 1, len(ev.Components))
	assert.Equal(t, "VALARM", ev.Components[0].Name)
}

func TestParseUnfold(t *testing.T) {
	// Space and tab continuations, LF-only endings, and a fold that splits a
	// multi-byte rune.
	comps := parseString(t, "BEGIN:VEVENT\nSUMMARY:Hel\n lo\n\t world\nDESCRIPTION:caf\xc3\n \xa9\nEND:VEVENT\n")
	ev := comps[0]
	assert.Equal(t, "Hello world", ev.Prop("SUMMARY").Value)
	assert.Equal(t, "café", ev.Prop("DESCRIPTION").Value)
}

func TestParseParams(t *testing.T) {
	comps := parseString(t, "BEGIN:VCARD\r\n"+
		`item1.EMAIL;type=INTERNET;TYPE=WORK,pref:a@example.com`+"\r\n"+
		`ATTENDEE;CN="Doe, Jane";MEMBER="mailto:a@x","mailto:b@x";X-EMPTY=:mailto:j@x`+"\r\n"+
		`TEL;HOME;VOICE:555`+"\r\n"+
		`X-CARET;X-P=a^nb^^c^'d^x:v`+"\r\n"+
		"END:VCARD\r\n")
	c := comps[0]
	email := c.Prop("EMAIL")
	require.True(t, email != nil)
	assert.Equal(t, "item1", email.Group)
	assert.Equal(t, []Param{{Name: "type", Values: []string{"INTERNET"}}, {Name: "TYPE", Values: []string{"WORK", "pref"}}}, email.Params)
	assert.Equal(t, []string{"INTERNET", "WORK", "pref"}, email.ParamValues("TYPE"))

	att := c.Prop("ATTENDEE")
	assert.Equal(t, "Doe, Jane", att.Param("CN"))
	assert.Equal(t, []string{"mailto:a@x", "mailto:b@x"}, att.ParamValues("MEMBER"))
	assert.Equal(t, []Param{{Name: "X-EMPTY", Values: []string{""}}}, att.Params[2:])
	assert.Equal(t, "mailto:j@x", att.Value)

	tel := c.Prop("TEL")
	assert.Equal(t, []Param{{Values: []string{"HOME"}}, {Values: []string{"VOICE"}}}, tel.Params)

	assert.Equal(t, "a\nb^c\"d^x", c.Prop("X-CARET").Param("X-P"))
}

func TestParseColonInValue(t *testing.T) {
	comps := parseString(t, "BEGIN:VEVENT\r\nURL;VALUE=URI:https://x.example/a:b\r\nDTSTART;TZID=\"Europe/Berlin\":20260101T090000\r\nEND:VEVENT\r\n")
	assert.Equal(t, "https://x.example/a:b", comps[0].Prop("URL").Value)
	assert.Equal(t, "Europe/Berlin", comps[0].Prop("DTSTART").Param("TZID"))
	assert.Equal(t, "20260101T090000", comps[0].Prop("DTSTART").Value)
}

func TestParseQuotedPrintableSoftBreaks(t *testing.T) {
	comps := parseString(t, "BEGIN:VCARD\r\nVERSION:2.1\r\nNOTE;ENCODING=QUOTED-PRINTABLE:first=0D=0A=\r\nsecond=\r\n third\r\nN:X\r\nEND:VCARD\r\n")
	assert.Equal(t, "first=0D=0Asecond third", comps[0].Prop("NOTE").Value)
	assert.Equal(t, "X", comps[0].Prop("N").Value)
}

func TestParseBlankLinesAndBOM(t *testing.T) {
	comps := parseString(t, "\ufeffBEGIN:VCARD\r\nPHOTO;BASE64:\r\n AAAA\r\n BBBB\r\n\r\nFN:A\r\nEND:VCARD\r\n\r\nBEGIN:VCARD\r\nFN:B\r\nEND:VCARD\r\n")
	require.Equal(t, 2, len(comps))
	assert.Equal(t, "AAAABBBB", comps[0].Prop("PHOTO").Value)
	assert.Equal(t, "B", comps[1].Prop("FN").Value)
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"unclosed":         "BEGIN:VEVENT\r\nSUMMARY:x\r\n",
		"mismatched end":   "BEGIN:VEVENT\r\nEND:VTODO\r\n",
		"stray end":        "END:VEVENT\r\n",
		"prop before":      "SUMMARY:x\r\nBEGIN:VEVENT\r\nEND:VEVENT\r\n",
		"junk before":      "junk\r\nBEGIN:VEVENT\r\nEND:VEVENT\r\n",
		"leading fold":     " x\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(in))
			assert.Error(t, err)
		})
	}
}

func TestWriteFoldsAt75Octets(t *testing.T) {
	long := strings.Repeat("é", 60) // 120 octets
	out := writeString(t, []*Component{{Name: "VEVENT", Props: []Property{{Name: "SUMMARY", Value: long}}}})
	for line := range strings.SplitSeq(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		assert.True(t, len(line) <= 75, "line %q is %d octets", line, len(line))
		assert.True(t, utf8ValidPrefix(line), "fold split a rune: %q", line)
	}
	assert.Equal(t, long, parseString(t, out)[0].Prop("SUMMARY").Value)
}

func utf8ValidPrefix(s string) bool {
	return strings.ToValidUTF8(strings.TrimPrefix(s, " "), "�") == strings.TrimPrefix(s, " ")
}

func TestWriteQuotesParams(t *testing.T) {
	out := writeString(t, []*Component{{Name: "VEVENT", Props: []Property{{
		Name: "ATTENDEE",
		Params: []Param{
			{Name: "CN", Values: []string{"Doe, Jane"}},
			{Name: "MEMBER", Values: []string{"mailto:a@x", "mailto:b@x"}},
			{Name: "X-NL", Values: []string{"a\nb^\""}},
			{Values: []string{"HOME"}},
		},
		Value: "mailto:j@x",
	}}}})
	assert.Equal(t, "BEGIN:VEVENT\r\n"+
		`ATTENDEE;CN="Doe, Jane";MEMBER="mailto:a@x","mailto:b@x";X-NL=a^nb^^^';HOME:mailto:j@x`+"\r\n"+
		"END:VEVENT\r\n", strings.ReplaceAll(out, "\r\n ", ""))
}

func TestWriteGroup(t *testing.T) {
	out := writeString(t, []*Component{{Name: "VCARD", Props: []Property{{Group: "item1", Name: "X-ABLabel", Value: "_$!<Other>!$_"}}}})
	assert.Equal(t, "BEGIN:VCARD\r\nitem1.X-ABLabel:_$!<Other>!$_\r\nEND:VCARD\r\n", out)
}

func TestWriteQuotedPrintableUsesSoftBreaks(t *testing.T) {
	val := strings.Repeat("=C3=A9", 30)
	out := writeString(t, []*Component{{Name: "VCARD", Props: []Property{{
		Name:   "NOTE",
		Params: []Param{{Name: "ENCODING", Values: []string{"QUOTED-PRINTABLE"}}},
		Value:  val,
	}}}})
	lines := strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n")
	for _, line := range lines[1 : len(lines)-2] {
		assert.True(t, strings.HasSuffix(line, "="), "soft break expected: %q", line)
		assert.False(t, strings.HasPrefix(line, " "), "QP continuation must not be space-folded: %q", line)
	}
	for _, line := range lines {
		assert.True(t, len(line) <= 76, "line too long: %q", line)
	}
	assert.Equal(t, val, parseString(t, out)[0].Prop("NOTE").Value)
}

func TestWriteDeterministic(t *testing.T) {
	in := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSUMMARY;LANGUAGE=de:" + strings.Repeat("x", 200) + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	comps := parseString(t, in)
	a, b := writeString(t, comps), writeString(t, comps)
	assert.Equal(t, a, b)
	assert.Equal(t, comps, parseString(t, a))
}

func TestParseKeepsMalformedLines(t *testing.T) {
	in := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nX-APPLE-RADIUS=49.9\r\nDTSTART;TZID=\"W. Europe Standard Time:20200609T090000\"\r\nEND:VEVENT\r\nEND:VCALENDAR\r\nX-COMMENT:after\r\n"
	comps := parseString(t, in)
	ev := comps[0].Components[0]
	require.Equal(t, 2, len(ev.Props))
	assert.True(t, ev.Props[0].Malformed())
	assert.Equal(t, "X-APPLE-RADIUS=49.9", ev.Props[0].Value)
	assert.True(t, ev.Props[1].Malformed())
	assert.Equal(t, "after", comps[0].Prop("X-COMMENT").Value)

	out := writeString(t, comps)
	assert.Contains(t, out, "\r\nX-APPLE-RADIUS=49.9\r\nDTSTART;TZID=\"W. Europe Standard Time:20200609T090000\"\r\n")
	assert.Contains(t, out, "BEGIN:VCALENDAR\r\nX-COMMENT:after\r\n")
}

func TestParseUTF16AndCRCRLF(t *testing.T) {
	src := "BEGIN:VCARD\r\r\nFN:Zoë\r\r\nEND:VCARD\r\r\n"
	u := utf16.Encode([]rune(src))
	b := []byte{0xFF, 0xFE}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	comps, err := Parse(bytes.NewReader(b))
	require.NoError(t, err)
	assert.Equal(t, "Zoë", comps[0].Prop("FN").Value)
}
