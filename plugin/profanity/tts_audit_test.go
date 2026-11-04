package profanity

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// The replacement text is a user's, so a shell command in it is data, not a
// command: the template sees it in $TEXT.
func TestGenerateSystemTextIsNotRun(t *testing.T) {
	dir := t.TempDir()
	pwned := filepath.Join(dir, "pwned")
	out := filepath.Join(dir, "out.txt")

	text := "darn $(touch " + pwned + ") `touch " + pwned + "`; touch " + pwned
	require.NoError(t, generateSystem(context.Background(), `printf '%s' "$TEXT" > "$OUTPUT"`, text, out))

	_, err := os.Stat(pwned)
	require.True(t, os.IsNotExist(err), "the text ran as a command")
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, text, string(got))
}
