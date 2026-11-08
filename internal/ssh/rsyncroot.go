package ssh

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gokrazy/rsync"

	"git.duckfam.us/jj-software/cog/vfs"
)

// rsyncRoot is the directory an rsync session sends from and receives into:
// a directory of the session's VFS. Everything goes through the VFS, so the
// files plugin records each file and holds writes to the quota as it does
// for SFTP; nothing is staged on the server's disk.
//
// rsync names files relative to the root with slashes. A name is cleaned
// before use, and one that would climb out of the root is refused.
type rsyncRoot struct {
	fsys vfs.FS
	base string // absolute VFS path of the root
}

var (
	_ rsync.DestRoot = rsyncRoot{}
	_ fs.StatFS      = rsyncView{}
	_ fs.ReadDirFS   = rsyncView{}
)

func newRsyncRoot(fsys vfs.FS, base string) (rsyncRoot, error) {
	for _, need := range []struct {
		ok   bool
		what string
	}{
		{isA[vfs.FileOpener](fsys), "open files"},
		{isA[vfs.DirReader](fsys), "read directories"},
		{isA[vfs.AttrSetter](fsys), "set times"},
	} {
		if !need.ok {
			return rsyncRoot{}, fmt.Errorf("rsync: the file system cannot %s", need.what)
		}
	}
	return rsyncRoot{fsys: fsys, base: path.Clean("/" + base)}, nil
}

func isA[T any](v any) bool {
	_, ok := v.(T)
	return ok
}

// abs maps a name relative to the root to its VFS path.
func (r rsyncRoot) abs(op, name string) (string, error) {
	if name == "" || name == "." {
		return r.base, nil
	}
	if path.IsAbs(name) || !fs.ValidPath(path.Clean(name)) {
		return "", &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
	}
	return path.Join(r.base, name), nil
}

func (r rsyncRoot) Lstat(name string) (fs.FileInfo, error) {
	p, err := r.abs("lstat", name)
	if err != nil {
		return nil, err
	}
	fi, err := r.fsys.(vfs.DirReader).LstatInfo(p)
	return fi, notExist(name, err)
}

func (r rsyncRoot) Stat(name string) (fs.FileInfo, error) {
	p, err := r.abs("stat", name)
	if err != nil {
		return nil, err
	}
	fi, err := r.fsys.(vfs.DirReader).StatInfo(p)
	return fi, notExist(name, err)
}

