package httphdr_test

import (
	"mime"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/httphdr"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestAttachmentRoundTrips(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":           "report.pdf",
		`a "quoted" name.txt`:  `a "quoted" name.txt`,
		"x\r\nSet-Cookie: a=b": "xSet-Cookie: a=b",
		"../../etc/passwd":     "....etcpasswd",
		"café ☕.txt":           "café ☕.txt",
		"semi;colon.txt":       "semi;colon.txt",
	} {
		v := httphdr.Attachment(in)
		kind, params, err := mime.ParseMediaType(v)
		require.NoError(t, err, in)
		require.Equal(t, "attachment", kind)
		require.Equal(t, want, params["filename"], in)
	}
}

func TestAttachmentWithoutName(t *testing.T) {
	require.Equal(t, "attachment", httphdr.Attachment("\r\n"))
	require.Equal(t, "inline", httphdr.Inline(""))
}
