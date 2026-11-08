package cog

import (
	"net"
	"net/http"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestServerSpeaksCleartextHTTP2: gRPC needs HTTP/2, and Cog serves plain
// HTTP behind a proxy, so a client that knows it is talking HTTP/2 must be
// answered in it without TLS — on the same port HTTP/1 clients use.
func TestServerSpeaksCleartextHTTP2(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := newServer("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Proto))
	}))
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	for _, tc := range []struct {
		name  string
		proto func(*http.Protocols)
		want  string
	}{
		{"h2c", func(p *http.Protocols) { p.SetUnencryptedHTTP2(true) }, "HTTP/2.0"},
		{"http1", func(p *http.Protocols) { p.SetHTTP1(true) }, "HTTP/1.1"},
	} {
		var protocols http.Protocols
		tc.proto(&protocols)
		client := &http.Client{Transport: &http.Transport{Protocols: &protocols}}
		res, err := client.Get("http://" + ln.Addr().String() + "/")
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.want, res.Proto, tc.name)
		res.Body.Close()
	}
}
