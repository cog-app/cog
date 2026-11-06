package bodylimit_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/bodylimit"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestLimit(t *testing.T) {
	var got []byte
	var err error
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got, err = io.ReadAll(r.Body) })

	bodylimit.Middleware(4, h).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader("abcd")))
	require.NoError(t, err)
	require.Equal(t, "abcd", string(got))

	bodylimit.Middleware(4, h).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader("abcde")))
	var mbe *http.MaxBytesError
	require.True(t, errors.As(err, &mbe), "got %v", err)
	require.Equal(t, "abcd", string(got))

	bodylimit.Middleware(4, bodylimit.Unlimited(h)).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader("abcdefgh")))
	require.NoError(t, err)
	require.Equal(t, "abcdefgh", string(got))
}
