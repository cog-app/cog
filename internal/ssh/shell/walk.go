package shell

import (
	"errors"
	"path"

	"git.duckfam.us/jj-software/cog/vfs"
)

// errSkipDir returned by a walkFunc for a directory leaves its contents out.
var errSkipDir = errors.New("skip directory")

// walkFunc is called for each entry of a walk. depth is 0 for the root.
type walkFunc func(p string, fi *vfs.FileInfo, depth int) error

// walk visits root and everything below it, depth first, in the order List
// returns entries. Entries come from List, which carries size, time and type,
// so nothing is stat'ed per entry. A directory that cannot be listed is
// passed to onErr and skipped; the walk goes on. An error from fn other than
// errSkipDir stops it.
func (s *Shell) walk(root string, fn walkFunc, onErr func(p string, err error)) error {
	fi, err := s.fs.Stat(root)
	if err != nil {
		return err
	}
	fi.Path = root
	return s.walkEntry(root, fi, 0, fn, onErr)
}

func (s *Shell) walkEntry(p string, fi *vfs.FileInfo, depth int, fn walkFunc, onErr func(string, error)) error {
	if err := fn(p, fi, depth); err != nil {
		if errors.Is(err, errSkipDir) {
			return nil
		}
		return err
	}
	if !fi.IsDir {
		return nil
	}
	entries, err := s.fs.List(p)
	if err != nil {
		onErr(p, err)
		return nil
	}
	for i := range entries {
		e := &entries[i]
		child := path.Join(p, e.Name)
		if err := s.walkEntry(child, e, depth+1, fn, onErr); err != nil {
			return err
		}
	}
	return nil
}
