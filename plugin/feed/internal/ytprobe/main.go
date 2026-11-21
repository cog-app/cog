// Command ytprobe resolves a YouTube watch URL's two tracks, waits until
// googlevideo serves them (their available_at, as Cog does), and asks each
// for its first 256 KiB from here, printing the answer. It also prints a curl
// line for each, to check whether googlevideo serves them to another IP.
//
//	go run ./plugin/feed/internal/ytprobe [-wait=false] <watch-url>
//
// With -wait=false it asks at once, as Cog did before it read available_at.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func main() {
	wait := flag.Bool("wait", true, "wait for each URL's available_at before probing it")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: ytprobe [-wait=false] <watch-url>")
		os.Exit(2)
	}
	if err := run(context.Background(), flag.Arg(0), *wait); err != nil {
		fmt.Fprintln(os.Stderr, "ytprobe:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, watchURL string, wait bool) error {
	begin := time.Now()
	format := feed.YouTubeFormat(feed.YouTubeVideoFormat, feed.YouTubeAudioFormat)
	video, audio, err := feed.ResolveYouTubeTracks(ctx, watchURL, format)
	if err != nil {
		return err
	}
	resolved := time.Now()
	fmt.Printf("resolved in %s\n", resolved.Sub(begin).Round(time.Millisecond))
	for _, t := range []feed.YouTubeTrack{video, audio} {
		avail := "none"
		if !t.AvailableAt.IsZero() {
			avail = fmt.Sprintf("%s (%s after the resolve)", t.AvailableAt.Format("15:04:05"), t.AvailableAt.Sub(resolved).Round(time.Millisecond))
		}
		fmt.Printf("%s format %s, expires %s, available_at %s\n", t.Kind, t.FormatID, t.ExpiresAt.Format("2006-01-02 15:04:05 MST"), avail)
		fmt.Printf("curl -sS -o /dev/null -w '%%{http_code} %%{size_download}\\n' -r 0-262143 '%s'\n", t.URL)
	}
	if wait {
		start := time.Now()
		if err := feed.AwaitAvailable(ctx, start, video, audio); err != nil {
			return err
		}
		fmt.Printf("waited %s\n", time.Since(start).Round(time.Millisecond))
	}
	for _, t := range []feed.YouTubeTrack{video, audio} {
		status, n, err := probe(ctx, t.URL)
		if err != nil {
			return fmt.Errorf("%s: %w", t.Kind, err)
		}
		fmt.Printf("%s probe: %s, %d bytes\n", t.Kind, status, n)
	}
	return nil
}

// probe asks u for its first 256 KiB and returns the status and the length
// of the answer.
func probe(ctx context.Context, u string) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Range", "bytes=0-262143")
	resp, err := feed.HTTP.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return resp.Status, n, err
	}
	return resp.Status, n, nil
}
