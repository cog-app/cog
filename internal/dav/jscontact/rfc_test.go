package jscontact

import (
	"bytes"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// Cards made of the examples in RFC 9553 section 2 (Figures 1 and the
// property examples), one card per group of properties. The photo's data
// is real base64; the RFC elides it.
var rfcCards = map[string]string{
	"figure 1": `{
  "@type": "Card",
  "version": "1.0",
  "uid": "22B2C7DF-9120-4969-8460-05956FE6B065",
  "kind": "individual",
  "name": {
    "components": [
      { "kind": "given", "value": "John" },
      { "kind": "surname", "value": "Doe" }
    ],
    "isOrdered": true
  }
}`,
	"group": `{
  "@type": "Card",
  "version": "1.0",
  "kind": "group",
  "uid": "urn:uuid:ab4310aa-fa43-11e9-8f0b-362b9e155667",
  "name": {
    "full": "The Doe family"
  },
  "members": {
    "urn:uuid:03a0e51f-d1aa-4385-8a53-e29025acd8af": true,
    "urn:uuid:b8767877-b4a1-4c70-9acc-505d3819e519": true
  }
}`,
	"contact details": `{
  "@type": "Card",
  "version": "1.0",
  "uid": "urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6",
  "updated": "2021-10-31T22:27:10Z",
  "nicknames": {
    "k391": { "name": "Johnny" }
  },
  "organizations": {
    "o1": {
      "name": "ABC, Inc.",
      "units": [
        { "name": "North American Division" },
        { "name": "Marketing" }
      ],
      "sortAs": "ABC"
    }
  },
  "titles": {
    "le9": { "kind": "title", "name": "Research Scientist" },
    "k2": { "kind": "role", "name": "Project Leader", "organizationId": "o1" }
  },
  "emails": {
    "e1": { "contexts": { "work": true }, "address": "jqpublic@xyz.example.com" },
    "e2": { "address": "jane_doe@example.com", "pref": 1 }
  },
  "onlineServices": {
    "x-apple": { "service": "Mastodon", "user": "@alice@example2.com", "uri": "https://example2.com/@alice" }
  },
  "phones": {
    "tel0": { "contexts": { "private": true }, "features": { "voice": true }, "number": "tel:+1-555-555-5555;ext=5555" },
    "tel3": { "contexts": { "work": true }, "number": "tel:+1-201-555-0123" }
  },
  "links": {
    "link3": { "contexts": { "private": true }, "uri": "https://example.com/~jdoe" }
  },
  "keywords": { "internet": true, "IETF": true },
  "relatedTo": {
    "urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6": { "relation": { "friend": true } },
    "8cacdfb7d1ffdb59@example.com": { "relation": {} }
  }
}`,
	"address and dates": `{
  "@type": "Card",
  "version": "1.0",
  "uid": "urn:uuid:3d9b0b6d-4c8a-4d3e-9b0e-2bdbd5d1f6c1",
  "addresses": {
    "k23": {
      "contexts": { "work": true },
      "components": [
        { "kind": "number", "value": "54321" },
        { "kind": "separator", "value": " " },
        { "kind": "name", "value": "Oak St" },
        { "kind": "locality", "value": "Reston" },
        { "kind": "region", "value": "VA" },
        { "kind": "postcode", "value": "20190" },
        { "kind": "country", "value": "USA" }
      ],
      "countryCode": "US",
      "defaultSeparator": ", ",
      "isOrdered": true
    }
  },
  "anniversaries": {
    "k8": { "kind": "birth", "date": { "year": 1953, "month": 4, "day": 15 } },
    "k9": {
      "kind": "death",
      "date": { "@type": "Timestamp", "utc": "2019-10-15T23:10:00Z" },
      "place": { "full": "4445 Tree Street\nNew England, ND 58647\nUSA" }
    }
  },
  "notes": {
    "n1": {
      "note": "Open office hours are 1600 to 1715 EST, Mon-Fri",
      "created": "2022-11-23T15:01:32Z",
      "author": { "name": "John" }
    }
  },
  "media": {
    "res45": { "kind": "sound", "uri": "CID:JOHNQ.part8.19960229T080000.xyzMail@example.com" },
    "res47": { "kind": "logo", "uri": "https://www.example.com/pub/logos/abccorp.jpg" },
    "res56": { "kind": "photo", "uri": "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQ==" }
  }
}`,
}

// withoutTypes removes Cog's own properties and the @type of nested
// objects, which the RFC's examples leave out (they are optional).
func withoutTypes(v any, top bool) any {
	switch t := v.(type) {
	case jsobj.Object:
		out := jsobj.Object{}
		for k, x := range t {
			if strings.HasPrefix(k, jsobj.Vendor) || k == "@type" && !top {
				continue
			}
			out[k] = withoutTypes(x, false)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = withoutTypes(x, false)
		}
		return out
	}
	return v
}

func writeCard(t *testing.T, c *vcard.Card) string {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, contentline.Write(&b, []*contentline.Component{c.Encode()}))
	return b.String()
}

