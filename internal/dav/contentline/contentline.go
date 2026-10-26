// Package contentline reads and writes the content-line syntax shared by
// iCalendar (RFC 5545) and vCard (RFC 2425, 2426, 6350).
//
// It knows syntax only: components, properties, parameters and folding. It
// has no domain types and does not interpret values. Text escaping (\, \; \n)
// depends on a property's value type, so codecs apply it, not this package.
//
// Names keep the spelling they were received with ("X-ABLabel" stays
// "X-ABLabel"), because anything Cog does not model is written back exactly as
// a client sent it. Compare names with EqualFold, or use the lookup helpers,
// which do.
package contentline

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"unicode/utf16"
)

// Component is a BEGIN/END block, such as VCALENDAR, VEVENT or VCARD.
type Component struct {
	Name       string
	Props      []Property   // in input order
	Components []*Component // nested, in input order
}

// Property is one content line.
//
// A line that is not valid content-line syntax is kept as a malformed
// property: Name is "" and Value holds the whole line, exactly as received.
// Write emits it unchanged, so a client's data survives even when Cog cannot
// read it.
type Property struct {
	Group  string  // vCard group, e.g. "item1" in "item1.EMAIL"; "" when absent
	Name   string  // as received
	Params []Param // in input order
	Value  string  // raw value text, still escaped
}

// Param is one property parameter. Values are unquoted and RFC 6868 caret
// escapes are decoded.
//
// A parameter written without a name, as vCard 2.1 does ("TEL;HOME;VOICE:"),
// has Name "" and its token as the only value.
type Param struct {
	Name   string
	Values []string // multi-valued, e.g. MEMBER="a","b" or TYPE=WORK,VOICE
}

// Is reports whether the component is named name, ignoring case.
func (c *Component) Is(name string) bool { return strings.EqualFold(c.Name, name) }

// Prop returns the first property named name, or nil.
func (c *Component) Prop(name string) *Property {
	for i := range c.Props {
		if c.Props[i].Is(name) {
			return &c.Props[i]
		}
	}
	return nil
}

// PropsNamed yields every property named name, in order.
func (c *Component) PropsNamed(name string) iter.Seq[*Property] {
	return func(yield func(*Property) bool) {
		for i := range c.Props {
			if c.Props[i].Is(name) && !yield(&c.Props[i]) {
				return
			}
		}
	}
}

// Is reports whether the property is named name, ignoring case and group.
func (p *Property) Is(name string) bool { return strings.EqualFold(p.Name, name) }

// Malformed reports whether the line could not be parsed; see Property.
func (p *Property) Malformed() bool { return p.Name == "" }

// Param returns the first value of the first parameter named name, or "".
func (p *Property) Param(name string) string {
	for _, pa := range p.Params {
		if strings.EqualFold(pa.Name, name) && len(pa.Values) > 0 {
			return pa.Values[0]
		}
	}
	return ""
}

// ParamValues returns the values of every parameter named name, flattened, so
// "TYPE=a;TYPE=b" and "TYPE=a,b" read the same.
func (p *Property) ParamValues(name string) []string {
	var vals []string
	for _, pa := range p.Params {
		if strings.EqualFold(pa.Name, name) {
			vals = append(vals, pa.Values...)
		}
	}
	return vals
}

// isQuotedPrintable reports whether the value uses vCard 2.1 quoted-printable
// encoding, whose soft line breaks replace folding.
func (p *Property) isQuotedPrintable() bool {
	for _, pa := range p.Params {
		if pa.Name == "" || strings.EqualFold(pa.Name, "ENCODING") {
			for _, v := range pa.Values {
				if strings.EqualFold(v, "QUOTED-PRINTABLE") {
					return true
				}
			}
		}
	}
	return false
}

