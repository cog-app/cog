// Package httphdr builds HTTP header values from untrusted parts. Header
// values are never assembled with fmt: a name holding a quote or a line
// break would end the value or start another header.
package httphdr

import (
	"mime"
	"strings"
)

// Attachment is a Content-Disposition value that downloads a file under
// name. Control characters and path separators are dropped from name, and a
// non-ASCII name is RFC 2231 encoded. A name that cannot be encoded at all
// leaves the disposition without one, so the response still downloads
// rather than displaying inline.
func Attachment(name string) string {
	return disposition("attachment", name)
}

// Inline is Attachment for a file the browser may display.
func Inline(name string) string {
	return disposition("inline", name)
}

func disposition(kind, name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, name)
	if name == "" {
		return kind
	}
	if v := mime.FormatMediaType(kind, map[string]string{"filename": name}); v != "" {
		return v
	}
	return kind
}
