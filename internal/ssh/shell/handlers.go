package shell

import (
	"context"
	"io"
	"io/fs"
	"os"

	"mvdan.cc/sh/v3/interp"
)

// openHandler intercepts file opens for redirects (>, >>, <).
// All paths are resolved and contained within the user's files directory.
func (s *Shell) openHandler(ctx context.Context, path string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	hc := interp.HandlerCtx(ctx)
	resolved, err := s.resolve(hc.Dir, path)
	if err != nil {
		return nil, err
	}
	return os.OpenFile(resolved, flag, perm)
}

// statHandler intercepts stat calls for test -f, cd validation, etc.
func (s *Shell) statHandler(ctx context.Context, name string, followSymlinks bool) (fs.FileInfo, error) {
	hc := interp.HandlerCtx(ctx)
	resolved, err := s.resolve(hc.Dir, name)
	if err != nil {
		return nil, err
	}
	if followSymlinks {
		return os.Stat(resolved)
	}
	return os.Lstat(resolved)
}

// readDirHandler intercepts directory reads for glob expansion.
func (s *Shell) readDirHandler(ctx context.Context, path string) ([]fs.DirEntry, error) {
	hc := interp.HandlerCtx(ctx)
	resolved, err := s.resolve(hc.Dir, path)
	if err != nil {
		return nil, err
	}
	return os.ReadDir(resolved)
}
