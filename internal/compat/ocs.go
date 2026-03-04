package compat

import (
	"encoding/json"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
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

func writeOCS(w http.ResponseWriter, statusCode int, data any) {
	resp := OCSResponse{}
	resp.OCS.Meta.Status = "ok"
	resp.OCS.Meta.StatusCode = 100
	resp.OCS.Meta.Message = "OK"
	resp.OCS.Data = data

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(resp)
}

func writeOCSError(w http.ResponseWriter, statusCode int, ocsCode int, message string) {
	resp := OCSResponse{}
	resp.OCS.Meta.Status = "failure"
	resp.OCS.Meta.StatusCode = ocsCode
	resp.OCS.Meta.Message = message
	resp.OCS.Data = nil

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(resp)
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
