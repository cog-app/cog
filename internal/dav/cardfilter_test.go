package dav

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func card(t *testing.T, lines ...string) *contentline.Component {
	t.Helper()
	comps, err := contentline.Parse(strings.NewReader("BEGIN:VCARD\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VCARD\r\n"))
	require.NoError(t, err)
	return comps[0]
}

func filter(t *testing.T, inner string) *CardFilter {
	t.Helper()
	n, err := parseXML([]byte(`<CR:addressbook-query xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav">` + inner + `</CR:addressbook-query>`))
	require.NoError(t, err)
	f, err := parseCardFilter(n)
	require.NoError(t, err)
	return f
}

func TestCardFilter(t *testing.T) {
	jo := card(t, "VERSION:3.0", "UID:jo", "FN:José Smith\\, Jr", "EMAIL;TYPE=WORK:jo@Example.com", "X-ABShowAs:COMPANY")
	ann := card(t, "VERSION:3.0", "UID:ann", "FN:Ann", "EMAIL;TYPE=HOME:ann@example.org")
	for _, c := range []struct {
		name   string
		filter string
		jo     bool
		ann    bool
	}{
		{"empty matches all", ``, true, true},
		{"contains, unicode-casemap", `<CR:filter><CR:prop-filter name="FN"><CR:text-match>JOSE</CR:text-match></CR:prop-filter></CR:filter>`, true, false},
		{"escapes removed", `<CR:filter><CR:prop-filter name="FN"><CR:text-match match-type="ends-with">smith, jr</CR:text-match></CR:prop-filter></CR:filter>`, true, false},
		{"ascii-casemap does not fold é", `<CR:filter><CR:prop-filter name="FN"><CR:text-match collation="i;ascii-casemap">jose</CR:text-match></CR:prop-filter></CR:filter>`, false, false},
		{"octet is exact", `<CR:filter><CR:prop-filter name="EMAIL"><CR:text-match collation="i;octet" match-type="equals">jo@Example.com</CR:text-match></CR:prop-filter></CR:filter>`, true, false},
		{"starts-with", `<CR:filter><CR:prop-filter name="EMAIL"><CR:text-match match-type="starts-with">ANN@</CR:text-match></CR:prop-filter></CR:filter>`, false, true},
		{"negate", `<CR:filter><CR:prop-filter name="EMAIL"><CR:text-match negate-condition="yes">example.com</CR:text-match></CR:prop-filter></CR:filter>`, false, true},
		{"is-not-defined, leftovers count", `<CR:filter><CR:prop-filter name="X-ABShowAs"><CR:is-not-defined/></CR:prop-filter></CR:filter>`, false, true},
		{"defined", `<CR:filter><CR:prop-filter name="x-abshowas"/></CR:filter>`, true, false},
		{"param-filter", `<CR:filter><CR:prop-filter name="EMAIL"><CR:param-filter name="TYPE"><CR:text-match match-type="equals">home</CR:text-match></CR:param-filter></CR:prop-filter></CR:filter>`, false, true},
		{"anyof", `<CR:filter><CR:prop-filter name="FN"><CR:text-match>ann</CR:text-match></CR:prop-filter><CR:prop-filter name="FN"><CR:text-match>jos</CR:text-match></CR:prop-filter></CR:filter>`, true, true},
		{"allof", `<CR:filter test="allof"><CR:prop-filter name="FN"><CR:text-match>ann</CR:text-match></CR:prop-filter><CR:prop-filter name="EMAIL"><CR:text-match>.org</CR:text-match></CR:prop-filter></CR:filter>`, false, true},
		{"prop allof", `<CR:filter><CR:prop-filter name="EMAIL" test="allof"><CR:text-match>jo</CR:text-match><CR:param-filter name="TYPE"><CR:text-match>home</CR:text-match></CR:param-filter></CR:prop-filter></CR:filter>`, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := filter(t, c.filter)
			assert.Equal(t, c.jo, f.Match(jo))
			assert.Equal(t, c.ann, f.Match(ann))
		})
	}
}

func TestCardFilterErrors(t *testing.T) {
	n, err := parseXML([]byte(`<CR:addressbook-query xmlns:CR="urn:ietf:params:xml:ns:carddav"><CR:filter><CR:prop-filter name="FN"><CR:text-match collation="i;klingon">x</CR:text-match></CR:prop-filter></CR:filter></CR:addressbook-query>`))
	require.NoError(t, err)
	_, err = parseCardFilter(n)
	assert.True(t, err == errCollation)
}
