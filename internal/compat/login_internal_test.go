package compat

import (
	"strings"
	"testing"
)

func TestClientName(t *testing.T) {
	for ua, want := range map[string]string{
		"":                                  "An app",
		"CogFeed/1.0 (Pixel 8; Android 15)": "CogFeed on Pixel 8",
		"CogFeed/1.0 (thinkpad)":            "CogFeed on thinkpad",
		"CogFeed/1.0":                       "CogFeed",
		"CogFeed/1.0 ()":                    "CogFeed",
		"curl/8.5.0":                        "curl",
		// A browser's comment is its platform.
		"Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0":                       "Mozilla",
		"Mozilla/5.0 (Android) Nextcloud-android/3.30.0":                      "Nextcloud for Android",
		"CogFeed/1.0 (Ein ganz besonders langer Gerätename, der zu lang ist)": "CogFeed on Ein ganz besonders langer Gerätename, d",
		// Cut at 40 bytes, not inside the ä that straddles them.
		"CogFeed/1.0 (" + strings.Repeat("a", 39) + "ä)": "CogFeed on " + strings.Repeat("a", 39),
	} {
		if got := clientName(ua); got != want {
			t.Errorf("clientName(%q) = %q, want %q", ua, got, want)
		}
	}
}
