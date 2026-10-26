package dav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// XML namespaces.
const (
	nsDAV     = "DAV:"
	nsCalDAV  = "urn:ietf:params:xml:ns:caldav"
	nsCardDAV = "urn:ietf:params:xml:ns:carddav"
	nsCS      = "http://calendarserver.org/ns/"
	nsApple   = "http://apple.com/ns/ical/"
)

// prefixes are declared on every response root, so property values can use
// them. Names in any other namespace are written with a default namespace
// declaration of their own.
var prefixes = []struct{ ns, prefix string }{
	{nsDAV, "D"}, {nsCalDAV, "C"}, {nsCardDAV, "CR"}, {nsCS, "CS"}, {nsApple, "ICAL"},
}

var rootDecls = func() string {
	var b strings.Builder
	for _, p := range prefixes {
		fmt.Fprintf(&b, ` xmlns:%s="%s"`, p.prefix, p.ns)
	}
	return b.String()
}()

func dav(local string) xml.Name    { return xml.Name{Space: nsDAV, Local: local} }
func caldav(local string) xml.Name { return xml.Name{Space: nsCalDAV, Local: local} }
func carddav(local string) xml.Name {
	return xml.Name{Space: nsCardDAV, Local: local}
}
func cs(local string) xml.Name    { return xml.Name{Space: nsCS, Local: local} }
func apple(local string) xml.Name { return xml.Name{Space: nsApple, Local: local} }

// tag returns how to open an element: its qualified tag and, for a
// namespace with no declared prefix, the declaration to add.
func tag(n xml.Name) (name, decl string) {
	for _, p := range prefixes {
		if p.ns == n.Space {
			return p.prefix + ":" + n.Local, ""
		}
	}
	if n.Space == "" {
		return n.Local, ` xmlns=""`
	}
	return n.Local, ` xmlns="` + escape(n.Space) + `"`
}

// el returns an element with inner XML, which must already be escaped.
func el(n xml.Name, inner string) string {
	t, decl := tag(n)
	if inner == "" {
		return "<" + t + decl + "/>"
	}
	return "<" + t + decl + ">" + inner + "</" + t + ">"
}

// hrefEl is a DAV:href element.
func hrefEl(href string) string { return el(dav("href"), escape(href)) }

func escape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		// EscapeText fails only on its writer; a Builder never does.
		panic(err)
	}
	return b.String()
}

// --- Requests ---

// node is a parsed XML element. Content holds, in order, the element's text
// (string) and child elements (*node).
type node struct {
	Name    xml.Name
	Attr    []xml.Attr
	Content []any
}

// maxBody bounds an XML request body.
const maxBody = 1 << 20

var errEmptyBody = errors.New("empty body")

// readXML parses a request body. An empty body returns errEmptyBody.
func readXML(r *http.Request) (*node, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("body larger than %d bytes", maxBody)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errEmptyBody
	}
	return parseXML(body)
}

func parseXML(body []byte) (*node, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	var stack []*node
	var root *node
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{Name: t.Name}
			for _, a := range t.Attr {
				if a.Name.Space != "xmlns" && a.Name.Local != "xmlns" {
					n.Attr = append(n.Attr, a)
				}
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.Content = append(p.Content, n)
			} else if root != nil {
				return nil, errors.New("more than one root element")
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.Content = append(p.Content, string(t))
			}
		}
	}
	if root == nil {
		return nil, errors.New("no root element")
	}
	return root, nil
}

func (n *node) is(name xml.Name) bool { return n != nil && n.Name == name }

// children yields the element children.
func (n *node) children() []*node {
	if n == nil {
		return nil
	}
	var out []*node
	for _, c := range n.Content {
		if e, ok := c.(*node); ok {
			out = append(out, e)
		}
	}
	return out
}

// child returns the first child element named name, or nil.
func (n *node) child(name xml.Name) *node {
	for _, c := range n.children() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// text returns the element's text content, its descendants' included,
// trimmed.
func (n *node) text() string {
	var b strings.Builder
	var walk func(*node)
	walk = func(n *node) {
		for _, c := range n.Content {
			switch c := c.(type) {
			case string:
				b.WriteString(c)
			case *node:
				walk(c)
			}
		}
	}
	if n != nil {
		walk(n)
	}
	return strings.TrimSpace(b.String())
}

func (n *node) attr(local string) string {
	for _, a := range n.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// innerXML re-encodes the element's content with every namespace declared
// where it is used, so it can be stored and written back anywhere.
func (n *node) innerXML() (string, error) {
	var b bytes.Buffer
	e := xml.NewEncoder(&b)
	var enc func(items []any) error
	enc = func(items []any) error {
		for _, c := range items {
			switch c := c.(type) {
			case string:
				if err := e.EncodeToken(xml.CharData(c)); err != nil {
					return err
				}
			case *node:
				start := xml.StartElement{Name: c.Name, Attr: c.Attr}
				if err := e.EncodeToken(start); err != nil {
					return err
				}
				if err := enc(c.Content); err != nil {
					return err
				}
				if err := e.EncodeToken(start.End()); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := enc(n.Content); err != nil {
		return "", err
	}
	if err := e.Flush(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// propNames returns the names of a DAV:prop element's children.
func (n *node) propNames() []xml.Name {
	var names []xml.Name
	for _, c := range n.children() {
		names = append(names, c.Name)
	}
	return names
}

// --- Responses ---

// prop is a property and its value as XML, already escaped.
type prop struct {
	Name  xml.Name
	Inner string
}

type propstat struct {
	Status int
	Props  []prop
	Error  string // optional DAV:error content
}

type response struct {
	Href      string
	Status    int    // for a response with no properties, e.g. a 404 in a sync report
	Error     string // with Status: the DAV:error's content
	Propstats []propstat
}

type multistatus struct {
	Responses []response
	SyncToken string
}

func statusLine(code int) string {
	return fmt.Sprintf("HTTP/1.1 %d %s", code, http.StatusText(code))
}

func (m *multistatus) write(w http.ResponseWriter) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<D:multistatus` + rootDecls + `>`)
	for _, r := range m.Responses {
		b.WriteString(`<D:response>`)
		b.WriteString(hrefEl(r.Href))
		if len(r.Propstats) == 0 {
			b.WriteString(el(dav("status"), statusLine(r.Status)))
			if r.Error != "" {
				b.WriteString(el(dav("error"), r.Error))
			}
		}
		for _, ps := range r.Propstats {
			b.WriteString(`<D:propstat><D:prop>`)
			for _, p := range ps.Props {
				b.WriteString(el(p.Name, p.Inner))
			}
			b.WriteString(`</D:prop>`)
			b.WriteString(el(dav("status"), statusLine(ps.Status)))
			if ps.Error != "" {
				b.WriteString(el(dav("error"), ps.Error))
			}
			b.WriteString(`</D:propstat>`)
		}
		b.WriteString(`</D:response>`)
	}
	if m.SyncToken != "" {
		b.WriteString(el(dav("sync-token"), escape(m.SyncToken)))
	}
	b.WriteString(`</D:multistatus>`)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	io.WriteString(w, b.String())
}

// writeError sends a status with a DAV:error body naming the failed
// precondition (RFC 4918 §16).
func writeError(w http.ResponseWriter, code int, condition xml.Name, inner string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(code)
	io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
		`<D:error`+rootDecls+`>`+el(condition, inner)+`</D:error>`)
}
