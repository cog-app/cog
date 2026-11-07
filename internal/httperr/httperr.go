// Package httperr answers requests whose handling failed. The client gets a
// generic message and the error goes to the log with the request that hit
// it: an error's text names paths, tables and hosts the client has no
// business learning, and the log is where someone fixing it looks.
package httperr

import (
	"errors"
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

// A StatusCoder is an error that says what status a request it failed is
// answered with: a client's mistake, such as a wrong password, rather than
// the server's. An error that does not say is the server's, a 500.
type StatusCoder interface {
	error
	StatusCode() int
}

// WithStatus wraps err as one answered with code. errors.Is and errors.As
// still see err, so a sentinel made with it compares as before.
func WithStatus(code int, err error) error {
	return statusError{code: code, err: err}
}

type statusError struct {
	code int
	err  error
}

func (e statusError) Error() string   { return e.err.Error() }
func (e statusError) Unwrap() error   { return e.err }
func (e statusError) StatusCode() int { return e.code }

// Status returns the status err asks for: the first StatusCoder's in its
// chain, or 500 when none says.
func Status(err error) int {
	if sc, ok := errors.AsType[StatusCoder](err); ok {
		return sc.StatusCode()
	}
	return http.StatusInternalServerError
}
