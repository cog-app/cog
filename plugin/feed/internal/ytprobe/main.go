// Command ytprobe resolves a YouTube watch URL's two tracks and prints a
// curl line for each, to check whether googlevideo serves them to another IP.
//
//	go run ./plugin/feed/internal/ytprobe <watch-url>
package main

import (
	"context"
	"fmt"
	"os"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: ytprobe <watch-url>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "ytprobe:", err)
		os.Exit(1)
	}
}

func run(watchURL string) error {
	// Each selector is a "/" chain, so group them before joining with "+".
	format := "(" + feed.YouTubeVideoFormat + ")+(" + feed.YouTubeAudioFormat + ")"
	video, audio, err := feed.ResolveYouTubeTracks(context.Background(), watchURL, format)
	if err != nil {
		return err
	}
	for _, t := range []feed.YouTubeTrack{video, audio} {
		fmt.Printf("%s format %s, expires %s\n", t.Kind, t.FormatID, t.ExpiresAt.Format("2006-01-02 15:04:05 MST"))
		fmt.Printf("curl -sS -o /dev/null -w '%%{http_code} %%{size_download}\\n' -r 0-262143 '%s'\n", t.URL)
	}
	return nil
}
