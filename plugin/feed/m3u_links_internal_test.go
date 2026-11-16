package feed

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

type baseURLContext struct {
	plugin.PluginContext
	base string
}

func (c baseURLContext) BaseURL() string { return c.base }

func TestParseM3UPath(t *testing.T) {
	for _, c := range []struct {
		kind, file string
		want       m3uList
		ok         bool
	}{
		{"playlist", "7.m3u", m3uList{ViewPlaylist, "7"}, true},
		{"folder", "3.m3u", m3uList{ViewFolder, "3"}, true},
		{"sub", "12.m3u", m3uList{ViewSubscription, "12"}, true},
		{"tag", "a b/c%d é.m3u.m3u", m3uList{ViewTag, "a b/c%d é.m3u"}, true},
		{"starred", "all.m3u", m3uList{ViewStarred, "all"}, true},
		{"unread", "all.m3u", m3uList{ViewUnread, "all"}, true},
		{"playlist", "7.m3u8", m3uList{}, false},
		{"playlist", "x.m3u", m3uList{}, false},
		{"playlist", "-1.m3u", m3uList{}, false},
		{"starred", "1.m3u", m3uList{}, false},
		{"history", "all.m3u", m3uList{}, false},
		{"all", "all.m3u", m3uList{}, false},
		{"tag", ".m3u", m3uList{}, false},
	} {
		got, err := parseM3UPath(c.kind, c.file)
		assert.Equal(t, c.ok, err == nil, "%s/%s: %v", c.kind, c.file, err)
		if c.ok {
			assert.Equal(t, c.want, got)
		}
	}
	l := m3uList{ViewTag, "a b/c%d é.m3u"}
	p := l.path()
	kind, file, _ := strings.Cut(p, "/")
	file, err := url.PathUnescape(file)
	require.NoError(t, err)
	got, err := parseM3UPath(kind, file)
	require.NoError(t, err)
	assert.Equal(t, l, got)
}

func TestM3UListView(t *testing.T) {
	v, err := m3uList{ViewPlaylist, "7"}.View()
	require.NoError(t, err)
	assert.Equal(t, View{Kind: ViewPlaylist, ID: 7}, v)
	v, err = m3uList{ViewTag, "x y"}.View()
	require.NoError(t, err)
	assert.Equal(t, View{Kind: ViewTag, Tag: "x y"}, v)
	v, err = m3uList{ViewStarred, "all"}.View()
	require.NoError(t, err)
	assert.Equal(t, View{Kind: ViewStarred}, v)
}

func TestM3USignatureScopes(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	pl := m3uList{ViewPlaylist, "7"}
	sig := m3uSignature(key, "alice", pl, hlsOptions{})
	assert.True(t, validM3USignature(key, "alice", pl, hlsOptions{}, sig))
	assert.False(t, validM3USignature(key, "bob", pl, hlsOptions{}, sig))
	assert.False(t, validM3USignature(key, "alice", m3uList{ViewPlaylist, "8"}, hlsOptions{}, sig))
	assert.False(t, validM3USignature(key, "alice", m3uList{ViewFolder, "7"}, hlsOptions{}, sig))
	assert.False(t, validM3USignature(key, "alice", pl, hlsOptions{Skip: true}, sig))
	assert.False(t, validM3USignature(bytes.Repeat([]byte{2}, 32), "alice", pl, hlsOptions{}, sig))
	assert.NotEqual(t, hlsSignature(key, "alice", 7, hlsOptions{}), sig)
	// edit is unsigned: it never changes an M3U signature
	assert.Equal(t, m3uSignature(key, "alice", pl, hlsOptions{Skip: true}),
		m3uSignature(key, "alice", pl, hlsOptions{Skip: true, Edit: "0123456789ab"}))
}

func TestAbsoluteURL(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Host = "h:8580"
	p := &Plugin{ctx: baseURLContext{base: "https://cog.example"}}
	assert.Equal(t, "https://cog.example/apps/feed/x", p.absoluteURL(r, "/apps/feed/x"))
	p = &Plugin{ctx: baseURLContext{}}
	assert.Equal(t, "http://h:8580/apps/feed/x", p.absoluteURL(r, "/apps/feed/x"))
}
