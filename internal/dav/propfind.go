package dav

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"slices"
)

// propfind is a parsed PROPFIND body (RFC 4918 §9.1), or the DAV:prop of a
// REPORT.
type propfind struct {
	all     bool       // DAV:allprop, or an empty body
	names   bool       // DAV:propname
	props   []xml.Name // DAV:prop
	include []xml.Name // DAV:include with allprop
}

func (q *request) propfindBody() (*propfind, error) {
	n, err := readXML(q.r)
	if errors.Is(err, errEmptyBody) {
		return &propfind{all: true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("invalid XML: %w", err)
	}
	if !n.is(dav("propfind")) {
		return nil, fmt.Errorf("expected DAV:propfind, got %s", n.Name.Local)
	}
	pf := &propfind{}
	switch {
	case n.child(dav("propname")) != nil:
		pf.names = true
	case n.child(dav("prop")) != nil:
		pf.props = n.child(dav("prop")).propNames()
	default:
		pf.all = true
		pf.include = n.child(dav("include")).propNames()
	}
	return pf, nil
}

// propsFrom reads the DAV:prop of a REPORT body; with none, it asks for every
// property.
func propsFrom(n *node) *propfind {
	if p := n.child(dav("prop")); p != nil {
		return &propfind{props: p.propNames()}
	}
	if n.child(dav("allprop")) != nil {
		return &propfind{all: true}
	}
	return &propfind{props: []xml.Name{dav("getetag")}}
}

// wants reports whether the request asks for name explicitly.
func (pf *propfind) wants(name xml.Name) bool {
	return slices.Contains(pf.props, name) || slices.Contains(pf.include, name)
}

// evaluate answers the request from a resource's live properties. extra
// holds properties returned only when named, never for allprop: costly ones
// such as calendar-data.
func (pf *propfind) evaluate(all, extra []prop) []propstat {
	switch {
	case pf.names:
		names := make([]prop, len(all))
		for i, p := range all {
			names[i] = prop{Name: p.Name}
		}
		return []propstat{{Status: http.StatusOK, Props: names}}
	case pf.all:
		found := slices.Clone(all)
		for _, n := range pf.include {
			if i := slices.IndexFunc(extra, func(p prop) bool { return p.Name == n }); i >= 0 {
				found = append(found, extra[i])
			}
		}
		return []propstat{{Status: http.StatusOK, Props: found}}
	}
	var found, missing []prop
	for _, n := range pf.props {
		i := slices.IndexFunc(all, func(p prop) bool { return p.Name == n })
		if i >= 0 {
			found = append(found, all[i])
			continue
		}
		if i := slices.IndexFunc(extra, func(p prop) bool { return p.Name == n }); i >= 0 {
			found = append(found, extra[i])
			continue
		}
		missing = append(missing, prop{Name: n})
	}
	var out []propstat
	if len(found) > 0 {
		out = append(out, propstat{Status: http.StatusOK, Props: found})
	}
	if len(missing) > 0 {
		out = append(out, propstat{Status: http.StatusNotFound, Props: missing})
	}
	if len(out) == 0 {
		out = append(out, propstat{Status: http.StatusOK})
	}
	return out
}
