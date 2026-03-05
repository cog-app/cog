package contacts

import (
	"fmt"
	"strings"
	"time"
)

// VCardFields holds extracted fields from a vCard.
type VCardFields struct {
	UID   string
	FN    string // formatted name
	Email string // first email
	Tel   string // first telephone
	Rev   string // revision timestamp
}

// ParseVCard extracts fields from a vCard blob (RFC 6350).
func ParseVCard(vcardData string) (VCardFields, error) {
	// Unfold continuation lines (RFC 5545/6350 §3.2).
	vcardData = strings.ReplaceAll(vcardData, "\r\n ", "")
	vcardData = strings.ReplaceAll(vcardData, "\r\n\t", "")

	lines := strings.Split(vcardData, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}

	var inCard bool
	var f VCardFields
	found := false

	for _, line := range lines {
		if line == "BEGIN:VCARD" {
			inCard = true
			found = true
			continue
		}
		if line == "END:VCARD" {
			break
		}
		if !inCard {
			continue
		}

		propFull, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		propName, _, _ := strings.Cut(propFull, ";")

		switch strings.ToUpper(propName) {
		case "UID":
			f.UID = value
		case "FN":
			f.FN = unescapeVCard(value)
		case "EMAIL":
			if f.Email == "" {
				f.Email = value
			}
		case "TEL":
			if f.Tel == "" {
				f.Tel = value
			}
		case "REV":
			f.Rev = value
		}
	}

	if !found {
		return f, fmt.Errorf("no VCARD found in data")
	}
	if f.UID == "" {
		return f, fmt.Errorf("VCARD missing UID")
	}
	return f, nil
}

// unescapeVCard reverses vCard text escaping.
func unescapeVCard(s string) string {
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\N", "\n")
	s = strings.ReplaceAll(s, "\\,", ",")
	s = strings.ReplaceAll(s, "\\;", ";")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	return s
}

// escapeVCard applies vCard text escaping.
func escapeVCard(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

// GenerateVCard produces a minimal vCard 3.0 blob.
func GenerateVCard(uid, fn, email, tel string) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\n")
	b.WriteString("VERSION:3.0\r\n")
	fmt.Fprintf(&b, "UID:%s\r\n", uid)
	fmt.Fprintf(&b, "FN:%s\r\n", escapeVCard(fn))
	if email != "" {
		fmt.Fprintf(&b, "EMAIL;TYPE=INTERNET:%s\r\n", email)
	}
	if tel != "" {
		fmt.Fprintf(&b, "TEL;TYPE=CELL:%s\r\n", tel)
	}
	fmt.Fprintf(&b, "REV:%s\r\n", time.Now().UTC().Format("20060102T150405Z"))
	b.WriteString("END:VCARD\r\n")
	return b.String()
}
