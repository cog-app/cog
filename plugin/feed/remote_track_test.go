package feed

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// rangeServer serves data with Range support and counts the requests.
func rangeServer(t *testing.T, data []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.ServeContent(w, r, "t.mp4", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func localTrack(t *testing.T, data []byte) hls.Track {
	t.Helper()
	want, err := hls.ReadTrack(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	return want
}

func assertSameTrack(t *testing.T, got, want hls.Track) {
	t.Helper()
	assert.Equal(t, got.Kind, want.Kind)
	assert.Equal(t, got.Codecs, want.Codecs)
	assert.Equal(t, got.InitLength, want.InitLength)
	assert.Equal(t, len(got.Fragments), len(want.Fragments))
}

func TestRemoteTrackOneRequest(t *testing.T) {
	data, err := os.ReadFile("testdata/media/video.mp4")
	require.NoError(t, err)
	srv, n := rangeServer(t, data)

	got, err := RemoteTrack(t.Context(), HTTP, srv.URL)
	require.NoError(t, err)
	assertSameTrack(t, got, localTrack(t, data))
	assert.Equal(t, n.Load(), int32(1))
}

func TestRemoteTrackPastHead(t *testing.T) {
	data, err := os.ReadFile("testdata/media/video.mp4")
	require.NoError(t, err)
	ftyp := binary.BigEndian.Uint32(data[:4])
	free := make([]byte, 300<<10)
	binary.BigEndian.PutUint32(free, uint32(len(free)))
	copy(free[4:], "free")
	padded := append(append(append([]byte{}, data[:ftyp]...), free...), data[ftyp:]...)
	srv, n := rangeServer(t, padded)

	got, err := RemoteTrack(t.Context(), HTTP, srv.URL)
	require.NoError(t, err)
	assertSameTrack(t, got, localTrack(t, padded))
	assert.True(t, n.Load() > 1, "requests: %d", n.Load())
}

func TestRemoteTrackFollowsRedirect(t *testing.T) {
	data, err := os.ReadFile("testdata/media/video.mp4")
	require.NoError(t, err)
	var redirected atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		if redirected.CompareAndSwap(false, true) {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		http.ServeContent(w, r, "t.mp4", time.Time{}, bytes.NewReader(data))
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "t.mp4", time.Time{}, bytes.NewReader(data))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got, err := RemoteTrack(t.Context(), HTTP, srv.URL+"/start")
	require.NoError(t, err)
	assert.True(t, redirected.Load())
	assertSameTrack(t, got, localTrack(t, data))
}

func TestRemoteTrackRefused(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusGone} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "expired", code)
		}))
		_, err := RemoteTrack(t.Context(), HTTP, srv.URL)
		srv.Close()
		assert.True(t, errors.Is(err, errRefused), "%d: %v", code, err)
	}
}

func TestRemoteTrackIgnoresRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 1024))
	}))
	t.Cleanup(srv.Close)

	_, err := RemoteTrack(t.Context(), HTTP, srv.URL)
	require.Error(t, err)
	assert.False(t, errors.Is(err, errRefused))
}

func TestRemoteHeadRefusesPrivate(t *testing.T) {
	data, err := os.ReadFile("testdata/media/video.mp4")
	require.NoError(t, err)
	srv, _ := rangeServer(t, data)

	safehttp.SetAllowPrivate(false)
	defer safehttp.SetAllowPrivate(true)
	_, err = RemoteTrack(t.Context(), HTTP, srv.URL)
	require.Error(t, err)
	assert.True(t, errors.Is(err, safehttp.ErrBlockedIP), "%v", err)
}
