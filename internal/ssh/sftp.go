package ssh

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/vfs"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

// serveSFTP handles an SFTP subsystem session.
func (s *Server) serveSFTP(channel gossh.Channel, filesDir string, userFS vfs.FS) {
	// If VFS is available, use it; otherwise fall back to direct filesystem.
	var h sftp.Handlers
	if userFS != nil {
		vh := &vfsSFTPHandler{fs: userFS}
		h = sftp.Handlers{
			FileGet:  vh,
			FilePut:  vh,
			FileCmd:  vh,
			FileList: vh,
		}
	} else {
		// Resolve symlinks so containment checks work consistently.
		if resolved, err := filepath.EvalSymlinks(filesDir); err == nil {
			filesDir = resolved
		}
		dh := &directSFTPHandler{filesDir: filesDir}
		h = sftp.Handlers{
			FileGet:  dh,
			FilePut:  dh,
			FileCmd:  dh,
			FileList: dh,
		}
	}

	var opts []sftp.RequestServerOption
	if userFS != nil {
		opts = append(opts, sftp.WithStartDirectory("/files"))
	}
	srv := sftp.NewRequestServer(channel, h, opts...)
	if err := srv.Serve(); err != nil {
		if !errors.Is(err, io.EOF) {
			s.log.Debug("sftp session ended", "err", err)
		}
	}
}

// --- VFS-based SFTP handler ---

type vfsSFTPHandler struct {
	fs vfs.FS
}

func (h *vfsSFTPHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	if opener, ok := h.fs.(vfs.FileOpener); ok {
		return opener.OpenFile(r.Filepath, os.O_RDONLY, 0)
	}
	// Fallback: read all into memory.
	rc, err := h.fs.Get(r.Filepath)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	return readerAtBytes(data), nil
}

func (h *vfsSFTPHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	if opener, ok := h.fs.(vfs.FileOpener); ok {
		pflags := r.Pflags()
		osFlags := os.O_WRONLY
		if pflags.Creat {
			osFlags |= os.O_CREATE
		}
		if pflags.Trunc {
			osFlags |= os.O_TRUNC
		}
		if pflags.Append {
			osFlags |= os.O_APPEND
		}
		if pflags.Excl {
			osFlags |= os.O_EXCL
		}
		return opener.OpenFile(r.Filepath, osFlags, 0644)
	}
	return nil, fmt.Errorf("backend does not support file writes")
}

func (h *vfsSFTPHandler) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Setstat":
		setter, ok := h.fs.(vfs.AttrSetter)
		if !ok {
			return sftp.ErrSSHFxOpUnsupported
		}
		flags := r.AttrFlags()
		attrs := r.Attributes()
		if flags.Size {
			if err := setter.Truncate(r.Filepath, int64(attrs.Size)); err != nil {
				return err
			}
		}
		if flags.Permissions {
			if err := setter.Chmod(r.Filepath, attrs.FileMode()); err != nil {
				return err
			}
		}
		if flags.Acmodtime {
			if err := setter.Chtimes(r.Filepath, attrs.AccessTime(), attrs.ModTime()); err != nil {
				return err
			}
		}
		return nil

	case "Rename":
		return h.fs.Rename(r.Filepath, r.Target)

	case "Rmdir", "Remove":
		return h.fs.Delete(r.Filepath)

	case "Mkdir":
		return h.fs.Mkdir(r.Filepath)

	case "Link":
		if setter, ok := h.fs.(vfs.AttrSetter); ok {
			return setter.Link(r.Filepath, r.Target)
		}
		return sftp.ErrSSHFxOpUnsupported

	case "Symlink":
		return fmt.Errorf("symlinks are not allowed")

	default:
		return fmt.Errorf("unsupported command: %s", r.Method)
	}
}

func (h *vfsSFTPHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		if dr, ok := h.fs.(vfs.DirReader); ok {
			entries, err := dr.ReadDirEntries(r.Filepath)
			if err != nil {
				return nil, err
			}
			infos := make([]fs.FileInfo, 0, len(entries))
			for _, e := range entries {
				info, err := e.Info()
				if err != nil {
					continue
				}
				infos = append(infos, info)
			}
			return listAt(infos), nil
		}
		// Fallback: use List and synthesize FileInfo.
		items, err := h.fs.List(r.Filepath)
		if err != nil {
			return nil, err
		}
		infos := make([]fs.FileInfo, len(items))
		for i := range items {
			infos[i] = &syntheticInfo{fi: &items[i]}
		}
		return listAt(infos), nil

	case "Stat":
		if dr, ok := h.fs.(vfs.DirReader); ok {
			info, err := dr.StatInfo(r.Filepath)
			if err != nil {
				return nil, err
			}
			return listAt{info}, nil
		}
		fi, err := h.fs.Stat(r.Filepath)
		if err != nil {
			return nil, err
		}
		return listAt{&syntheticInfo{fi: fi}}, nil

	case "Readlink":
		// Not supported for VFS backends.
		return nil, sftp.ErrSSHFxOpUnsupported

	default:
		return nil, fmt.Errorf("unsupported list method: %s", r.Method)
	}
}

