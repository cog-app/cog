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
	"os/exec"
	"path/filepath"
	"strconv"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Downloader processes queued downloads from the feed store.
type Downloader struct {
	store *feed.Store
	root  string
}

// NewDownloader creates a Downloader that writes files under root.
func NewDownloader(s *feed.Store, root string) *Downloader {
	return &Downloader{store: s, root: root}
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
	if err := os.MkdirAll(d.root, 0o755); err != nil {
		return d.fail(itemID, err)
	}

	dst := filepath.Join(d.root, strconv.FormatInt(itemID, 10)+extForMIME(it.MediaMIME, it.Type))

	// The media URL is whatever the feed wrote.
	if err := feed.CheckURL(it.MediaURL); err != nil {
		return d.fail(itemID, err)
	}
	switch sub.Kind {
	case "youtube":
		cmd := exec.CommandContext(ctx, "yt-dlp", "-o", dst, "--", it.MediaURL)
		if out, err := cmd.CombinedOutput(); err != nil {
			return d.fail(itemID, fmt.Errorf("yt-dlp: %w: %s", err, string(out)))
		}
	default:
		if err := httpFetch(ctx, it.MediaURL, dst); err != nil {
			return d.fail(itemID, err)
		}
	}

	info, err := os.Stat(dst)
	if err != nil {
		return d.fail(itemID, err)
	}
	if err := d.store.MarkDownloadReady(itemID, dst, info.Size()); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}
	return nil
}

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
