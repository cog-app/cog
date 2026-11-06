package feed

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin"
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