// ParseError reports malformed input and the physical line it starts on.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// Parse reads every top-level component from r.
//
// It accepts CRLF or bare LF, unfolds lines continued with a space or tab,
// joins vCard 2.1 quoted-printable soft line breaks, skips blank lines and a
// leading byte-order mark, and decodes UTF-16 input marked by one.
//
// It is lenient where real clients are sloppy: a malformed line inside a
// component is kept as a malformed property, and a property after the last
// END belongs to the component before it. Structural errors (unbalanced
// BEGIN/END, content before the first BEGIN) are errors.
func Parse(r io.Reader) ([]*Component, error) {
	br := bufio.NewReader(r)
	if head, _ := br.Peek(2); len(head) == 2 && isUTF16(head) {
		b, err := io.ReadAll(br)
		if err != nil {
			return nil, err
		}
		br = bufio.NewReader(strings.NewReader(decodeUTF16(b)))
	}
	lr := lineReader{r: br}
	var (
		roots []*Component
		stack []*Component
	)
	for {
		line, lineNo, err := lr.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		prop, err := parseLine(line)
		if err != nil {
			prop = Property{Value: line}
		}
		switch {
		case prop.Malformed() && len(stack) == 0:
			return nil, &ParseError{Line: lineNo, Msg: err.Error()}
		case prop.Group == "" && prop.Is("BEGIN"):
			c := &Component{Name: prop.Value}
			if len(stack) == 0 {
				roots = append(roots, c)
			} else {
				parent := stack[len(stack)-1]
				parent.Components = append(parent.Components, c)
			}
			stack = append(stack, c)
		case prop.Group == "" && prop.Is("END"):
			if len(stack) == 0 {
				return nil, &ParseError{Line: lineNo, Msg: "END:" + prop.Value + " without BEGIN"}
			}
			if top := stack[len(stack)-1]; !top.Is(prop.Value) {
				return nil, &ParseError{Line: lineNo, Msg: "END:" + prop.Value + " closes BEGIN:" + top.Name}
			}
			stack = stack[:len(stack)-1]
		default:
			var top *Component
			switch {
			case len(stack) > 0:
				top = stack[len(stack)-1]
			case len(roots) > 0:
				top = roots[len(roots)-1]
			default:
				return nil, &ParseError{Line: lineNo, Msg: "property " + prop.Name + " before the first BEGIN"}
			}
			top.Props = append(top.Props, prop)
		}
	}
	if len(stack) > 0 {
		return nil, &ParseError{Line: lr.lineNo, Msg: "BEGIN:" + stack[len(stack)-1].Name + " never ends"}
	}
	return roots, nil
}

// lineReader yields logical (unfolded) lines.
type lineReader struct {
	r       *bufio.Reader
	lineNo  int    // physical lines read
	pending string // physical line read ahead
	hasPend bool
	started bool
}

// physical returns the next physical line without its line ending.
func (lr *lineReader) physical() (string, error) {
	if lr.hasPend {
		lr.hasPend = false
		return lr.pending, nil
	}
	s, err := lr.r.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || s == "") {
		return "", err
	}
	lr.lineNo++
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimRight(s, "\r") // iOS 6 wrote CR CR LF
	if !lr.started {
		lr.started = true
		s = strings.TrimPrefix(s, "\ufeff")
	}
	return s, nil
}

func (lr *lineReader) unread(s string) {
	lr.pending, lr.hasPend = s, true
}

func (lr *lineReader) next() (string, int, error) {
	var first string
	for {
		s, err := lr.physical()
		if err != nil {
			return "", 0, err
		}
		if s == "" {
			continue
		}
		if s[0] == ' ' || s[0] == '\t' {
			return "", 0, &ParseError{Line: lr.lineNo, Msg: "continuation line without a line to continue"}
		}
		first = s
		break
	}
	start := lr.lineNo
	var b strings.Builder
	b.WriteString(first)
	qp := -1 // unknown until the line ends in "="
	for {
		s, err := lr.physical()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", 0, err
		}
		cur := b.String()
		if strings.HasSuffix(cur, "=") {
			if qp < 0 {
				qp = 0
				if p, err := parseLine(cur); err == nil && p.isQuotedPrintable() {
					qp = 1
				}
			}
			if qp == 1 {
				// Soft line break: drop the "=" and take the next line
				// literally, leading whitespace included.
				b.Reset()
				b.WriteString(cur[:len(cur)-1])
				b.WriteString(s)
				continue
			}
		}
		if s == "" {
			// Blank lines end a vCard 2.1 base64 value but carry nothing;
			// skipping them also mends folds split by one.
			continue
		}
		if s[0] == ' ' || s[0] == '\t' {
			b.WriteString(s[1:])
			continue
		}
		lr.unread(s)
		break
	}
	return b.String(), start, nil
}

