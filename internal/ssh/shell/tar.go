package shell

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/vfs"
)

type tarOpts struct {
	mode     byte
	file     string
	gzip     bool
	bzip2    bool
	verbose  bool
	dir      string
	operands []string
}

// parseTarArgs reads tar's arguments, including the bundled first word of
// the traditional form ("tar czf out.tgz dir"), where f and C take the
// following words in order.
func parseTarArgs(args []string) (*tarOpts, error) {
	o := &tarOpts{file: "-"}
	setMode := func(m byte) error {
		if o.mode != 0 && o.mode != m {
			return errors.New("you may not specify more than one of -c, -x, -t")
		}
		o.mode = m
		return nil
	}
	i := 0
	next := func(flag string) (string, error) {
		if i >= len(args) {
			return "", fmt.Errorf("option requires an argument -- '%s'", flag)
		}
		i++
		return args[i-1], nil
	}
	letters := func(cluster string, bundled bool) error {
		for j := 0; j < len(cluster); j++ {
			c := cluster[j]
			switch c {
			case 'c', 'x', 't':
				if err := setMode(c); err != nil {
					return err
				}
			case 'v':
				o.verbose = true
			case 'z':
				o.gzip = true
			case 'j':
				o.bzip2 = true
			case 'p', 'o':
			case 'f', 'C':
				var v string
				if !bundled && j+1 < len(cluster) {
					v = cluster[j+1:]
					j = len(cluster)
				} else {
					var err error
					if v, err = next(string(c)); err != nil {
						return err
					}
				}
				if c == 'f' {
					o.file = v
				} else {
					o.dir = v
				}
			default:
				return fmt.Errorf("invalid option -- '%c'", c)
			}
		}
		return nil
	}
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		i = 1
		if err := letters(args[0], true); err != nil {
			return nil, err
		}
	}
	for i < len(args) {
		a := args[i]
		i++
		switch {
		case a == "--":
			o.operands = append(o.operands, args[i:]...)
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a[2:], "=")
			value := func() (string, error) {
				if hasVal {
					return val, nil
				}
				return next(name)
			}
			var err error
			switch name {
			case "create":
				err = setMode('c')
			case "extract", "get":
				err = setMode('x')
			case "list":
				err = setMode('t')
			case "verbose":
				o.verbose = true
			case "gzip", "gunzip":
				o.gzip = true
			case "bzip2":
				o.bzip2 = true
			case "file":
				o.file, err = value()
			case "directory":
				o.dir, err = value()
			default:
				err = fmt.Errorf("unrecognized option '%s'", a)
			}
			if err != nil {
				return nil, err
			}
		case len(a) > 1 && a[0] == '-':
			if err := letters(a[1:], false); err != nil {
				return nil, err
			}
		default:
			o.operands = append(o.operands, a)
		}
	}
	if o.mode == 0 {
		return nil, errors.New("you must specify one of the -c, -t, -x options")
	}
	return o, nil
}

func (s *Shell) newTarCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tar -c|-x|-t [-zjv] [-f archive] [-C dir] [path...]",
		Short: "Create, extract or list tar archives",
		Long: "Create, extract or list tar archives. Without -f the archive is stdin or stdout.\n" +
			"gzip and bzip2 archives are recognised when reading; -z compresses when creating.\n" +
			"Links and special files are skipped when extracting.",
		DisableFlagParsing: true,
		ValidArgsFunction:  s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, stdin, dir := getHC(cmd)
			if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
				return cmd.Help()
			}
			o, err := parseTarArgs(args)
			if err != nil {
				s.errorf(stderr, "tar: %v\n", err)
				return exitErr(2)
			}
			base := dir
			if o.dir != "" {
				base = s.toVirtualPath(dir, o.dir)
				fi, err := s.fs.Stat(base)
				if err == nil && !fi.IsDir {
					err = errors.New("not a directory")
				}
				if err != nil {
					s.errorf(stderr, "tar: %s: cannot change directory: %v\n", o.dir, err)
					return exitErr(2)
				}
			}
			t := &tarrer{s: s, o: o, stdout: stdout, stderr: stderr, base: base, buf: make([]byte, copyBufSize)}
			if o.mode == 'c' {
				return t.create(stdout, dir)
			}
			return t.read(stdin, dir)
		},
	}
}

