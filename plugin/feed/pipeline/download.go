// Package pipeline contains background workers that act on items after ingest:
// downloading media, muting profanity, etc.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Downloader processes queued downloads from the feed store.
type Downloader struct {
	store *feed.Store
	space feed.Space
}

// NewDownloader creates a Downloader that keeps media in space.
func NewDownloader(s *feed.Store, space feed.Space) *Downloader {
	return &Downloader{store: s, space: space}
}

// RunOnce processes all currently-queued downloads sequentially and returns.
func (d *Downloader) RunOnce(ctx context.Context) error {
	queued, err := d.store.PendingDownloads(100)
	if err != nil {
		return fmt.Errorf("downloader list queued: %w", err)
	}
	for _, q := range queued {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.processOne(ctx, q.ItemID); err != nil {
			slog.Warn("feed downloader: process failed", "item_id", q.ItemID, "err", err)
		}
	}
	return nil
}

func (d *Downloader) processOne(ctx context.Context, itemID int64) error {
	if err := d.store.MarkDownloadStart(itemID); err != nil {
		return fmt.Errorf("mark start: %w", err)
	}
	it, err := d.store.GetItem(itemID)
	if err != nil {
		return d.fail(itemID, err)
	}
	sub, err := d.store.GetSubscription(it.SubscriptionID)
	if err != nil {
		return d.fail(itemID, err)
	}
	// The media URL is whatever the feed wrote.
	if err := feed.CheckURL(it.MediaURL); err != nil {
		return d.fail(itemID, err)
	}
	id := strconv.FormatInt(itemID, 10)
	if sub.Kind == "youtube" && it.Type != "audio" {
		return d.youtubeVideo(ctx, itemID, it.MediaURL, id)
	}
	ext := extForMIME(it.MediaMIME, it.Type)
	if sub.Kind == "youtube" {
		ext = ".m4a"
	}
	name, size, err := d.fetch(ctx, sub.Kind, it.MediaURL, id+ext, youtubeAudio)
	if err != nil {
		return d.fail(itemID, err)
	}
	if err := d.store.MarkDownloadReady(itemID, name, size); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}
	return nil
}

// youtubeVideo downloads a YouTube video's picture and sound as the two
// tracks YouTube serves, media/<id>.video.mp4 and media/<id>.m4a.
func (d *Downloader) youtubeVideo(ctx context.Context, itemID int64, src, id string) error {
	video, videoSize, err := d.fetch(ctx, "youtube", src, id+".video.mp4", youtubeVideo)
	if err != nil {
		return d.fail(itemID, err)
	}
	audio, audioSize, err := d.fetch(ctx, "youtube", src, id+".m4a", youtubeAudio)
	if err != nil {
		return d.fail(itemID, errors.Join(err, d.space.Own.Remove(video)))
	}
	if err := d.store.MarkVideoDownloadReady(itemID, audio, video, videoSize+audioSize); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}
	return nil
}

// fetch downloads src into the cache as file, with yt-dlp and format for a
// YouTube item, and keeps it as media/<file>.
func (d *Downloader) fetch(ctx context.Context, kind, src, file string, format []string) (string, int64, error) {
	tmp := "downloads/" + file
	dst, err := d.space.Cache.LocalPath(tmp)
	if err != nil {
		return "", 0, err
	}
	switch kind {
	case "youtube":
		cmd, err := feed.YTDLP(ctx, src, []string{filepath.Dir(dst)}, append(format, "-o", dst)...)
		if err != nil {
			return "", 0, err
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", 0, errors.Join(fmt.Errorf("yt-dlp: %w: %s", err, string(out)), d.space.Cache.Remove(tmp))
		}
	default:
		if err := httpFetch(ctx, src, dst); err != nil {
			return "", 0, err
		}
	}
	name := "media/" + file
	size, err := d.space.Keep(name, tmp)
	if err != nil {
		return "", 0, errors.Join(err, d.space.Cache.Remove(tmp))
	}
	return name, size, nil
}

// youtubeVideo and youtubeAudio are what yt-dlp downloads of a YouTube
// item. YouTube serves a video's picture and sound apart, as fragmented MP4
// tracks with an index; they stay apart as YouTube serves them, and the
// packager presents them together over HLS. The picture is H.264 up to
// 1080p, failing that the best up to 1080p; the sound is its m4a audio.
// --fixup never keeps yt-dlp from remuxing an m4a into a plain MP4, which
// would lose the index.
var (
	youtubeVideo = []string{"--fixup", "never", "--format", "bv[height<=1080][vcodec^=avc1]/bv[height<=1080]"}
	youtubeAudio = []string{"--fixup", "never", "--format", "ba[ext=m4a]/ba"}
)

func (d *Downloader) fail(itemID int64, err error) error {
	if dbErr := d.store.MarkDownloadError(itemID, err.Error()); dbErr != nil {
		return fmt.Errorf("download error %v (and recording failed: %w)", err, dbErr)
	}
	return err
}

func httpFetch(ctx context.Context, src, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	resp, err := feed.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errors.New(resp.Status)
	}
	if resp.ContentLength > maxMediaBytes {
		return fmt.Errorf("media is %d bytes, over the %d byte limit", resp.ContentLength, maxMediaBytes)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxMediaBytes+1))
	if err == nil && n > maxMediaBytes {
		err = fmt.Errorf("media is over the %d byte limit", maxMediaBytes)
	}
	if err != nil {
		f.Close()
		if rmErr := os.Remove(dst); rmErr != nil {
			return errors.Join(err, rmErr)
		}
		return err
	}
	return f.Close()
}

// maxMediaBytes bounds one downloaded episode, so a feed cannot fill the
// disk with an endless body. It is far above any real podcast or video.
const maxMediaBytes = 8 << 30

func extForMIME(mime, typ string) string {
	switch mime {
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4", "audio/x-m4a":
		return ".m4a"
	case "audio/ogg":
		return ".ogg"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	}
	if typ == "video" {
		return ".bin.video"
	}
	return ".bin"
}
