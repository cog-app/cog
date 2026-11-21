package shell

import (
	"bufio"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

func (s *Shell) newSha256sumCmd() *cobra.Command {
	return s.newSumCmd("sha256sum", "SHA256", sha256.New)
}
func (s *Shell) newSha1sumCmd() *cobra.Command { return s.newSumCmd("sha1sum", "SHA1", sha1.New) }
func (s *Shell) newMd5sumCmd() *cobra.Command  { return s.newSumCmd("md5sum", "MD5", md5.New) }

func (s *Shell) newSumCmd(name, algo string, newHash func() hash.Hash) *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:               name + " [flags] [file...]",
		Short:             "Compute or check " + algo + " checksums",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, stdin, dir := getHC(cmd)
			if len(args) == 0 {
				args = []string{"-"}
			}
			sr := &sumRun{s: s, name: name, h: newHash(), buf: make([]byte, 128<<10), stdin: stdin, stderr: stderr, dir: dir, out: bufio.NewWriter(stdout)}
			ok := true
			for _, a := range args {
				if check {
					ok = sr.check(a) && ok
				} else {
					ok = sr.print(a) && ok
				}
			}
			if err := sr.out.Flush(); err != nil {
				return err
			}
			if !ok {
				return exitErr(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&check, "check", "c", false, "Read checksums from the files and check them")
	return cmd
}

type sumRun struct {
	s      *Shell
	name   string
	h      hash.Hash
	buf    []byte
	stdin  io.Reader
	stderr io.Writer
	dir    string
	out    *bufio.Writer
}

func (r *sumRun) open(name string) (io.ReadCloser, error) {
	if name == "-" {
		return io.NopCloser(r.stdin), nil
	}
	return r.s.fs.Get(r.s.toVirtualPath(r.dir, name))
}

// sum hashes name with the run's hash and buffer, both reused across files.
func (r *sumRun) sum(name string) (string, error) {
	rc, err := r.open(name)
	if err != nil {
		return "", err
	}
	r.h.Reset()
	// Hiding WriterTo keeps io.CopyBuffer on the run's buffer: *os.File's
	// WriteTo would allocate its own for every file.
	_, err = io.CopyBuffer(r.h, struct{ io.Reader }{rc}, r.buf)
	if cerr := rc.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(r.h.Sum(nil)), nil
}

func (r *sumRun) print(name string) bool {
	sum, err := r.sum(name)
	if err != nil {
		r.s.errorf(r.stderr, "%s: %s: %v\n", r.name, name, err)
		return false
	}
	r.out.WriteString(sum)
	r.out.WriteString("  ")
	r.out.WriteString(name)
	r.out.WriteByte('\n')
	return true
}

// check verifies each "<sum>  <name>" line of list, as written by print or
// by GNU coreutils (whose binary-mode lines use " *" for the separator).
func (r *sumRun) check(list string) bool {
	rc, err := r.open(list)
	if err != nil {
		r.s.errorf(r.stderr, "%s: %s: %v\n", r.name, list, err)
		return false
	}
	defer rc.Close()
	want := r.h.Size() * 2
	var failed, unreadable, malformed int
	sc := bufio.NewScanner(rc)
	for sc.Scan() {
		line := sc.Text()
		if len(line) < want+2 || (line[want] != ' ') || (line[want+1] != ' ' && line[want+1] != '*') {
			malformed++
			continue
		}
		expected, name := strings.ToLower(line[:want]), line[want+2:]
		got, err := r.sum(name)
		switch {
		case err != nil:
			r.s.errorf(r.stderr, "%s: %s: %v\n", r.name, name, err)
			fmt.Fprintf(r.out, "%s: FAILED open or read\n", name)
			unreadable++
		case got == expected:
			fmt.Fprintf(r.out, "%s: OK\n", name)
		default:
			fmt.Fprintf(r.out, "%s: FAILED\n", name)
			failed++
		}
	}
	if err := sc.Err(); err != nil {
		r.s.errorf(r.stderr, "%s: %s: %v\n", r.name, list, err)
		return false
	}
	if err := r.out.Flush(); err != nil {
		r.s.errorf(r.stderr, "%s: %v\n", r.name, err)
		return false
	}
	warn := func(n int, what string) {
		if n == 0 {
			return
		}
		plural := "s"
		if n == 1 {
			plural = ""
		}
		r.s.errorf(r.stderr, "%s: WARNING: %d %s\n", r.name, n, fmt.Sprintf(what, plural))
	}
	warn(malformed, "line%s improperly formatted")
	warn(unreadable, "listed file%s could not be read")
	warn(failed, "computed checksum%s did NOT match")
	return failed == 0 && unreadable == 0
}
