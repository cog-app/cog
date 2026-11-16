package feed

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestHLSOptionsSignature(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	// A phase-1 link: the message is unchanged when there are no options.
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("cog-feed-hls\x00alice\x0042"))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), hlsSignature(key, "alice", 42, hlsOptions{}))
	assert.NotEqual(t, hlsSignature(key, "alice", 42, hlsOptions{}), hlsSignature(key, "alice", 42, hlsOptions{Skip: true}))
	// The edit list is outside the signature.
	assert.Equal(t, hlsSignature(key, "alice", 42, hlsOptions{Skip: true}), hlsSignature(key, "alice", 42, hlsOptions{Skip: true, Edit: "abcdefabcdef"}))
	sig := hlsSignature(key, "alice", 42, hlsOptions{Skip: true})
	assert.True(t, validHLSSignature(key, "alice", 42, hlsOptions{Skip: true}, sig))
	assert.False(t, validHLSSignature(key, "alice", 42, hlsOptions{}, sig))
}

func TestParseHLSOptions(t *testing.T) {
	o, err := parseHLSOptions(url.Values{"sig": {"x"}, "skip": {"silence"}})
	require.NoError(t, err)
	assert.True(t, o.Skip)
	o, err = parseHLSOptions(url.Values{"sig": {"x"}, "skip": {"silence"}, "edit": {"abcdef012345"}})
	require.NoError(t, err)
	assert.Equal(t, "abcdef012345", o.Edit)
	assert.Equal(t, "skip=silence", o.Values().Encode())
	for _, bad := range []url.Values{
		{"skip": {"pauses"}}, {"skip": {"silence", "silence"}}, {"mute": {"profanity"}}, {"sig": {"a", "b"}},
		{"edit": {"zz"}}, {"edit": {"abcdefabcdef"}},
		{"skip": {"silence"}, "edit": {"ABCDEFABCDEF"}}, {"skip": {"silence"}, "edit": {"abcdef"}},
	} {
		_, err := parseHLSOptions(bad)
		assert.Error(t, err, "%v", bad)
	}
	o, err = parseHLSOptions(url.Values{"sig": {"x"}})
	require.NoError(t, err)
	assert.Equal(t, hlsOptions{}, o)
	assert.Equal(t, 0, len(o.Values()))
}
