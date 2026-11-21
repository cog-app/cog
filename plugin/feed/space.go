package feed

import (
	"fmt"
	"io"
	"log/slog"
	"path"
	"path/filepath"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// Space is where the feed keeps one user's files. Downloaded media,
// captions and cleaned audio are theirs, in their storage, and count toward
// their quota. Programs (yt-dlp, whisper, ffmpeg) write into the cache, and
// what they made is then taken into the storage, so it is counted like any
// other write and refused when it would take the user over quota.
//
// The downloads table records names within the storage ("media/12.mp3").
// Rows written before the feed had storage hold absolute paths into its
// old directories, whose files moved into the storage under the same base
// name; Path resolves both.
type Space struct {
	Own   plugin.Storage
	Cache plugin.Storage
}

// Space returns the user's feed space.
func (p *Plugin) Space(username string) (Space, error) {
	own, err := p.ctx.UserStorage(username)
	if err != nil {
		return Space{}, fmt.Errorf("feed storage: %w", err)
	}
	cache, err := p.ctx.UserCache(username)
	if err != nil {
		return Space{}, fmt.Errorf("feed cache: %w", err)
	}
	return Space{Own: own, Cache: cache}, nil
}

// Name returns the storage name a downloads row records, as written now or
// before the feed had storage.
func Name(stored string) string {
	if !filepath.IsAbs(stored) {
		return stored
	}
	dir := "media"
	if filepath.Base(filepath.Dir(stored)) == "captions" {
		dir = "captions"
	}
	return dir + "/" + filepath.Base(stored)
}

// Path returns the local path of a file a downloads row records, for a
// program that reads it.
func (s Space) Path(stored string) (string, error) {
	return s.Own.LocalPath(Name(stored))
}

// Keep takes the file a program wrote at the cache's name tmp into the
// storage as name, and returns its size.
func (s Space) Keep(name, tmp string) (int64, error) {
	if !strings.HasPrefix(name, "media/") && !strings.HasPrefix(name, "captions/") {
		return 0, fmt.Errorf("feed: keep %q: not a media or captions name", name)
	}
	return s.Own.Take(path.Clean(name), s.Cache, tmp)
}

// Track reads the fragment index of a stored track file (hls.ReadTrack),
// which reads only the head of the file, and returns it with the file's size.
func (s Space) Track(stored string) (hls.Track, int64, error) {
	return readTrack(s.Own, Name(stored))
}

// CachedTrack is Track for a track file in the cache, such as an edge.
func (s Space) CachedTrack(name string) (hls.Track, int64, error) {
	return readTrack(s.Cache, name)
}

func readTrack(from plugin.Storage, stored string) (t hls.Track, size int64, err error) {
	err = readAt(from, stored, func(r io.ReaderAt, n int64) error {
		t, size, err = hls.Track{}, n, nil
		t, err = hls.ReadTrack(r, n)
		return err
	})
	return t, size, err
}

// TrackInit reads the init section (ftyp and moov) of a stored track file
// whose index is t.
func (s Space) TrackInit(stored string, t hls.Track) (init []byte, err error) {
	err = readAt(s.Own, Name(stored), func(r io.ReaderAt, n int64) error {
		if t.InitOffset < 0 || t.InitLength <= 0 || t.InitOffset+t.InitLength > n {
			return fmt.Errorf("init %d+%d outside a %d-byte file", t.InitOffset, t.InitLength, n)
		}
		init = make([]byte, t.InitLength)
		_, err := r.ReadAt(init, t.InitOffset)
		return err
	})
	return init, err
}

// DecodeTime is hls.FirstDecodeTime of a stored track file.
func (s Space) DecodeTime(stored string) (t float64, err error) {
	err = readAt(s.Own, Name(stored), func(r io.ReaderAt, n int64) error {
		t, err = hls.FirstDecodeTime(r, n)
		return err
	})
	return t, err
}

// CachedDecodeTime is hls.FirstDecodeTime of a track file in the cache:
// where players put its first fragment.
func (s Space) CachedDecodeTime(name string) (t float64, err error) {
	err = readAt(s.Cache, name, func(r io.ReaderAt, n int64) error {
		t, err = hls.FirstDecodeTime(r, n)
		return err
	})
	return t, err
}

// readAt opens a track file for reading at offsets and calls read on it.
func readAt(from plugin.Storage, stored string, read func(r io.ReaderAt, size int64) error) error {
	f, err := from.Open(stored)
	if err != nil {
		return fmt.Errorf("open track: %w", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Warn("feed: close track", "path", stored, "err", err)
		}
	}()
	ra, ok := f.(io.ReaderAt)
	if !ok {
		return fmt.Errorf("track %s: storage file %T cannot be read at an offset", stored, f)
	}
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat track: %w", err)
	}
	if err := read(ra, st.Size()); err != nil {
		return fmt.Errorf("track %s: %w", stored, err)
	}
	return nil
}
