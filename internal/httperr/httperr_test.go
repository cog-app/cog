package httperr_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/httperr"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestStatus(t *testing.T) {
	base := errors.New("wrong password")
	wrong := httperr.WithStatus(http.StatusUnauthorized, base)

	require.Equal(t, http.StatusUnauthorized, httperr.Status(wrong))
	require.Equal(t, http.StatusUnauthorized, httperr.Status(fmt.Errorf("sign in: %w", wrong)))
	require.True(t, errors.Is(wrong, base), "wrapping hid the error")
	require.Equal(t, "wrong password", wrong.Error())

	require.Equal(t, http.StatusInternalServerError, httperr.Status(errors.New("disk I/O error")))
	require.Equal(t, http.StatusInternalServerError, httperr.Status(errors.Join(errors.New("disk I/O error"), fmt.Errorf("x"))))
}