type tarrer struct {
	s      *Shell
	o      *tarOpts
	stdout io.Writer
	stderr io.Writer
	base   string
	self   string
	buf    []byte
	failed bool
}

func (t *tarrer) result(err error) error {
	if err != nil {
		t.s.errorf(t.stderr, "tar: %v\n", err)
		return exitErr(2)
	}
	if t.failed {
		return exitErr(2)
	}
	return nil
}

func (t *tarrer) create(stdout io.Writer, cwd string) error {
	if t.o.bzip2 {
		return t.result(errors.New("bzip2 compression is not supported when creating archives"))
	}
	if len(t.o.operands) == 0 {
		return t.result(errors.New("refusing to create an empty archive"))
	}
	log := t.stdout
	if t.o.file == "-" {
		log = t.stderr
		w := bufio.NewWriterSize(stdout, copyBufSize)
		err := t.write(w, log)
		if err == nil {
			err = w.Flush()
		}
		return t.result(err)
	}
	t.self = t.s.toVirtualPath(cwd, t.o.file)
	return t.result(t.s.putStream(t.self, func(w io.Writer) error {
		return t.write(w, log)
	}))
}

func (t *tarrer) write(w io.Writer, log io.Writer) error {
	out := io.Writer(archiveWriter{w})
	var gz *gzip.Writer
	if t.o.gzip {
		gz = gzip.NewWriter(out)
		out = gz
	}
	tw := tar.NewWriter(out)
	stripped := false
	for _, op := range t.o.operands {
		vp := t.s.toVirtualPath(t.base, op)
		err := t.s.walk(vp, func(p string, fi *vfs.FileInfo, _ int) error {
			if p == t.self {
				return nil
			}
			name := memberName(t.base, p)
			if name == "" {
				return nil
			}
			if !stripped && !strings.HasPrefix(p, t.base+"/") && t.base != "/" {
				stripped = true
				t.s.errorf(t.stderr, "tar: Removing leading '/' from member names\n")
			}
			h := &tar.Header{Name: name, ModTime: fi.ModTime, Uname: t.s.username, Gname: t.s.username}
			if fi.IsDir {
				h.Typeflag, h.Name, h.Mode = tar.TypeDir, name+"/", 0o755
			} else {
				h.Typeflag, h.Size, h.Mode = tar.TypeReg, fi.Size, 0o644
			}
			if err := t.addEntry(tw, h, p); err != nil {
				return err
			}
			if t.o.verbose {
				fmt.Fprintln(log, h.Name)
			}
			return nil
		}, func(p string, err error) {
			t.s.errorf(t.stderr, "tar: %s: %v\n", p, err)
			t.failed = true
		})
		if errors.Is(err, errArchiveWrite) {
			return err
		}
		if err != nil {
			t.s.errorf(t.stderr, "tar: %s: %v\n", op, err)
			t.failed = true
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if gz != nil {
		return gz.Close()
	}
	return nil
}

// addEntry writes a header and, for a file, exactly the size it declared:
// the header is out before the content is read, so a file that shrank since
// it was listed is padded with zeros rather than leaving the archive corrupt.
func (t *tarrer) addEntry(tw *tar.Writer, h *tar.Header, p string) error {
	if h.Typeflag != tar.TypeReg {
		if err := tw.WriteHeader(h); err != nil {
			return fmt.Errorf("%w: %w", errArchiveWrite, err)
		}
		return nil
	}
	rc, err := t.s.fs.Get(p)
	if err != nil {
		t.s.errorf(t.stderr, "tar: %s: %v\n", p, err)
		t.failed = true
		return nil
	}
	if err := tw.WriteHeader(h); err != nil {
		return fmt.Errorf("%w: %w", errArchiveWrite, errors.Join(err, rc.Close()))
	}
	n, err := io.CopyBuffer(tw, readerOnly{io.LimitReader(rc, h.Size)}, t.buf)
	if cerr := rc.Close(); err == nil {
		err = cerr
	}
	if errors.Is(err, errArchiveWrite) {
		return err
	}
	if err == nil && n < h.Size {
		err = errors.New("file shrank while being archived")
	}
	if err != nil {
		t.s.errorf(t.stderr, "tar: %s: %v\n", p, err)
		t.failed = true
		if _, err := io.CopyBuffer(tw, readerOnly{io.LimitReader(zeros{}, h.Size-n)}, t.buf); err != nil {
			return err
		}
	}
	return nil
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func (t *tarrer) read(stdin io.Reader, cwd string) error {
	var src io.Reader = stdin
	if t.o.file != "-" {
		rc, err := t.s.fs.Get(t.s.toVirtualPath(cwd, t.o.file))
		if err != nil {
			return t.result(fmt.Errorf("%s: %w", t.o.file, err))
		}
		defer func() {
			if err := rc.Close(); err != nil {
				t.s.errorf(t.stderr, "tar: %s: %v\n", t.o.file, err)
			}
		}()
		src = rc
	}
	br := bufio.NewReaderSize(src, copyBufSize)
	dec, err := decompressed(br)
	if err != nil {
		return t.result(err)
	}
	tr := tar.NewReader(dec)
	matched := make([]bool, len(t.o.operands))
	var dirs *dirMaker
	if t.o.mode == 'x' {
		dirs = newDirMaker(t.s.fs)
	}
	stripped := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil && !errors.Is(err, tar.ErrInsecurePath) {
			return t.result(err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		i, ok := matchesMember(h.Name, t.o.operands)
		if !ok {
			continue
		}
		if i >= 0 {
			matched[i] = true
		}
		if t.o.mode == 't' {
			t.listEntry(h)
			continue
		}
		if !stripped && strings.HasPrefix(h.Name, "/") {
			stripped = true
			t.s.errorf(t.stderr, "tar: Removing leading '/' from member names\n")
		}
		if err := t.extractEntry(tr, h, dirs); err != nil {
			return t.result(err)
		}
	}
	for i, ok := range matched {
		if !ok {
			t.s.errorf(t.stderr, "tar: %s: not found in archive\n", t.o.operands[i])
			t.failed = true
		}
	}
	return t.result(nil)
}

// decompressed recognises gzip and bzip2 by their magic numbers, as GNU tar
// does when reading a file, so -z is not needed to read a .tgz.
func decompressed(br *bufio.Reader) (io.Reader, error) {
	magic, err := br.Peek(3)
	if err != nil && err != io.EOF {
		return nil, err
	}
	switch {
	case bytes.HasPrefix(magic, []byte{0x1f, 0x8b}):
		return gzip.NewReader(br)
	case bytes.HasPrefix(magic, []byte("BZh")):
		return bzip2.NewReader(br), nil
	}
	return br, nil
}

func (t *tarrer) listEntry(h *tar.Header) {
	if !t.o.verbose {
		fmt.Fprintln(t.stdout, h.Name)
		return
	}
	owner := h.Uname
	if owner == "" {
		owner = fmt.Sprint(h.Uid)
	}
	group := h.Gname
	if group == "" {
		group = fmt.Sprint(h.Gid)
	}
	fmt.Fprintf(t.stdout, "%s %s/%s %9d %s %s\n", h.FileInfo().Mode(), owner, group, h.Size,
		h.ModTime.Format("2006-01-02 15:04"), h.Name)
}

// extractEntry writes one member. A member that cannot be written is
// reported and the rest still go; only a broken archive is returned.
func (t *tarrer) extractEntry(tr *tar.Reader, h *tar.Header, dirs *dirMaker) error {
	name, err := safeMember(h.Name)
	if err != nil {
		t.s.errorf(t.stderr, "tar: %s: skipping: %v\n", h.Name, err)
		t.failed = true
		return nil
	}
	dst := path.Join(t.base, name)
	switch h.Typeflag {
	case tar.TypeDir:
		err = dirs.ensure(dst)
	case tar.TypeReg:
		if err = dirs.ensure(path.Dir(dst)); err == nil {
			err = t.s.fs.Put(dst, tr)
		}
		if errors.Is(err, tar.ErrHeader) || errors.Is(err, io.ErrUnexpectedEOF) {
			return err
		}
	default:
		t.s.errorf(t.stderr, "tar: %s: skipping: not a regular file or directory\n", name)
		return nil
	}
	if err != nil {
		t.s.errorf(t.stderr, "tar: %s: %v\n", name, err)
		t.failed = true
		return nil
	}
	if t.o.verbose {
		fmt.Fprintln(t.stdout, h.Name)
	}
	return nil
}