// parseLine splits an unfolded content line into its parts.
func parseLine(line string) (Property, error) {
	var p Property
	i := strings.IndexAny(line, ";:")
	if i < 0 {
		return p, errors.New("missing ':'")
	}
	name := line[:i]
	if g, n, ok := strings.Cut(name, "."); ok {
		p.Group, name = g, n
	}
	if name == "" {
		return p, errors.New("empty property name")
	}
	p.Name = name
	rest := line[i:]
	for rest[0] == ';' {
		rest = rest[1:]
		j := strings.IndexAny(rest, "=;:")
		if j < 0 {
			return p, errors.New("missing ':'")
		}
		if rest[j] != '=' {
			// Nameless vCard 2.1 parameter.
			p.Params = append(p.Params, Param{Values: []string{decodeCaret(rest[:j])}})
			rest = rest[j:]
			continue
		}
		pa := Param{Name: rest[:j]}
		rest = rest[j+1:]
		for {
			var v string
			// Tolerate whitespace before an opening quote, as in RFC 5545's
			// own folded examples ("DELEGATED-FROM=\r\n  \"mailto:...").
			if t := strings.TrimLeft(rest, " \t"); t != "" && t[0] == '"' {
				rest = t
			}
			if rest != "" && rest[0] == '"' {
				end := strings.IndexByte(rest[1:], '"')
				if end < 0 {
					return p, errors.New("unterminated quoted parameter value")
				}
				v, rest = rest[1:end+1], rest[end+2:]
			} else {
				k := strings.IndexAny(rest, ",;:")
				if k < 0 {
					return p, errors.New("missing ':'")
				}
				v, rest = rest[:k], rest[k:]
			}
			pa.Values = append(pa.Values, decodeCaret(v))
			if rest == "" {
				return p, errors.New("missing ':'")
			}
			if rest[0] != ',' {
				break
			}
			rest = rest[1:]
		}
		p.Params = append(p.Params, pa)
		if rest[0] != ';' && rest[0] != ':' {
			return p, fmt.Errorf("unexpected %q after parameter %s", rest[0], pa.Name)
		}
	}
	p.Value = rest[1:]
	return p, nil
}

