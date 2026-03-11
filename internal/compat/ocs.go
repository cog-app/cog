package compat

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"reflect"
	"sort"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// OCSResponse wraps data in the standard OCS envelope.
type OCSResponse struct {
	OCS struct {
		Meta struct {
			Status     string `json:"status"`
			StatusCode int    `json:"statuscode"`
			Message    string `json:"message"`
		} `json:"meta"`
		Data any `json:"data"`
	} `json:"ocs"`
}

func wantsJSON(r *http.Request) bool {
	return r.URL.Query().Get("format") == "json"
}

func writeOCSReq(w http.ResponseWriter, r *http.Request, statusCode int, data any) {
	if wantsJSON(r) {
		writeOCSJSON(w, statusCode, data)
	} else {
		writeOCSXML(w, statusCode, "ok", statusCode, "OK", data)
	}
}

func writeOCSErrorReq(w http.ResponseWriter, r *http.Request, statusCode int, ocsCode int, message string) {
	if wantsJSON(r) {
		writeOCSErrorJSON(w, statusCode, ocsCode, message)
	} else {
		writeOCSXML(w, statusCode, "failure", ocsCode, message, nil)
	}
}

func writeOCS(w http.ResponseWriter, statusCode int, data any) {
	writeOCSJSON(w, statusCode, data)
}

func writeOCSError(w http.ResponseWriter, statusCode int, ocsCode int, message string) {
	writeOCSErrorJSON(w, statusCode, ocsCode, message)
}

func writeOCSJSON(w http.ResponseWriter, statusCode int, data any) {
	resp := OCSResponse{}
	resp.OCS.Meta.Status = "ok"
	resp.OCS.Meta.StatusCode = statusCode
	resp.OCS.Meta.Message = "OK"
	resp.OCS.Data = data

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(resp)
}

func writeOCSErrorJSON(w http.ResponseWriter, statusCode int, ocsCode int, message string) {
	resp := OCSResponse{}
	resp.OCS.Meta.Status = "failure"
	resp.OCS.Meta.StatusCode = ocsCode
	resp.OCS.Meta.Message = message
	resp.OCS.Data = nil

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(resp)
}

func writeOCSXML(w http.ResponseWriter, httpCode int, status string, statusCode int, message string, data any) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(httpCode)
	fmt.Fprint(w, xml.Header)
	fmt.Fprint(w, "<ocs>\n <meta>\n")
	fmt.Fprintf(w, "  <status>%s</status>\n", status)
	fmt.Fprintf(w, "  <statuscode>%d</statuscode>\n", statusCode)
	fmt.Fprintf(w, "  <message>%s</message>\n", xmlEscape(message))
	fmt.Fprint(w, " </meta>\n <data>\n")
	if data != nil {
		writeOCSXMLValue(w, data, "  ")
	}
	fmt.Fprint(w, " </data>\n</ocs>\n")
}

func writeOCSXMLValue(w http.ResponseWriter, v any, indent string) {
	if v == nil {
		return
	}
	val := reflect.ValueOf(v)
	switch val.Kind() {
	case reflect.Slice:
		for i := 0; i < val.Len(); i++ {
			fmt.Fprintf(w, "%s<element>\n", indent)
			writeOCSXMLValue(w, val.Index(i).Interface(), indent+" ")
			fmt.Fprintf(w, "%s</element>\n", indent)
		}
	case reflect.Map:
		keys := make([]string, 0, val.Len())
		for _, k := range val.MapKeys() {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		for _, k := range keys {
			mv := val.MapIndex(reflect.ValueOf(k))
			elem := mv.Interface()
			if elem == nil {
				fmt.Fprintf(w, "%s<%s/>\n", indent, k)
				continue
			}
			ev := reflect.ValueOf(elem)
			if ev.Kind() == reflect.Slice || ev.Kind() == reflect.Map {
				fmt.Fprintf(w, "%s<%s>\n", indent, k)
				writeOCSXMLValue(w, elem, indent+" ")
				fmt.Fprintf(w, "%s</%s>\n", indent, k)
			} else {
				fmt.Fprintf(w, "%s<%s>%s</%s>\n", indent, k, xmlEscape(fmt.Sprint(elem)), k)
			}
		}
	case reflect.Struct:
		writeOCSXMLStruct(w, val, indent)
	case reflect.Pointer:
		if val.IsNil() {
			return
		}
		writeOCSXMLValue(w, val.Elem().Interface(), indent)
	default:
		fmt.Fprintf(w, "%s%s\n", indent, xmlEscape(fmt.Sprint(v)))
	}
}

func writeOCSXMLStruct(w http.ResponseWriter, val reflect.Value, indent string) {
	t := val.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		// Parse tag name (strip ",omitempty" etc.)
		name := tag
		if idx := len(tag); idx > 0 {
			if comma := indexOf(tag, ','); comma >= 0 {
				name = tag[:comma]
			}
		}
		fv := val.Field(i)
		if fv.Kind() == reflect.Pointer && fv.IsNil() {
			fmt.Fprintf(w, "%s<%s/>\n", indent, name)
			continue
		}
		iface := fv.Interface()
		rv := reflect.ValueOf(iface)
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map || rv.Kind() == reflect.Struct {
			fmt.Fprintf(w, "%s<%s>\n", indent, name)
			writeOCSXMLValue(w, iface, indent+" ")
			fmt.Fprintf(w, "%s</%s>\n", indent, name)
		} else if rv.Kind() == reflect.Pointer {
			fmt.Fprintf(w, "%s<%s>%s</%s>\n", indent, name, xmlEscape(fmt.Sprint(rv.Elem().Interface())), name)
		} else {
			fmt.Fprintf(w, "%s<%s>%s</%s>\n", indent, name, xmlEscape(fmt.Sprint(iface)), name)
		}
	}
}

