package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"git.duckfam.us/jj-software/cog/vfs"

	"mvdan.cc/sh/v3/interp"
)

// The interpreter makes every path absolute against its working directory,
// which is a VFS path, before it reaches these handlers. Some reach them from
// builtins (test, [, glob expansion) whose context has no HandlerContext.

// openHandler opens files for redirects (>, >>, <).
func (s *Shell) openHandler(ctx context.Context, path string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	opener, ok := s.fs.(vfs.FileOpener)
	if !ok {
		return nil, fmt.Errorf("open %s: %w", path, errors.ErrUnsupported)
	}
	hc := interp.HandlerCtx(ctx)
	return opener.OpenFile(s.toVirtualPath(hc.Dir, path), flag, perm)
}

// statHandler stats files for test -f, cd validation, etc.
func (s *Shell) statHandler(ctx context.Context, name string, followSymlinks bool) (fs.FileInfo, error) {
	dr, err := s.dirReader()
	if err != nil {
		return nil, err
	}
	if followSymlinks {
		return dr.StatInfo(name)
	}
	return dr.LstatInfo(name)
}

// readDirHandler reads directories for glob expansion.
func (s *Shell) readDirHandler(ctx context.Context, path string) ([]fs.DirEntry, error) {
	dr, err := s.dirReader()
	if err != nil {
		return nil, err
	}
	return dr.ReadDirEntries(path)
}

// dirReader returns the shell's VFS as a DirReader.
func (s *Shell) dirReader() (vfs.DirReader, error) {
	dr, ok := s.fs.(vfs.DirReader)
	if !ok {
		return nil, fmt.Errorf("read directory: %w", errors.ErrUnsupported)
	}
	return dr, nil
}
