package service

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestGiveDirArgs: on Windows the install directory keeps out the local
// users C:\ProgramData lets in, and lets in only SYSTEM, Administrators
// and the service.
func TestGiveDirArgs(t *testing.T) {
	got := strings.Join(giveDirArgs(`C:\ProgramData\Cog`, `NT SERVICE\cog`), " ")
	require.Equal(t, `C:\ProgramData\Cog /inheritance:r /grant:r *S-1-5-18:(OI)(CI)F /grant:r *S-1-5-32-544:(OI)(CI)F /grant:r NT SERVICE\cog:(OI)(CI)M /Q`, got)
}
