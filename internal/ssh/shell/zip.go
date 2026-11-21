package shell

import (
	"archive/zip"
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/vfs"
)

func (s *Shell) newZipCmd() *cobra.Command {
	var recursive, quiet bool
	cmd := &cobra.Command{
		Use:               "zip [flags] <archive.zip> <path...>",
		Short:             "Package files into a zip archive",
		Long:              "Package files into a zip archive. An existing archive is replaced, not updated.\nAn archive of - is written to stdout.",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, dir := getHC(cmd)
			archive := args[0]
			z := &zipper{s: s, stderr: stderr, base: dir, recursive: recursive, buf: make([]byte, copyBufSize)}
			if !quiet {
				z.log = stdout
			}
			if archive == "-" {
				z.log = nil
				if !quiet {
					z.log = stderr
				}
				w := bufio.NewWriterSize(stdout, copyBufSize)
				err := z.write(w, args[1:])
				if err == nil {
					err = w.Flush()
				}
				return z.finish(err)
			}
			if path.Ext(archive) == "" {
				archive += ".zip"
			}
			z.self = s.toVirtualPath(dir, archive)
			return z.finish(s.putStream(z.self, func(w io.Writer) error {
				return z.write(w, args[1:])
			}))
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recurse-paths", "r", false, "Travel the directory structure recursively")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Quiet operation")
	return cmd
}

type zipper struct {
	s         *Shell
	stderr    io.Writer
	log       io.Writer
	base      string
	self      string
	recursive bool
	buf       []byte
	failed    bool

	pending *zip.FileHeader
}

func (z *zipper) finish(err error) error {
	if err != nil {
		z.s.errorf(z.stderr, "zip: %v\n", err)
		return exitErr(2)
	}
	if z.failed {
		return exitErr(1)
	}
	return nil
}