// syntheticInfo wraps vfs.FileInfo to implement fs.FileInfo.
type syntheticInfo struct {
	fi *vfs.FileInfo
}

func (s *syntheticInfo) Name() string       { return s.fi.Name }
func (s *syntheticInfo) Size() int64        { return s.fi.Size }
func (s *syntheticInfo) ModTime() time.Time { return s.fi.ModTime }
func (s *syntheticInfo) IsDir() bool        { return s.fi.IsDir }
func (s *syntheticInfo) Sys() any           { return nil }

func (s *syntheticInfo) Mode() fs.FileMode {
	if s.fi.IsDir {
		return fs.ModeDir | 0755
	}
	return 0644
}

// readerAtBytes wraps a byte slice to implement io.ReaderAt.
type readerAtBytes []byte

func (b readerAtBytes) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n+int(off) >= len(b) {
		return n, io.EOF
	}
	return n, nil
}

// --- Direct filesystem SFTP handler (fallback) ---

// directSFTPHandler implements sftp.Handlers interfaces with path sandboxing.
type directSFTPHandler struct {
	filesDir string
}

func (h *directSFTPHandler) resolve(vpath string) (string, error) {
	cleaned := filepath.Clean("/" + vpath)
	real := filepath.Join(h.filesDir, cleaned)

	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		parent, err2 := filepath.EvalSymlinks(filepath.Dir(real))
		if err2 != nil {
			return "", fmt.Errorf("path not found: %s", vpath)
		}
		resolved = filepath.Join(parent, filepath.Base(real))
	}

	if !strings.HasPrefix(resolved, h.filesDir) {
		return "", fmt.Errorf("access denied: path outside home directory")
	}
	return resolved, nil
}

func (h *directSFTPHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	path, err := h.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (h *directSFTPHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	path, err := h.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}

	pflags := r.Pflags()
	osFlags := os.O_WRONLY
	if pflags.Creat {
		osFlags |= os.O_CREATE
	}
	if pflags.Trunc {
		osFlags |= os.O_TRUNC
	}
	if pflags.Append {
		osFlags |= os.O_APPEND
	}
	if pflags.Excl {
		osFlags |= os.O_EXCL
	}

	return os.OpenFile(path, osFlags, 0644)
}

func (h *directSFTPHandler) Filecmd(r *sftp.Request) error {
	path, err := h.resolve(r.Filepath)
	if err != nil {
		return err
	}

	switch r.Method {
	case "Setstat":
		flags := r.AttrFlags()
		attrs := r.Attributes()
		if flags.Size {
			if err := os.Truncate(path, int64(attrs.Size)); err != nil {
				return err
			}
		}
		if flags.Permissions {
			if err := os.Chmod(path, attrs.FileMode()); err != nil {
				return err
			}
		}
		if flags.Acmodtime {
			if err := os.Chtimes(path, attrs.AccessTime(), attrs.ModTime()); err != nil {
				return err
			}
		}
		return nil

	case "Rename":
		target, err := h.resolve(r.Target)
		if err != nil {
			return err
		}
		return os.Rename(path, target)

	case "Rmdir":
		return os.Remove(path)

	case "Remove":
		return os.Remove(path)

	case "Mkdir":
		return os.MkdirAll(path, 0755)

	case "Link":
		target, err := h.resolve(r.Target)
		if err != nil {
			return err
		}
		return os.Link(path, target)

	case "Symlink":
		return fmt.Errorf("symlinks are not allowed")

	default:
		return fmt.Errorf("unsupported command: %s", r.Method)
	}
}

func (h *directSFTPHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	path, err := h.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}

	switch r.Method {
	case "List":
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		infos := make([]fs.FileInfo, 0, len(entries))
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			infos = append(infos, info)
		}
		return listAt(infos), nil

	case "Stat":
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		return listAt{info}, nil

	case "Readlink":
		target, err := os.Readlink(path)
		if err != nil {
			return nil, err
		}
		return listAt{readlinkInfo(target)}, nil

	default:
		return nil, fmt.Errorf("unsupported list method: %s", r.Method)
	}
}

// listAt implements sftp.ListerAt for a slice of os.FileInfo.
type listAt []fs.FileInfo

func (l listAt) ListAt(ls []fs.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(ls, l[offset:])
	if n+int(offset) >= len(l) {
		return n, io.EOF
	}
	return n, nil
}

// readlinkInfo is a minimal FileInfo for readlink results.
type readlinkInfo string

func (r readlinkInfo) Name() string       { return string(r) }
func (r readlinkInfo) Size() int64        { return 0 }
func (r readlinkInfo) Mode() fs.FileMode  { return fs.ModeSymlink }
func (r readlinkInfo) ModTime() time.Time { return time.Time{} }
func (r readlinkInfo) IsDir() bool        { return false }
func (r readlinkInfo) Sys() any           { return nil }
