package password

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestCheckLength(t *testing.T) {
	c := &Checker{Policy: func() Policy { return Policy{MinLength: 10} }}
	require.True(t, errors.Is(c.Check("short"), ErrWeak))
	require.NoError(t, c.Check("long enough"))
}

// "password" has SHA-1 5BAA61E4C9B93F3F0682250B6CF8331B7EE68FD8; the
// service is asked for 5BAA6 and answers with suffixes.
func TestCheckBreach(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "true", r.Header.Get("Add-Padding"))
		if r.URL.Path == "/range/5BAA6" {
			w.Write([]byte("0000000000000000000000000000000000A:0\r\n1E4C9B93F3F0682250B6CF8331B7EE68FD8:3861493\r\n"))
			return
		}
		w.Write([]byte("0000000000000000000000000000000000A:0\r\n"))
	}))
	defer srv.Close()
	c := &Checker{
		Policy: func() Policy { return Policy{MinLength: 1, BreachCheck: true} },
		Breach: &BreachList{URL: srv.URL + "/range/", HTTP: srv.Client()},
	}
	err := c.Check("password")
	require.True(t, errors.Is(err, ErrWeak), "got %v", err)
	require.NoError(t, c.Check("not in the list"))
}

// A list that cannot be reached refuses the password rather than setting
// it unchecked.
func TestCheckBreachFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := &Checker{
		Policy: func() Policy { return Policy{MinLength: 1, BreachCheck: true} },
		Breach: &BreachList{URL: srv.URL + "/range/", HTTP: srv.Client()},
	}
	require.True(t, errors.Is(c.Check("anything"), ErrUnchecked))
}
