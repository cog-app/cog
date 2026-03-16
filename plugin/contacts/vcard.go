package contacts

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TypedValue holds a value with an optional type label (e.g. "WORK", "HOME").
type TypedValue struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Address holds a structured postal address.
type Address struct {
	Type    string `json:"type"`
	Street  string `json:"street"`
	City    string `json:"city"`
	State   string `json:"state"`
	Zip     string `json:"zip"`
	Country string `json:"country"`
}

// VCardFields holds extracted fields from a vCard.
type VCardFields struct {
	UID           string
	FN            string // formatted name
	Email         string // primary email (first)
	Tel           string // primary telephone (first)
	Org           string
	Title         string
	Note          string
	Emails        []TypedValue
	Phones        []TypedValue
	Addresses     []Address
	EmailsJSON    string // serialized JSON
	PhonesJSON    string // serialized JSON
	AddressesJSON string // serialized JSON
	Rev           string // revision timestamp
}

// MarshalJSONFields serializes Emails, Phones, and Addresses into their JSON string fields.
func (f *VCardFields) MarshalJSONFields() {
	if data, err := json.Marshal(f.Emails); err == nil {
		f.EmailsJSON = string(data)
	} else {
		f.EmailsJSON = "[]"
	}
	if data, err := json.Marshal(f.Phones); err == nil {
		f.PhonesJSON = string(data)
	} else {
		f.PhonesJSON = "[]"
	}
	if data, err := json.Marshal(f.Addresses); err == nil {
		f.AddressesJSON = string(data)
	} else {
		f.AddressesJSON = "[]"
	}
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
		propName, params, _ := strings.Cut(propFull, ";")

		switch strings.ToUpper(propName) {
		case "UID":
			f.UID = value
		case "FN":
			f.FN = unescapeVCard(value)
		case "EMAIL":
			typ := extractTypeParam(params)
			if typ == "" {
				typ = "INTERNET"
			}
			f.Emails = append(f.Emails, TypedValue{Type: typ, Value: value})
			if f.Email == "" {
				f.Email = value
			}
		case "TEL":
			typ := extractTypeParam(params)
			if typ == "" {
				typ = "CELL"
			}
			f.Phones = append(f.Phones, TypedValue{Type: typ, Value: value})
			if f.Tel == "" {
				f.Tel = value
			}
		case "ORG":
			f.Org = unescapeVCard(value)
		case "TITLE", "ROLE":
			if f.Title == "" {
				f.Title = unescapeVCard(value)
			}
		case "NOTE":
			f.Note = unescapeVCard(value)
		case "ADR":
			typ := extractTypeParam(params)
			if typ == "" {
				typ = "HOME"
			}
			f.Addresses = append(f.Addresses, parseADR(typ, value))
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

	f.MarshalJSONFields()
	return f, nil
}

// extractTypeParam extracts the first TYPE= value from vCard property parameters.
func extractTypeParam(params string) string {
	if params == "" {
		return ""
	}
	for part := range strings.SplitSeq(params, ";") {
		upper := strings.ToUpper(part)
		if strings.HasPrefix(upper, "TYPE=") {
			val := part[5:]
			// TYPE=WORK,VOICE => take first
			if idx := strings.IndexByte(val, ','); idx >= 0 {
				val = val[:idx]
			}
			return strings.ToUpper(val)
		}
		// Bare type labels like ;WORK; are also valid
		switch upper {
		case "HOME", "WORK", "CELL", "VOICE", "FAX", "PAGER", "INTERNET", "PREF":
			return upper
		}
	}
	return ""
}

// parseADR splits ADR value (;-separated: PO;ext;street;city;state;zip;country).
func parseADR(typ, value string) Address {
	parts := strings.Split(value, ";")
	get := func(i int) string {
		if i < len(parts) {
			return unescapeVCard(parts[i])
		}
		return ""
	}
	return Address{
		Type:    typ,
		Street:  get(2),
		City:    get(3),
		State:   get(4),
		Zip:     get(5),
		Country: get(6),
	}
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

// GenerateVCard produces a minimal vCard 3.0 blob (backward-compatible wrapper).
func GenerateVCard(uid, fn, email, tel string, now time.Time) string {
	f := VCardFields{UID: uid, FN: fn, Email: email, Tel: tel}
	if email != "" {
		f.Emails = []TypedValue{{Type: "INTERNET", Value: email}}
	}
	if tel != "" {
		f.Phones = []TypedValue{{Type: "CELL", Value: tel}}
	}
	return GenerateVCardFull(f, now)
}

// GenerateVCardFull produces a vCard 3.0 blob with all supported fields.
func GenerateVCardFull(fields VCardFields, now time.Time) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\n")
	b.WriteString("VERSION:3.0\r\n")
	fmt.Fprintf(&b, "UID:%s\r\n", fields.UID)
	fmt.Fprintf(&b, "FN:%s\r\n", escapeVCard(fields.FN))
	for _, e := range fields.Emails {
		typ := e.Type
		if typ == "" {
			typ = "INTERNET"
		}
		fmt.Fprintf(&b, "EMAIL;TYPE=%s:%s\r\n", typ, e.Value)
	}
	if len(fields.Emails) == 0 && fields.Email != "" {
		fmt.Fprintf(&b, "EMAIL;TYPE=INTERNET:%s\r\n", fields.Email)
	}
	for _, p := range fields.Phones {
		typ := p.Type
		if typ == "" {
			typ = "CELL"
		}
		fmt.Fprintf(&b, "TEL;TYPE=%s:%s\r\n", typ, p.Value)
	}
	if len(fields.Phones) == 0 && fields.Tel != "" {
		fmt.Fprintf(&b, "TEL;TYPE=CELL:%s\r\n", fields.Tel)
	}
	if fields.Org != "" {
		fmt.Fprintf(&b, "ORG:%s\r\n", escapeVCard(fields.Org))
	}
	if fields.Title != "" {
		fmt.Fprintf(&b, "TITLE:%s\r\n", escapeVCard(fields.Title))
	}
	if fields.Note != "" {
		fmt.Fprintf(&b, "NOTE:%s\r\n", escapeVCard(fields.Note))
	}
	for _, a := range fields.Addresses {
		typ := a.Type
		if typ == "" {
			typ = "HOME"
		}
		fmt.Fprintf(&b, "ADR;TYPE=%s:;;%s;%s;%s;%s;%s\r\n", typ,
			escapeVCard(a.Street), escapeVCard(a.City),
			escapeVCard(a.State), escapeVCard(a.Zip), escapeVCard(a.Country))
	}
	fmt.Fprintf(&b, "REV:%s\r\n", now.UTC().Format("20060102T150405Z"))
	b.WriteString("END:VCARD\r\n")
	return b.String()
}

// ParseEmailsJSON deserializes a JSON array of TypedValue from a string.
func ParseEmailsJSON(jsonStr string) []TypedValue {
	var result []TypedValue
	if jsonStr == "" || jsonStr == "[]" {
		return nil
	}
	json.Unmarshal([]byte(jsonStr), &result)
	return result
}

// ParsePhonesJSON deserializes a JSON array of TypedValue from a string.
func ParsePhonesJSON(jsonStr string) []TypedValue {
	var result []TypedValue
	if jsonStr == "" || jsonStr == "[]" {
		return nil
	}
	json.Unmarshal([]byte(jsonStr), &result)
	return result
}

// ParseAddressesJSON deserializes a JSON array of Address from a string.
func ParseAddressesJSON(jsonStr string) []Address {
	var result []Address
	if jsonStr == "" || jsonStr == "[]" {
		return nil
	}
	json.Unmarshal([]byte(jsonStr), &result)
	return result
}
