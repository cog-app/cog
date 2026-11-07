// Package httperr answers requests whose handling failed. The client gets a
// generic message and the error goes to the log with the request that hit
// it: an error's text names paths, tables and hosts the client has no
// business learning, and the log is where someone fixing it looks.
package httperr

import (
	"log/slog"
	"net/http"
)

// Message is what a client is told when a request fails on the server.
const Message = "Internal error"

// Log records err against the request it failed.
func Log(r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
}

// Internal logs err and answers 500 with Message.
func Internal(w http.ResponseWriter, r *http.Request, err error) {
	Log(r, err)
	http.Error(w, Message, http.StatusInternalServerError)
}
