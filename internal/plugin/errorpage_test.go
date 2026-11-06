package plugin

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testErrorPlugin(name string) *grpcPlugin {
	return &grpcPlugin{
		id:   "recipes",
		log:  slog.New(slog.DiscardHandler),
		name: func() string { return name },
	}
}

func TestErrorPage_Unavailable(t *testing.T) {
	g := testErrorPlugin("Recipes")
	rec := httptest.NewRecorder()
	g.errorPage(rec, httptest.NewRequest(http.MethodGet, "/apps/recipes/list", nil),
		http.StatusServiceUnavailable)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "5", rec.Header().Get("Retry-After"))

	body := rec.Body.String()
	assert.Contains(t, body, "Recipes is temporarily unavailable")
	assert.Contains(t, body, "being restarted")

	// The page is a whole document — the shell passes 300-and-over straight
	// through, so nothing else will wrap it — and it shares the app stylesheet.
	assert.Contains(t, body, "<!doctype html>")
	assert.Contains(t, body, "/static/style.css")

	// A GET can be repeated, so the failed path is offered as a retry.
	assert.Contains(t, body, "Try again")
	assert.Contains(t, body, `href="/apps/recipes/list"`)
	assert.Contains(t, body, "Back to Cog")
}

// Repeating a POST would resubmit whatever the user just did, so a failure on
// one offers no retry link.
func TestErrorPage_NoRetryForUnsafeMethod(t *testing.T) {
	g := testErrorPlugin("Recipes")
	rec := httptest.NewRecorder()
	g.errorPage(rec, httptest.NewRequest(http.MethodPost, "/apps/recipes/add", nil),
		http.StatusServiceUnavailable)

	body := rec.Body.String()
	assert.Contains(t, body, "Recipes is temporarily unavailable")
	assert.NotContains(t, body, "Try again")
	assert.NotContains(t, body, `href="/apps/recipes/add"`)
	assert.Contains(t, body, "Back to Cog")
}

func TestErrorPage_InternalError(t *testing.T) {
	g := testErrorPlugin("Recipes")
	rec := httptest.NewRecorder()
	g.errorPage(rec, httptest.NewRequest(http.MethodGet, "/apps/recipes/list", nil),
		http.StatusInternalServerError)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, rec.Header().Get("Retry-After"))
	assert.Contains(t, rec.Body.String(), "Recipes hit an error")
}

// A plugin that registered no nav item has no display name, and its ID is the
// only thing left to call it.
func TestErrorPage_FallsBackToPluginID(t *testing.T) {
	g := testErrorPlugin("")
	rec := httptest.NewRecorder()
	g.errorPage(rec, httptest.NewRequest(http.MethodGet, "/apps/recipes/", nil),
		http.StatusServiceUnavailable)

	assert.Contains(t, rec.Body.String(), "recipes is temporarily unavailable")
}

// Whatever the failure, the user never sees the socket path or the gRPC status
// that produced it.
func TestErrorPage_LeaksNoTransportDetail(t *testing.T) {
	for _, code := range []int{
		http.StatusServiceUnavailable,
		http.StatusInternalServerError,
		http.StatusBadRequest,
	} {
		g := testErrorPlugin("Recipes")
		rec := httptest.NewRecorder()
		g.errorPage(rec, httptest.NewRequest(http.MethodGet, "/apps/recipes/", nil), code)

		body := rec.Body.String()
		assert.NotContains(t, body, "rpc error")
		assert.NotContains(t, body, "plugin.sock")
		assert.NotContains(t, body, "connection refused")
	}
}

// A call canceled by the host closing a reaped process's connection is an
// outage; one canceled because the browser went away is not.
func TestStatusForRPCError_Canceled(t *testing.T) {
	closing := status.Error(codes.Canceled, "grpc: the client connection is closing")
	assert.Equal(t, http.StatusServiceUnavailable, statusForRPCError(context.Background(), closing))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Equal(t, http.StatusInternalServerError, statusForRPCError(ctx, closing))
}
