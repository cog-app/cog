package safehttp

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// The proxy holds a program's own connections to the dial policy: a tunnel
// or a plain request to a private address is refused, and only proxy
// requests are served at all.
func TestProxyRefusesPrivateAddresses(t *testing.T) {
	p := &Proxy{}
	t.Cleanup(func() { p.Close() })
	addr, err := p.Addr()
	require.NoError(t, err)
	proxyURL, err := url.Parse(addr)
	require.NoError(t, err)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	resp, err := client.Get("http://10.0.0.1/metadata")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	_, err = client.Get("https://169.254.169.254/latest/meta-data/")
	require.True(t, err != nil && strings.Contains(err.Error(), "Forbidden"), "got %v", err)

	// Not a proxy request: nothing to forward.
	direct, err := http.Get(addr + "/x")
	require.NoError(t, err)
	direct.Body.Close()
	require.Equal(t, http.StatusBadRequest, direct.StatusCode)
}
