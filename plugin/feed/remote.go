package feed

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
	"git.duckfam.us/jj-software/cog/internal/sandbox"
)

// HTTP fetches what subscriptions name: feeds, pages and media. Their URLs
// are typed by users or written by the feeds themselves, so it dials only
// public addresses — never the server's own network or a cloud metadata
// service. Requests are bounded by their contexts and by safehttp's idle
// timeout, not by a client timeout, because a podcast episode can take a long
// time to download.
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

// Proxy is the loopback proxy yt-dlp's connections go through, so a
// redirect or a manifest naming an internal address is refused like a
// URL typed by a user would be.
var Proxy = &safehttp.Proxy{}

// YTDLP returns the yt-dlp command that fetches raw with args, writing only
// into the directories in write. yt-dlp makes its own connections, which
// HTTP cannot guard directly, so raw must pass CheckURL and name a public
// address, every connection it then makes goes through Proxy, and it runs
// sandboxed: it reaches the network, but none of the server's files beyond
// write.
func YTDLP(ctx context.Context, raw string, write []string, args ...string) (*exec.Cmd, error) {
	if err := CheckURL(raw); err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if err := safehttp.CheckHost(ctx, u.Hostname()); err != nil {
		return nil, err
	}
	proxy, err := Proxy.Addr()
	if err != nil {
		return nil, err
	}
	args = append([]string{"--proxy", proxy}, args...)
	return sandbox.CommandContext(ctx, sandbox.Spec{Write: write, Network: true}, "yt-dlp", append(args, "--", raw)...), nil
}