// write streams the archive to w. A source that cannot be read is reported
// and left out; a failure writing the archive stops it.
func (z *zipper) write(w io.Writer, paths []string) error {
	zw := zip.NewWriter(w)
	for _, p := range paths {
		vp := z.s.toVirtualPath(z.base, p)
		err := z.s.walk(vp, func(p string, fi *vfs.FileInfo, depth int) error {
			if p == z.self {
				return nil
			}
			if fi.IsDir {
				if name := memberName(z.base, p); name != "" {
					if err := z.add(zw, p, name+"/", fi); err != nil {
						return err
					}
				}
				if !z.recursive && depth == 0 {
					return errSkipDir
				}
				return nil
			}
			return z.add(zw, p, memberName(z.base, p), fi)
		}, func(p string, err error) {
			z.s.errorf(z.stderr, "zip: %s: %v\n", p, err)
			z.failed = true
		})
		if errors.Is(err, errArchiveWrite) {
			return err
		}
		if err != nil {
			z.s.errorf(z.stderr, "zip: %s: %v\n", p, err)
			z.failed = true
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	z.logPending()
	return nil
}

// add writes the entry name for p. A file that cannot be opened is reported
// and left out; one that fails partway is reported, and what was read of it
// stays in the archive.
func (z *zipper) add(zw *zip.Writer, p, name string, fi *vfs.FileInfo) error {
	var rc io.ReadCloser
	h := &zip.FileHeader{Name: name, Method: zip.Store, Modified: fi.ModTime}
	if !fi.IsDir {
		var err error
		if rc, err = z.s.fs.Get(p); err != nil {
			z.s.errorf(z.stderr, "zip: %s: %v\n", p, err)
			z.failed = true
			return nil
		}
		if !alreadyCompressed(fi) {
			h.Method = zip.Deflate
		}
		h.UncompressedSize64 = uint64(fi.Size)
	}
	h.SetMode(zipMode(fi))
	if h.Modified.IsZero() {
		h.Modified = time.Now()
	}
	dst, err := zw.CreateHeader(h)
	if err != nil {
		if rc != nil {
			err = errors.Join(err, rc.Close())
		}
		return fmt.Errorf("%w: %w", errArchiveWrite, err)
	}
	z.logPending()
	z.pending = h
	if rc == nil {
		return nil
	}
	_, err = io.CopyBuffer(archiveWriter{dst}, readerOnly{rc}, z.buf)
	if cerr := rc.Close(); err == nil {
		err = cerr
	}
	if errors.Is(err, errArchiveWrite) {
		return err
	}
	if err != nil {
		z.s.errorf(z.stderr, "zip: %s: %v\n", p, err)
		z.failed = true
	}
	return nil
}

// logPending reports the entry before the one being written: a zip entry's
// compressed size is known only once the next one starts.
func (z *zipper) logPending() {
	h := z.pending
	z.pending = nil
	if h == nil || z.log == nil {
		return
	}
	if h.Method == zip.Store {
		fmt.Fprintf(z.log, "  adding: %s (stored 0%%)\n", h.Name)
		return
	}
	ratio := 0
	if h.UncompressedSize64 > 0 {
		ratio = 100 - int(h.CompressedSize64*100/h.UncompressedSize64)
	}
	fmt.Fprintf(z.log, "  adding: %s (deflated %d%%)\n", h.Name, ratio)
}

func zipMode(fi *vfs.FileInfo) fs.FileMode {
	if fi.IsDir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}

func (s *Shell) newUnzipCmd() *cobra.Command {
	var list, overwrite, quiet bool
	var exdir string
	cmd := &cobra.Command{
		Use:   "unzip [flags] <archive.zip> [member...]",
		Short: "List or extract files from a zip archive",
		Long: "List or extract files from a zip archive. Existing files are skipped unless -o is given;\n" +
			"there is no prompt.",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, dir := getHC(cmd)
			opener, ok := s.fs.(vfs.FileOpener)
			if !ok {
				s.errorf(stderr, "unzip: %v\n", errors.ErrUnsupported)
				return exitErr(1)
			}
			vp := s.toVirtualPath(dir, args[0])
			f, err := opener.OpenFile(vp, os.O_RDONLY, 0)
			if err != nil && isNotExist(err) && path.Ext(vp) == "" {
				vp += ".zip"
				f, err = opener.OpenFile(vp, os.O_RDONLY, 0)
			}
			if err != nil {
				s.errorf(stderr, "unzip: %s: %v\n", args[0], err)
				return exitErr(9)
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				s.errorf(stderr, "unzip: %s: %v\n", args[0], err)
				return exitErr(9)
			}
			// Each member's name is checked by safeMember before it is
			// written, which is what ErrInsecurePath warns about.
			zr, err := zip.NewReader(newWindowReaderAt(f, copyBufSize), st.Size())
			if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
				s.errorf(stderr, "unzip: %s: %v\n", args[0], err)
				return exitErr(9)
			}
			u := &unzipper{s: s, stdout: stdout, stderr: stderr, quiet: quiet, overwrite: overwrite,
				patterns: args[1:], buf: make([]byte, copyBufSize)}
			if list {
				return u.list(zr)
			}
			dest := dir
			if exdir != "" {
				dest = s.toVirtualPath(dir, exdir)
			}
			if !quiet {
				fmt.Fprintf(stdout, "Archive:  %s\n", args[0])
			}
			return u.extract(zr, dest)
		},
	}
	cmd.Flags().BoolVarP(&list, "list", "l", false, "List archive contents")
	cmd.Flags().BoolVarP(&overwrite, "overwrite", "o", false, "Overwrite existing files")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Quiet operation")
	cmd.Flags().StringVarP(&exdir, "directory", "d", "", "Extract into this directory")
	return cmd
}

type unzipper struct {
	s         *Shell
	stdout    io.Writer
	stderr    io.Writer
	quiet     bool
	overwrite bool
	patterns  []string
	buf       []byte

	// existing holds the names in each destination directory, listed once
	// when the first member lands there.
	existing map[string]map[string]bool
}

