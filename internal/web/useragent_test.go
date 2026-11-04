package web

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestDescribeUserAgent(t *testing.T) {
	for ua, want := range map[string]string{
		"": "Unknown device",
		"Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0":                                                    "Firefox on Linux",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36 Edg/130.0":     "Edge on Windows",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/604.1": "Safari on iPhone",
		"Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Mobile Safari/537.36":                  "Chrome on Android",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36":            "Chrome on macOS",
		"Nextcloud-android/3.30":        "Nextcloud-android/3.30",
		"curl/8.10.1":                   "curl",
		"Mozilla/5.0 (Nextcloud, 3.14)": "Mozilla/5.0",
	} {
		require.Equal(t, want, describeUserAgent(ua), "ua %q", ua)
	}
}
