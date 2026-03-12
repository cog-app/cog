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

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

// serveSFTP handles an SFTP subsystem session.
func (s *Server) serveSFTP(channel gossh.Channel, filesDir string) {
	// Resolve symlinks so containment checks work consistently.
	if resolved, err := filepath.EvalSymlinks(filesDir); err == nil {
		filesDir = resolved
	}

	h := &sftpHandler{filesDir: filesDir}
	srv := sftp.NewRequestServer(channel, sftp.Handlers{
		FileGet:  h,
		FilePut:  h,
		FileCmd:  h,
		FileList: h,
	})

	if err := srv.Serve(); err != nil {
		if !errors.Is(err, io.EOF) {
			s.log.Debug("sftp session ended", "err", err)
		}
	}
}

// sftpHandler implements sftp.Handlers interfaces with path sandboxing.
type sftpHandler struct {
	filesDir string
}

// resolve maps a virtual SFTP path to a real filesystem path, ensuring containment.
func (h *sftpHandler) resolve(vpath string) (string, error) {
	// SFTP paths are always absolute (rooted at user's home).
	cleaned := filepath.Clean("/" + vpath)
	real := filepath.Join(h.filesDir, cleaned)

	// Evaluate symlinks for existing paths.
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		// Path doesn't exist yet — check parent.
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

// Fileread returns a reader for the given path.
func (h *sftpHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	path, err := h.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

// Filewrite returns a writer for the given path.
func (h *sftpHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
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

// Filecmd handles file commands (mkdir, rename, remove, etc.)
func (h *sftpHandler) Filecmd(r *sftp.Request) error {
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
		// Deny symlinks to prevent escape.
		return fmt.Errorf("symlinks are not allowed")

	default:
		return fmt.Errorf("unsupported command: %s", r.Method)
	}
}

// Filelist returns directory listings or stat results.
func (h *sftpHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
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
		// Resolve the symlink target but keep it within sandbox.
		target, err := os.Readlink(path)
		if err != nil {
			return nil, err
		}
		// Return a fake FileInfo with the target as name.
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
