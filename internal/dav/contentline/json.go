package contentline

import (
	"encoding/base64"
	"encoding/json"
	"unicode/utf8"
)

// Codecs store leftovers as JSON. encoding/json replaces bytes that are not
// UTF-8 with U+FFFD, but clients send text in other charsets (vCard 2.1's
// CHARSET=SHIFT_JIS, Latin-1 exports), and leftovers must be written back
// byte for byte. So text is written as a JSON string when it is UTF-8, and
// as {"b64": "..."} when it is not.

// jsonText is a string with that encoding.
type jsonText string

func (s jsonText) MarshalJSON() ([]byte, error) {
	if utf8.ValidString(string(s)) {
		return json.Marshal(string(s))
	}
	return json.Marshal(struct {
		B64 string `json:"b64"`
	}{base64.StdEncoding.EncodeToString([]byte(s))})
}

func (s *jsonText) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '{' {
		var raw struct {
			B64 string `json:"b64"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
		data, err := base64.StdEncoding.DecodeString(raw.B64)
		if err != nil {
			return err
		}
		*s = jsonText(data)
		return nil
	}
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = jsonText(v)
	return nil
}

type propertyJSON struct {
	Group  jsonText `json:"group,omitempty"`
	Name   jsonText `json:"name,omitempty"`
	Params []Param  `json:"params,omitempty"`
	Value  jsonText `json:"value"`
}

// MarshalJSON writes the property with its text byte for byte.
func (p Property) MarshalJSON() ([]byte, error) {
	return json.Marshal(propertyJSON{jsonText(p.Group), jsonText(p.Name), p.Params, jsonText(p.Value)})
}

// UnmarshalJSON reads what MarshalJSON writes.
func (p *Property) UnmarshalJSON(b []byte) error {
	var v propertyJSON
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = Property{Group: string(v.Group), Name: string(v.Name), Params: v.Params, Value: string(v.Value)}
	return nil
}

type paramJSON struct {
	Name   jsonText   `json:"name,omitempty"`
	Values []jsonText `json:"values"`
}

// MarshalJSON writes the parameter with its text byte for byte.
func (p Param) MarshalJSON() ([]byte, error) {
	v := paramJSON{Name: jsonText(p.Name)}
	if p.Values != nil {
		v.Values = make([]jsonText, len(p.Values))
		for i, s := range p.Values {
			v.Values[i] = jsonText(s)
		}
	}
	return json.Marshal(v)
}

// UnmarshalJSON reads what MarshalJSON writes.
func (p *Param) UnmarshalJSON(b []byte) error {
	var v paramJSON
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = Param{Name: string(v.Name)}
	if v.Values != nil {
		p.Values = make([]string, len(v.Values))
		for i, s := range v.Values {
			p.Values[i] = string(s)
		}
	}
	return nil
}
