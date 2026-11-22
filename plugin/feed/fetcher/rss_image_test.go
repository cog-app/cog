package fetcher

import (
	"testing"

	"github.com/mmcdole/gofeed"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestParseITunesDuration(t *testing.T) {
	for in, want := range map[string]int64{
		"3725": 3725, "62:05": 3725, "1:02:05": 3725, " 45 ": 45,
		"": 0, "abc": 0, "1:xx": 0, "-5": 0,
	} {
		if got := parseITunesDuration(in); got != want {
			t.Errorf("parseITunesDuration(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestFirstContentImage(t *testing.T) {
	base := "https://example.com/posts/1"
	cases := map[string]string{
		`<p>hi</p>`:          "",
		`<img src="/a.jpg">`: "https://example.com/a.jpg",
		`<img src="https://t.co/p.gif" width="1" height="1"><img src="b.png">`: "https://example.com/posts/b.png",
		`<img src="data:image/png;base64,AAAA"><img src="//cdn.x/c.jpg">`:      "https://cdn.x/c.jpg",
	}
	for in, want := range cases {
		if got := firstContentImage(in, base); got != want {
			t.Errorf("firstContentImage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestItemImagePrecedence(t *testing.T) {
	const base = "https://example.com/post"
	const head = `<?xml version="1.0"?><rss version="2.0"
		xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"
		xmlns:media="http://search.yahoo.com/mrss/"
		xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>x</title><item><title>t</title>`
	const tail = `</item></channel></rss>`
	cases := map[string]struct{ item, want string }{
		"itunes beats media and img": {
			`<itunes:image href="https://img/itunes.jpg"/><media:thumbnail url="https://img/media.jpg"/><description>&lt;img src="https://img/body.jpg"&gt;</description>`,
			"https://img/itunes.jpg",
		},
		"media beats img": {
			`<media:thumbnail url="https://img/media.jpg"/><description>&lt;img src="https://img/body.jpg"&gt;</description>`,
			"https://img/media.jpg",
		},
		"media:content medium=image": {
			`<media:content url="https://img/mc.jpg" medium="image"/>`,
			"https://img/mc.jpg",
		},
		"media:content image type in group": {
			`<media:group><media:content url="https://img/g.jpg" type="image/jpeg"/></media:group>`,
			"https://img/g.jpg",
		},
		"media:content video ignored": {
			`<media:content url="https://img/v.mp4" type="video/mp4"/>`,
			"",
		},
		"relative media thumbnail": {
			`<media:thumbnail url="/t.jpg"/>`,
			"https://example.com/t.jpg",
		},
		"content:encoded before description": {
			`<description>&lt;img src="https://img/desc.jpg"&gt;</description><content:encoded>&lt;img src="https://img/enc.jpg"&gt;</content:encoded>`,
			"https://img/enc.jpg",
		},
		"description only": {
			`<description>&lt;img src="https://img/desc.jpg"&gt;</description>`,
			"https://img/desc.jpg",
		},
		"non-http thumbnail skipped": {
			`<media:thumbnail url="ftp://img/t.jpg"/>`,
			"",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			parsed, err := gofeed.NewParser().ParseString(head + c.item + tail)
			require.NoError(t, err)
			require.Equal(t, 1, len(parsed.Items))
			if got := itemImage(parsed.Items[0], base); got != c.want {
				t.Errorf("itemImage = %q, want %q", got, c.want)
			}
		})
	}
}
