package files

import (
	"bufio"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// errSVGExternal is returned for an SVG that refers to something outside
// itself, which Inkscape would fetch or read while drawing it.
var errSVGExternal = errors.New("SVG refers to resources outside itself")

const xincludeNS = "http://www.w3.org/2001/XInclude"

// checkSVG refuses an SVG, plain or gzipped, that could make Inkscape read
// another file or fetch a URL: an image, font or use element, a stylesheet,
// an entity or an XInclude naming one, or a url() in a style. Only
// references into the document (#id) and data: URLs pass; a hyperlink's
// target is never fetched, so an <a> may link anywhere. A document that is
// not well-formed is refused too, since what Inkscape recovers from it is
// not what was checked.
func checkSVG(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("check svg: %w", err)
	}
	defer f.Close()
	br := bufio.NewReader(f)
	var r io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("check svg: %w", err)
		}
		defer gz.Close()
		r = gz
	}
	return checkSVGFrom(r)
}

func checkSVGFrom(r io.Reader) error {
	dec := xml.NewDecoder(r)
	inStyle := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: not well-formed: %v", errSVGExternal, err)
		}
		switch t := tok.(type) {
		case xml.Directive:
			d := strings.ToUpper(string(t))
			if strings.Contains(d, "ENTITY") || strings.Contains(d, "SYSTEM") {
				return fmt.Errorf("%w: a DOCTYPE declaring entities", errSVGExternal)
			}
		case xml.ProcInst:
			if t.Target == "xml-stylesheet" {
				return fmt.Errorf("%w: a stylesheet", errSVGExternal)
			}
		case xml.StartElement:
			if t.Name.Space == xincludeNS {
				return fmt.Errorf("%w: an XInclude", errSVGExternal)
			}
			if t.Name.Local == "style" {
				inStyle++
			}
			for _, a := range t.Attr {
				switch a.Name.Local {
				case "href", "absref":
					if t.Name.Local != "a" && !internalRef(a.Value) {
						return fmt.Errorf("%w: %s %q", errSVGExternal, t.Name.Local, a.Value)
					}
				}
				if err := checkCSS(a.Value); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "style" && inStyle > 0 {
				inStyle--
			}
		case xml.CharData:
			if inStyle > 0 {
				if err := checkCSS(string(t)); err != nil {
					return err
				}
			}
		}
	}
}

// internalRef reports whether a reference stays inside the document.
func internalRef(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "" || strings.HasPrefix(v, "#") || strings.HasPrefix(v, "data:")
}

// checkCSS refuses an @import, or a url() that leaves the document.
func checkCSS(s string) error {
	lower := strings.ToLower(s)
	if strings.Contains(lower, "@import") {
		return fmt.Errorf("%w: @import", errSVGExternal)
	}
	for {
		i := strings.Index(lower, "url(")
		if i < 0 {
			return nil
		}
		lower = lower[i+len("url("):]
		ref := strings.TrimLeft(lower, " \t\r\n'\"")
		if !internalRef(ref) {
			end := min(len(ref), 60)
			return fmt.Errorf("%w: url(%s", errSVGExternal, ref[:end])
		}
	}
}