func (u *unzipper) list(zr *zip.Reader) error {
	fmt.Fprintln(u.stdout, "  Length      Date    Time    Name")
	fmt.Fprintln(u.stdout, "---------  ---------- -----   ----")
	var total uint64
	n := 0
	for _, f := range zr.File {
		if _, ok := matchesMember(f.Name, u.patterns); !ok {
			continue
		}
		fmt.Fprintf(u.stdout, "%9d  %s   %s\n", f.UncompressedSize64, f.Modified.Format("2006-01-02 15:04"), f.Name)
		total += f.UncompressedSize64
		n++
	}
	fmt.Fprintln(u.stdout, "---------                     -------")
	unit := "files"
	if n == 1 {
		unit = "file"
	}
	fmt.Fprintf(u.stdout, "%9d                     %d %s\n", total, n, unit)
	return nil
}

func (u *unzipper) extract(zr *zip.Reader, dest string) error {
	dirs := newDirMaker(u.s.fs)
	if err := dirs.ensure(dest); err != nil {
		u.s.errorf(u.stderr, "unzip: %s: %v\n", dest, err)
		return exitErr(50)
	}
	failed := false
	matched := make([]bool, len(u.patterns))
	for _, f := range zr.File {
		i, ok := matchesMember(f.Name, u.patterns)
		if !ok {
			continue
		}
		if i >= 0 {
			matched[i] = true
		}
		name, err := safeMember(f.Name)
		if err != nil {
			u.s.errorf(u.stderr, "unzip: skipping %s: %v\n", f.Name, err)
			failed = true
			continue
		}
		dst := path.Join(dest, name)
		mode := f.Mode()
		switch {
		case mode.IsDir() || strings.HasSuffix(f.Name, "/"):
			if err := dirs.ensure(dst); err != nil {
				u.s.errorf(u.stderr, "unzip: %s: %v\n", name, err)
				failed = true
				continue
			}
			u.logf("   creating: %s/\n", name)
		case !mode.IsRegular():
			u.s.errorf(u.stderr, "unzip: skipping %s: not a regular file\n", name)
			failed = true
		default:
			wrote, err := u.extractFile(f, dirs, dst)
			if err != nil {
				u.s.errorf(u.stderr, "unzip: %s: %v\n", name, err)
				failed = true
				continue
			}
			if !wrote {
				u.logf("  skipping: %s (exists; use -o to overwrite)\n", name)
				continue
			}
			verb := "  inflating"
			if f.Method == zip.Store {
				verb = " extracting"
			}
			u.logf("%s: %s\n", verb, name)
		}
	}
	for i, ok := range matched {
		if !ok {
			u.s.errorf(u.stderr, "unzip: %s: not found in archive\n", u.patterns[i])
			failed = true
		}
	}
	if failed {
		return exitErr(1)
	}
	return nil
}

func (u *unzipper) logf(format string, a ...any) {
	if !u.quiet {
		fmt.Fprintf(u.stdout, format, a...)
	}
}

func (u *unzipper) extractFile(f *zip.File, dirs *dirMaker, dst string) (bool, error) {
	parent := path.Dir(dst)
	if err := dirs.ensure(parent); err != nil {
		return false, err
	}
	if !u.overwrite {
		exists, err := u.exists(parent, path.Base(dst))
		if err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
	}
	rc, err := f.Open()
	if err != nil {
		return false, err
	}
	err = u.s.fs.Put(dst, rc)
	if cerr := rc.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if u.existing != nil && u.existing[parent] != nil {
		u.existing[parent][path.Base(dst)] = true
	}
	return true, nil
}

func (u *unzipper) exists(dir, name string) (bool, error) {
	if u.existing == nil {
		u.existing = map[string]map[string]bool{}
	}
	names, ok := u.existing[dir]
	if !ok {
		entries, err := u.s.fs.List(dir)
		if err != nil && !isNotExist(err) {
			return false, err
		}
		names = make(map[string]bool, len(entries))
		for _, e := range entries {
			names[e.Name] = true
		}
		u.existing[dir] = names
	}
	return names[name], nil
}
