package shell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"git.duckfam.us/jj-software/cog/vfs"
)

// copyBufSize is the buffer archive commands copy file contents through.
const copyBufSize = 64 << 10

// readerOnly hides a source's WriterTo, so io.CopyBuffer uses the caller's
// buffer: *os.File's WriteTo falls back to io.Copy, which allocates 32KiB
// per file.
type readerOnly struct{ io.Reader }

// windowReaderAt serves reads from a window read ahead of them. archive/zip
// inflates through a 4KiB bufio over its ReaderAt, which is a pread per 4KiB
// of a compressed member; extraction reads each member front to back.
type windowReaderAt struct {
	r   io.ReaderAt
	buf []byte
	off int64
	n   int
}

func newWindowReaderAt(r io.ReaderAt, size int) *windowReaderAt {
	return &windowReaderAt{r: r, buf: make([]byte, size)}
}

func (w *windowReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if len(p) >= len(w.buf) {
		return w.r.ReadAt(p, off)
	}
	if off < w.off || off+int64(len(p)) > w.off+int64(w.n) {
		n, err := w.r.ReadAt(w.buf, off)
		if err != nil && !(errors.Is(err, io.EOF) && n > 0) {
			w.n = 0
			return 0, err
		}
		w.off, w.n = off, n
	}
	n := copy(p, w.buf[off-w.off:w.n])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

var errArchiveWrite = errors.New("write archive")

// archiveWriter marks errors from writing the archive, which stop it, apart
// from errors reading a source, which leave that source out.
type archiveWriter struct{ w io.Writer }

func (a archiveWriter) Write(p []byte) (int, error) {
	n, err := a.w.Write(p)
	if err != nil {
		err = fmt.Errorf("%w: %w", errArchiveWrite, err)
	}
	return n, err
}

// putStream writes dst from what write produces, without holding it all: the
// VFS's Put reads a pipe that write fills. An error on either side ends both.
// write gets a buffered writer, which archive/zip adopts rather than adding
// its own 4KiB buffer; each pipe write is a hand-off between goroutines.
func (s *Shell) putStream(dst string, write func(io.Writer) error) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := s.fs.Put(dst, pr)
		if err != nil {
			pr.CloseWithError(err)
		} else {
			pr.Close()
		}
		done <- err
	}()
	bw := bufio.NewWriterSize(pw, copyBufSize)
	werr := write(bw)
	if werr == nil {
		werr = bw.Flush()
	}
	pw.CloseWithError(werr)
	putErr := <-done
	if werr != nil {
		return werr
	}
	return putErr
}

// memberName is how vp is named in an archive made from base: relative to
// base when under it, otherwise its absolute path without the leading slash.
// vp == base names the root of the archive, which is "".
func memberName(base, vp string) string {
	if vp == base {
		return ""
	}
	if base == "/" {
		return strings.TrimPrefix(vp, "/")
	}
	if rest, ok := strings.CutPrefix(vp, base+"/"); ok {
		return rest
	}
	return strings.TrimPrefix(vp, "/")
}

var errUnsafeMember = errors.New("path escapes the destination")

// safeMember cleans an archive member's name for extraction. Leading slashes
// are dropped, as tar and unzip do; a name that still climbs out of the
// destination is refused.
func safeMember(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimLeft(name, "/")
	if name == "" {
		return "", errUnsafeMember
	}
	if slices.Contains(strings.Split(name, "/"), "..") {
		return "", errUnsafeMember
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", errUnsafeMember
	}
	return clean, nil
}

// dirMaker makes directories for extraction, each once: Mkdir on the files
// plugin records the directory and its parents, so repeating it per file is
// a database round-trip per member.
type dirMaker struct {
	fs   vfs.FS
	made map[string]bool
}

func newDirMaker(fsys vfs.FS) *dirMaker {
	return &dirMaker{fs: fsys, made: map[string]bool{}}
}

func (d *dirMaker) ensure(dir string) error {
	if d.made[dir] {
		return nil
	}
	if err := d.fs.Mkdir(dir); err != nil {
		return err
	}
	for p := dir; !d.made[p]; p = path.Dir(p) {
		d.made[p] = true
		if p == "/" {
			break
		}
	}
	return nil
}

// matchesMember reports whether name was asked for: no patterns means
// everything, and a pattern matches its name, a glob of it, or a directory
// holding it.
func matchesMember(name string, patterns []string) (int, bool) {
	if len(patterns) == 0 {
		return -1, true
	}
	name = strings.TrimSuffix(name, "/")
	for i, p := range patterns {
		p = strings.TrimSuffix(p, "/")
		if name == p || strings.HasPrefix(name, p+"/") {
			return i, true
		}
		if ok, _ := path.Match(p, name); ok {
			return i, true
		}
	}
	return -1, false
}

// storedExts are formats that are already compressed; deflating them again
// costs CPU for a few bytes at best.
var storedExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".heic": true, ".heif": true, ".avif": true, ".jxl": true,
	".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true,
	".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".opus": true, ".flac": true,
	".zip": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true, ".zst": true,
	".7z": true, ".rar": true, ".jar": true, ".apk": true,
	".docx": true, ".xlsx": true, ".pptx": true, ".odt": true, ".ods": true, ".odp": true,
	".epub": true,
}

func alreadyCompressed(fi *vfs.FileInfo) bool {
	if storedExts[strings.ToLower(path.Ext(fi.Name))] {
		return true
	}
	mime, _, _ := strings.Cut(fi.MimeType, ";")
	switch {
	case mime == "image/svg+xml", mime == "image/bmp", mime == "image/tiff",
		mime == "audio/wav", mime == "audio/x-wav":
		return false
	case strings.HasPrefix(mime, "image/"), strings.HasPrefix(mime, "video/"), strings.HasPrefix(mime, "audio/"):
		return true
	}
	return false
}
