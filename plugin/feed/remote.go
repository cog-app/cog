package feed

import (
	"fmt"
	"net/url"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
)

// HTTP fetches what subscriptions name: feeds, pages and media. Their URLs
// are typed by users or written by the feeds themselves, so it dials only
// public addresses — never the server's own network or a cloud metadata
// service. Requests are bounded by their contexts, not by a client timeout,
// because a podcast episode can take a long time to download.
var HTTP = safehttp.Client(0)

// CheckURL reports whether raw is a URL the plugin may fetch, or pass to
// yt-dlp: an absolute http or https URL. Anything else — a file: URL, or a
// string starting with "-" that a command would read as an option — is
// refused.
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q: only http and https", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("URL %q has no host", raw)
	}
	return nil
}