func (r rsyncRoot) Open(name string) (rsync.BasisFile, error) {
	f, err := r.openFile("open", name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (r rsyncRoot) openFile(op, name string, flag int, perm fs.FileMode) (vfs.File, error) {
	p, err := r.abs(op, name)
	if err != nil {
		return nil, err
	}
	f, err := r.fsys.(vfs.FileOpener).OpenFile(p, flag, perm)
	if err != nil {
		return nil, notExist(name, err)
	}
	return f, nil
}

// CreateTemp creates a hidden, randomly named file beside name.
func (r rsyncRoot) CreateTemp(name string, perm fs.FileMode) (string, rsync.PendingFile, error) {
	dir, base := path.Split(name)
	for range 100 {
		tmp := dir + "." + base + "." + strconv.FormatUint(rand.Uint64(), 36)
		f, err := r.openFile("create", tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		return tmp, pendingFile{f}, nil
	}
	return "", nil, &fs.PathError{Op: "create", Path: name, Err: fs.ErrExist}
}

// pendingFile gives a VFS file the Sync a pending file needs. The files
// plugin's writes are durable when they close.
type pendingFile struct{ vfs.File }

func (pendingFile) Sync() error { return nil }

func (r rsyncRoot) Rename(oldname, newname string) error {
	from, err := r.abs("rename", oldname)
	if err != nil {
		return err
	}
	to, err := r.abs("rename", newname)
	if err != nil {
		return err
	}
	// The receiver renames only to move a file it received into place,
	// which is an update of the file there, not a delete and a new file.
	return notExist(oldname, vfs.Replace(r.fsys, from, to))
}

func (r rsyncRoot) Remove(name string) error {
	p, err := r.abs("remove", name)
	if err != nil {
		return err
	}
	if p == r.base {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrPermission}
	}
	return notExist(name, r.fsys.Delete(p))
}

func (r rsyncRoot) RemoveAll(name string) error {
	err := r.Remove(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (r rsyncRoot) MkdirAll(name string, _ fs.FileMode) error {
	p, err := r.abs("mkdir", name)
	if err != nil {
		return err
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(p, r.base), "/")
	if rel == "" {
		return nil
	}
	cur := r.base
	for part := range strings.SplitSeq(rel, "/") {
		cur = path.Join(cur, part)
		fi, err := r.fsys.Stat(cur)
		switch {
		case err == nil && fi.IsDir:
			continue
		case err == nil:
			return &fs.PathError{Op: "mkdir", Path: name, Err: syscall.ENOTDIR}
		case !isNotExist(err):
			return err
		}
		if err := r.fsys.Mkdir(cur); err != nil {
			return err
		}
	}
	return nil
}

// Readlink and Symlink fail: Cog's files have no symbolic links, so the
// receiver reports each link it is sent and goes on with the rest.
func (r rsyncRoot) Readlink(name string) (string, error) {
	return "", &fs.PathError{Op: "readlink", Path: name, Err: errors.ErrUnsupported}
}

func (r rsyncRoot) Symlink(_, newname string) error {
	return &fs.PathError{Op: "symlink", Path: newname, Err: errors.ErrUnsupported}
}

// Chmod and Lchown do nothing: Cog's files have no owners or modes of their
// own, and refusing would fail every rsync -a.
func (r rsyncRoot) Chmod(string, fs.FileMode) error { return nil }
func (r rsyncRoot) Lchown(string, int, int) error   { return nil }

// Chtimes keeps the sender's modification time, which is what lets the next
// run skip a file that has not changed.
func (r rsyncRoot) Chtimes(name string, atime, mtime time.Time) error {
	p, err := r.abs("chtimes", name)
	if err != nil {
		return err
	}
	return notExist(name, r.fsys.(vfs.AttrSetter).Chtimes(p, atime, mtime))
}

func (r rsyncRoot) FS() fs.FS { return rsyncView{r} }

func (r rsyncRoot) Sub(name string) (rsync.DestRoot, error) {
	p, err := r.abs("sub", name)
	if err != nil {
		return nil, err
	}
	fi, err := r.fsys.Stat(p)
	if err != nil {
		return nil, notExist(name, err)
	}
	if !fi.IsDir {
		return nil, &fs.PathError{Op: "sub", Path: name, Err: syscall.ENOTDIR}
	}
	return rsyncRoot{fsys: r.fsys, base: p}, nil
}

// --- fs.FS, which the sender reads and --delete walks ---

// rsyncView is a root as an fs.FS. It is a type of its own because fs.FS's
// Open and DestRoot's return different things.
type rsyncView struct{ r rsyncRoot }

func (v rsyncView) Stat(name string) (fs.FileInfo, error) { return v.r.Stat(name) }

func (v rsyncView) ReadDir(name string) ([]fs.DirEntry, error) {
	p, err := v.r.abs("readdir", name)
	if err != nil {
		return nil, err
	}
	entries, err := v.r.fsys.(vfs.DirReader).ReadDirEntries(p)
	return entries, notExist(name, err)
}

func (v rsyncView) Open(name string) (fs.File, error) {
	fi, err := v.Stat(name)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		entries, err := v.ReadDir(name)
		if err != nil {
			return nil, err
		}
		return &rsyncDir{info: fi, entries: entries}, nil
	}
	return v.r.openFile("open", name, os.O_RDONLY, 0)
}

type rsyncDir struct {
	info    fs.FileInfo
	entries []fs.DirEntry
}

func (d *rsyncDir) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *rsyncDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.Name(), Err: fs.ErrInvalid}
}
func (d *rsyncDir) Close() error { return nil }

func (d *rsyncDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		out := d.entries
		d.entries = nil
		return out, nil
	}
	if len(d.entries) == 0 {
		return nil, io.EOF
	}
	n = min(n, len(d.entries))
	out := d.entries[:n]
	d.entries = d.entries[n:]
	return out, nil
}

// notExist makes the VFS's not-found error one os.IsNotExist recognises,
// which is how the receiver tells a missing file from a failure;
// os.IsNotExist does not look through wrapped errors.
func notExist(name string, err error) error {
	if err == nil || os.IsNotExist(err) {
		return err
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, vfs.ErrNotFound) {
		return &fs.PathError{Op: "lookup", Path: name, Err: fs.ErrNotExist}
	}
	return err
}
