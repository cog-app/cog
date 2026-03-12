package shell

import (
	"context"
	"io"
	"io/fs"
	"os"
	"strings"

	"git.duckfam.us/jj-software/cog/vfs"

	"mvdan.cc/sh/v3/interp"
)

// interpreterToVirtualPath converts a path from the interpreter (which may be
// a real filesystem absolute path under filesDir) to a virtual VFS path.
// The interpreter calls absPath(r.Dir, name) before passing paths to stat/readdir
// handlers, so the path is always absolute — either a real path under filesDir
// or an absolute virtual path that doesn't live under filesDir (e.g., /.ssh).
func (s *Shell) interpreterToVirtualPath(name string) string {
	if strings.HasPrefix(name, s.filesDir) {
		return s.virtualPath(name)
	}
	return name
}

// openHandler intercepts file opens for redirects (>, >>, <).
// All paths are resolved and contained within the user's files directory.
func (s *Shell) openHandler(ctx context.Context, path string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	if s.fs != nil {
		if opener, ok := s.fs.(vfs.FileOpener); ok {
			hc := interp.HandlerCtx(ctx)
			vp := s.toVirtualPath(hc.Dir, path)
			return opener.OpenFile(vp, flag, perm)
		}
	}
	hc := interp.HandlerCtx(ctx)
	resolved, err := s.resolve(hc.Dir, path)
	if err != nil {
		return nil, err
	}
	return os.OpenFile(resolved, flag, perm)
}

// statHandler intercepts stat calls for test -f, cd validation, etc.
// NOTE: The interpreter prepends r.Dir to paths via absPath() before calling
// this handler, so name is always absolute. It may be called from built-in
// commands (test, [) whose context does NOT contain a HandlerContext.
func (s *Shell) statHandler(ctx context.Context, name string, followSymlinks bool) (fs.FileInfo, error) {
	if s.fs != nil {
		if dr, ok := s.fs.(vfs.DirReader); ok {
			vp := s.interpreterToVirtualPath(name)
			if followSymlinks {
				return dr.StatInfo(vp)
			}
			return dr.LstatInfo(vp)
		}
	}
	// The interpreter passes absolute paths (absPath(r.Dir, name)).
	// Resolve ensures symlink safety and containment within filesDir.
	resolved, err := s.resolve(s.filesDir, name)
	if err != nil {
		return nil, err
	}
	if followSymlinks {
		return os.Stat(resolved)
	}
	return os.Lstat(resolved)
}

// readDirHandler intercepts directory reads for glob expansion.
// NOTE: Same as statHandler — name is already absolute from absPath().
// May be called without HandlerContext from built-in glob expansion.
func (s *Shell) readDirHandler(ctx context.Context, path string) ([]fs.DirEntry, error) {
	if s.fs != nil {
		if dr, ok := s.fs.(vfs.DirReader); ok {
			vp := s.interpreterToVirtualPath(path)
			return dr.ReadDirEntries(vp)
		}
	}
	resolved, err := s.resolve(s.filesDir, path)
	if err != nil {
		return nil, err
	}
	return os.ReadDir(resolved)
}
