package files

import (
	"errors"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestCheckSVG(t *testing.T) {
	const head = `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" xmlns:sodipodi="http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd">`
	ok := []string{
		head + `<rect width="1" height="1" fill="url(#g)"/></svg>`,
		head + `<use xlink:href="#a"/><image href="data:image/png;base64,AAAA"/></svg>`,
		head + `<a href="https://example.com/"><text>link</text></a></svg>`,
		`<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">` + head + `</svg>`,
	}
	for _, s := range ok {
		require.NoError(t, checkSVGFrom(strings.NewReader(s)), "%s", s)
	}
	bad := []string{
		head + `<image xlink:href="file:///etc/passwd"/></svg>`,
		head + `<image href="/data/alice/files/secret.png"/></svg>`,
		head + `<image xlink:href="#x" sodipodi:absref="/etc/shadow"/></svg>`,
		head + `<image href="http://169.254.169.254/latest/meta-data/"/></svg>`,
		head + `<rect style="fill:url( 'file:///etc/passwd')"/></svg>`,
		head + `<style>@import url(x.css);</style></svg>`,
		head + `<style>rect { fill: url(/tmp/x.svg#a) }</style></svg>`,
		`<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]>` + head + `<text>&x;</text></svg>`,
		`<?xml-stylesheet href="file:///etc/x.css"?>` + head + `</svg>`,
		head + `<xi:include xmlns:xi="http://www.w3.org/2001/XInclude" href="file:///etc/passwd" parse="text"/></svg>`,
		head + `<rect`,
	}
	for _, s := range bad {
		err := checkSVGFrom(strings.NewReader(s))
		require.True(t, errors.Is(err, errSVGExternal), "accepted %s (%v)", s, err)
	}
}
