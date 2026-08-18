package plugcmd

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/plugpb"
)

// stubRegistrar is a PluginContext that only answers PluginDB.
type stubRegistrar struct {
	cogplugin.PluginContext
	asked string
	err   error
}

func (s *stubRegistrar) PluginDB(username string, _ ...cogplugin.DBOption) (*sql.DB, error) {
	s.asked = username
	return nil, s.err
}

func TestRequestContext_CarriesUserAndToken(t *testing.T) {
	s := &pluginServer{}
	reg := &stubRegistrar{err: errors.New("no database")}

	ctx := s.requestContext(context.Background(), &plugpb.HTTPRequest{
		UserId:      "u-1",
		Username:    "alice",
		DisplayName: "Alice A",
		IsAdmin:     true,
		CsrfToken:   "tok-123",
	}, reg)

	u := cogplugin.UserFromContext(ctx)
	if u == nil {
		t.Fatal("no user in context")
	}
	if u.ID != "u-1" || u.Username != "alice" || u.DisplayName != "Alice A" || !u.IsAdmin {
		t.Errorf("user = %+v, want the whole identity from the request", u)
	}
	if got := cogplugin.CSRFTokenFromContext(ctx); got != "tok-123" {
		t.Errorf("csrf token = %q, want %q", got, "tok-123")
	}
}

// The provider must reach the registrar the plugin registered with — that is
// what decides whether this plugin has database access at all.
func TestRequestContext_DBProviderReachesRegistrar(t *testing.T) {
	s := &pluginServer{}
	want := errors.New("no database for you")
	reg := &stubRegistrar{err: want}

	ctx := s.requestContext(context.Background(), &plugpb.HTTPRequest{
		Username: "alice",
	}, reg)

	r := requestWithContext(t, ctx)
	_, err := cogplugin.UserDB(r)
	if !errors.Is(err, want) {
		t.Fatalf("UserDB error = %v, want %v", err, want)
	}
	if reg.asked != "alice" {
		t.Errorf("registrar asked for %q, want %q", reg.asked, "alice")
	}
}

// An unauthenticated request carries no user, and UserDB must say so rather
// than dereference one.
func TestRequestContext_Unauthenticated(t *testing.T) {
	s := &pluginServer{}
	ctx := s.requestContext(context.Background(), &plugpb.HTTPRequest{}, &stubRegistrar{})

	if u := cogplugin.UserFromContext(ctx); u != nil {
		t.Fatalf("user = %+v, want none", u)
	}
	if _, err := cogplugin.UserDB(requestWithContext(t, ctx)); err == nil {
		t.Fatal("UserDB error = nil, want a missing-user error")
	}
}

// A Register that has not run yet leaves no registrar, and a request arriving
// then must still not panic.
func TestRequestContext_NoRegistrar(t *testing.T) {
	s := &pluginServer{}
	ctx := s.requestContext(context.Background(), &plugpb.HTTPRequest{Username: "alice"}, nil)

	if _, err := cogplugin.UserDB(requestWithContext(t, ctx)); err == nil {
		t.Fatal("UserDB error = nil, want a missing-provider error")
	}
}

func requestWithContext(t *testing.T, ctx context.Context) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "/apps/x/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return r
}
