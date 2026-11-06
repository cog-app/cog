package ssh

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"git.duckfam.us/jj-software/cog/vfs"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

// serveSFTP handles an SFTP subsystem session on userFS, whose /files
// holds the quota and the files plugin's records itself.
func (s *Server) serveSFTP(channel gossh.Channel, userFS vfs.FS) {
	h := &vfsSFTPHandler{fs: userFS}
	srv := sftp.NewRequestServer(channel, sftp.Handlers{
		FileGet:  h,
		FilePut:  h,
		FileCmd:  h,
		FileList: h,
	}, sftp.WithStartDirectory("/files"))
	if err := srv.Serve(); err != nil {
		if !errors.Is(err, io.EOF) {
			s.log.Debug("sftp session ended", "err", err)
		}
	}
}

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
				if errors.Is(err, fs.ErrNotExist) {
					continue // removed since the directory was read
				}
				if err != nil {
					return nil, err
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