// decodeCaret decodes RFC 6868 parameter escapes: ^n, ^^ and ^'. Any other
// caret is literal.
func decodeCaret(s string) string {
	if !strings.Contains(s, "^") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '^' && i+1 < len(s) {
			switch s[i+1] {
			case 'n', 'N':
				b.WriteByte('\n')
				i++
				continue
			case '^':
				b.WriteByte('^')
				i++
				continue
			case '\'':
				b.WriteByte('"')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Write writes components with CRLF line endings, folding lines at 75
// octets. The same tree always produces the same bytes.
func Write(w io.Writer, comps []*Component) error {
	var buf []byte
	for _, c := range comps {
		buf = appendComponent(buf, c)
	}
	_, err := w.Write(buf)
	return err
}

func appendComponent(buf []byte, c *Component) []byte {
	buf = append(buf, "BEGIN:"...)
	buf = append(buf, c.Name...)
	buf = append(buf, "\r\n"...)
	for i := range c.Props {
		buf = appendProperty(buf, &c.Props[i])
	}
	for _, sub := range c.Components {
		buf = appendComponent(buf, sub)
	}
	buf = append(buf, "END:"...)
	buf = append(buf, c.Name...)
	return append(buf, "\r\n"...)
}

// maxLine is the longest line RFC 5545 and RFC 6350 allow, in octets,
// excluding the line break.
const maxLine = 75

func appendProperty(buf []byte, p *Property) []byte {
	if p.Malformed() {
		return appendFolded(buf, []byte(p.Value))
	}
	line := make([]byte, 0, len(p.Name)+len(p.Value)+16)
	if p.Group != "" {
		line = append(line, p.Group...)
		line = append(line, '.')
	}
	line = append(line, p.Name...)
	for _, pa := range p.Params {
		line = append(line, ';')
		if pa.Name != "" {
			line = append(line, pa.Name...)
			line = append(line, '=')
		}
		for i, v := range pa.Values {
			if i > 0 {
				line = append(line, ',')
			}
			line = appendParamValue(line, v)
		}
	}
	line = append(line, ':')
	if p.isQuotedPrintable() {
		return appendSoftBroken(buf, line, p.Value)
	}
	line = append(line, p.Value...)
	return appendFolded(buf, line)
}

func appendParamValue(buf []byte, v string) []byte {
	quote := strings.ContainsAny(v, ",;:")
	if quote {
		buf = append(buf, '"')
	}
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '^':
			// A caret is literal unless it would start an escape, so "^a"
			// round-trips as written.
			if i+1 < len(v) && strings.IndexByte("nN^'\"\n", v[i+1]) >= 0 {
				buf = append(buf, "^^"...)
			} else {
				buf = append(buf, '^')
			}
		case '\n':
			buf = append(buf, "^n"...)
		case '"':
			buf = append(buf, "^'"...)
		default:
			buf = append(buf, v[i])
		}
	}
	if quote {
		buf = append(buf, '"')
	}
	return buf
}

// appendFolded writes line, breaking it into chunks of at most 75 octets with
// continuation lines starting with a space. It does not split a UTF-8
// sequence.
func appendFolded(buf, line []byte) []byte {
	limit := maxLine
	for len(line) > limit {
		cut := limit
		// Back off to a rune boundary; at most three continuation bytes.
		for k := 0; k < 3 && cut > 1 && isContinuation(line[cut]); k++ {
			cut--
		}
		buf = append(buf, line[:cut]...)
		buf = append(buf, "\r\n "...)
		line = line[cut:]
		limit = maxLine - 1
	}
	buf = append(buf, line...)
	return append(buf, "\r\n"...)
}

func isContinuation(b byte) bool { return b&0xC0 == 0x80 }

// appendSoftBroken writes a quoted-printable property: the value is broken
// with "=" soft line breaks, never inside an "=XX" escape, and continuation
// lines are not indented.
func appendSoftBroken(buf, prefix []byte, value string) []byte {
	buf = append(buf, prefix...)
	col := len(prefix)
	for len(value) > 0 {
		room := maxLine - col
		if room < 4 {
			room = 4
		}
		if len(value) <= room {
			buf = append(buf, value...)
			break
		}
		cut := room - 1 // leave room for the "="
		if i := strings.LastIndexByte(value[:cut], '='); i >= 0 && i > cut-3 {
			cut = i
		}
		if cut == 0 {
			cut = min(3, len(value))
		}
		buf = append(buf, value[:cut]...)
		buf = append(buf, "=\r\n"...)
		value = value[cut:]
		col = 0
	}
	return append(buf, "\r\n"...)
}

// isUTF16 reports whether input starting with head is UTF-16: a byte-order
// mark, or a NUL in the first two bytes, which no 8-bit content line has
// since every one starts with a letter.
func isUTF16(head []byte) bool {
	return head[0] == 0xFF && head[1] == 0xFE || head[0] == 0xFE && head[1] == 0xFF ||
		head[0] == 0 || head[1] == 0
}

// decodeUTF16 converts UTF-16 to UTF-8, honouring a byte-order mark and
// otherwise inferring the byte order from where the NUL falls.
func decodeUTF16(b []byte) string {
	order := binary.ByteOrder(binary.LittleEndian)
	start := 0
	switch {
	case b[0] == 0xFE && b[1] == 0xFF:
		order, start = binary.BigEndian, 2
	case b[0] == 0xFF && b[1] == 0xFE:
		start = 2
	case b[0] == 0:
		order = binary.BigEndian
	}
	u := make([]uint16, 0, len(b)/2)
	for i := start; i+1 < len(b); i += 2 {
		u = append(u, order.Uint16(b[i:]))
	}
	return string(utf16.Decode(u))
}