func indexOf(s string, b byte) int {
	for i := range len(s) {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// HandleCapabilities returns a static capabilities response.
func HandleCapabilities(w http.ResponseWriter, r *http.Request) {
	writeOCS(w, http.StatusOK, map[string]any{
		"version": map[string]any{
			"major":   31,
			"minor":   0,
			"micro":   0,
			"string":  "31.0.0",
			"edition": "",
		},
		"capabilities": map[string]any{
			"core": map[string]any{
				"webdav-root": "remote.php/dav",
			},
			"files": map[string]any{
				"bigfilechunking": false,
				"favorites":       true,
			},
			"news": map[string]any{
				"version": "25.0.0-alpha1",
			},
			"files_sharing": map[string]any{
				"api_enabled": true,
				"public": map[string]any{
					"enabled":     true,
					"password":    map[string]any{"enforced": false},
					"expire_date": map[string]any{"enabled": false},
					"upload":      true,
				},
				"group_sharing":       true,
				"resharing":           true,
				"default_permissions": 31,
			},
			"notes": map[string]any{
				"api_version": []string{"0.2", "1.3"},
			},
		},
	})
}

// HandleCurrentUser returns info about the authenticated user.
func HandleCurrentUser(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSError(w, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	writeOCS(w, http.StatusOK, map[string]any{
		"id":           u.ID,
		"display-name": u.Username,
		"email":        "",
	})
}

// HandleWebDAVStub responds to WebDAV presence checks (HEAD/PROPFIND/OPTIONS on /remote.php/dav).
// NC clients probe this to confirm the server is a valid NextCloud instance.
// Write methods (PUT, MKCOL, MOVE, DELETE) are rejected so that unhandled
// paths (e.g. /remote.php/dav/uploads/) don't silently swallow data.
func HandleWebDAVStub(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("DAV", "1, 3, extended-mkcol")
	switch r.Method {
	case http.MethodGet, http.MethodHead, "PROPFIND", http.MethodOptions:
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "not implemented", http.StatusMethodNotAllowed)
	}
}

// HandleUserByID returns info about a user by their ID.
// Route: GET /ocs/v2.php/cloud/users/{id}
func HandleUserByID(users *user.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("id")
		if userID == "" {
			writeOCSError(w, http.StatusBadRequest, 400, "user id required")
			return
		}

		// Try by ID first, then by username
		target, err := users.GetByID(userID)
		if err != nil {
			target, err = users.GetByUsername(userID)
		}
		if err != nil {
			writeOCSError(w, http.StatusNotFound, 404, "user not found")
			return
		}

		displayName := target.DisplayName
		if displayName == "" {
			displayName = target.Username
		}

		writeOCS(w, http.StatusOK, map[string]any{
			"id":           target.ID,
			"display-name": displayName,
			"email":        target.Email,
		})
	}
}

// HandleStatus returns the server status.
func HandleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"installed":       true,
		"maintenance":     false,
		"needsDbUpgrade":  false,
		"version":         "31.0.0.0",
		"versionstring":   "31.0.0",
		"edition":         "",
		"productname":     "Cog",
		"extendedSupport": false,
	})
}