func readCard(t *testing.T, text string) *vcard.Card {
	t.Helper()
	comps, err := contentline.Parse(strings.NewReader(text))
	require.NoError(t, err)
	return vcard.Decode(comps[0])
}

// TestRFCExamples reads each card, writes it back as JSContact directly
// and after a trip through vCard text (what a CardDAV client sees and
// writes back), and checks both are the card as sent, Cog's own
// properties aside.
func TestRFCExamples(t *testing.T) {
	for name, text := range rfcCards {
		t.Run(name, func(t *testing.T) {
			in, err := jsobj.ParseObject([]byte(text))
			require.NoError(t, err)
			c, err := Decode(in)
			require.NoError(t, err)
			want := pretty(withoutTypes(in, true))

			out, err := Encode(c)
			require.NoError(t, err)
			if got := pretty(withoutTypes(out, true)); got != want {
				t.Errorf("encode(decode(x)) != x\n%s", lineDiff(want, got))
			}

			vcf := writeCard(t, c)
			back := readCard(t, vcf)
			out, err = Encode(back)
			require.NoError(t, err)
			if got := pretty(withoutTypes(out, true)); got != want {
				t.Errorf("through vCard:\n%s\n%s", vcf, lineDiff(want, got))
			}
		})
	}
}

// TestRFCExamplesInVCard checks what the examples mean once they are vCard.
func TestRFCExamplesInVCard(t *testing.T) {
	read := func(name string) *vcard.Card {
		in, err := jsobj.ParseObject([]byte(rfcCards[name]))
		require.NoError(t, err)
		c, err := Decode(in)
		require.NoError(t, err)
		return c
	}
	c := read("figure 1")
	assert.Equal(t, DefaultVersion, c.Version)
	assert.Equal(t, "individual", c.Kind)
	assert.Equal(t, []string{"Doe"}, c.N.Family)
	assert.Equal(t, []string{"John"}, c.N.Given)

	c = read("group")
	assert.True(t, c.IsGroup())
	assert.Equal(t, "The Doe family", c.FN)
	assert.Equal(t, 2, len(c.Members))

	c = read("contact details")
	assert.Equal(t, []string{"ABC, Inc.", "North American Division", "Marketing"}, c.Org)
	assert.Equal(t, "Research Scientist", c.Title)
	assert.Equal(t, "Project Leader", c.Role)
	assert.Equal(t, "20211031T222710Z", c.Rev)
	require.Equal(t, 2, len(c.Emails))
	assert.Equal(t, []string{"WORK"}, c.Emails[0].Types)
	assert.Equal(t, 1, c.Emails[1].Pref)
	require.Equal(t, 2, len(c.Phones))
	assert.Equal(t, []string{"HOME", "VOICE"}, c.Phones[0].Types)
	assert.Equal(t, []string{"IETF", "internet"}, c.Categories)

	c = read("address and dates")
	require.Equal(t, 1, len(c.Addresses))
	a := c.Addresses[0]
	assert.Equal(t, []string{"54321", "Oak St"}, a.Street)
	assert.Equal(t, []string{"Reston"}, a.Locality)
	assert.Equal(t, "1953-04-15", c.Birthday)
	assert.Equal(t, "Open office hours are 1600 to 1715 EST, Mon-Fri", c.Note)
	require.NotNil(t, c.Photo)
	assert.Equal(t, "JPEG", c.Photo.Type)
}
