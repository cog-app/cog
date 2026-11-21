package shell

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/vfs"
)

type wcCounts struct{ lines, words, chars, bytes int64 }

func (c *wcCounts) add(o wcCounts) {
	c.lines += o.lines
	c.words += o.words
	c.chars += o.chars
	c.bytes += o.bytes
}

// wcSpace is 1 for the bytes C's isspace accepts.
var wcSpace = [256]byte{' ': 1, '\t': 1, '\n': 1, '\v': 1, '\f': 1, '\r': 1}

// utf8Continuations counts bytes of the form 10xxxxxx, eight at a time.
func utf8Continuations(p []byte) int {
	n := 0
	for len(p) >= 8 {
		x := binary.LittleEndian.Uint64(p)
		n += bits.OnesCount64(x &^ (x << 1) & 0x8080808080808080)
		p = p[8:]
	}
	for _, b := range p {
		if b&0xC0 == 0x80 {
			n++
		}
	}
	return n
}

// wcCount streams r through buf, so a file of any size costs one buffer.
// Words are runs of non-space bytes; characters are UTF-8 sequence starts.
func wcCount(r io.Reader, buf []byte, words, chars bool) (wcCounts, error) {
	var c wcCounts
	inWord := false
	for {
		n, err := r.Read(buf)
		chunk := buf[:n]
		c.bytes += int64(n)
		c.lines += int64(bytes.Count(chunk, []byte{'\n'}))
		if words {
			var prev byte = 1
			if inWord {
				prev = 0
			}
			var starts int64
			for _, b := range chunk {
				cur := wcSpace[b]
				starts += int64(prev &^ cur)
				prev = cur
			}
			c.words += starts
			inWord = prev == 0
		}
		if chars {
			c.chars += int64(n - utf8Continuations(chunk))
		}
		if errors.Is(err, io.EOF) {
			return c, nil
		}
		if err != nil {
			return c, err
		}
	}
}

func (s *Shell) newWcCmd() *cobra.Command {
	var lines, words, chars, byteCount bool
	cmd := &cobra.Command{
		Use:               "wc [flags] [file...]",
		Short:             "Count lines, words and bytes",
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, stdin, dir := getHC(cmd)
			if !lines && !words && !chars && !byteCount {
				lines, words, byteCount = true, true, true
			}
			fromStdin := len(args) == 0
			if fromStdin {
				args = []string{"-"}
			}

			// GNU pads to the width of the total size when every input is a
			// file, and to 7 when one is a stream it cannot size up front.
			fields := 0
			for _, f := range []bool{lines, words, chars, byteCount} {
				if f {
					fields++
				}
			}
			infos := make([]*vfs.FileInfo, len(args))
			var total int64
			sized := true
			for i, a := range args {
				if a == "-" {
					sized = false
					continue
				}
				if fi, err := s.fs.Stat(s.toVirtualPath(dir, a)); err == nil {
					infos[i] = fi
					total += fi.Size
				}
			}
			width := 1
			switch {
			case fields == 1 && len(args) == 1:
			case !sized:
				width = 7
			default:
				width = len(strconv.FormatInt(total, 10))
			}

			out := bufio.NewWriter(stdout)
			write := func(c wcCounts, name string) {
				sep := ""
				for _, f := range []struct {
					on bool
					n  int64
				}{{lines, c.lines}, {words, c.words}, {chars, c.chars}, {byteCount, c.bytes}} {
					if f.on {
						fmt.Fprintf(out, "%s%*d", sep, width, f.n)
						sep = " "
					}
				}
				if name != "" {
					out.WriteString(" " + name)
				}
				out.WriteByte('\n')
			}

			buf := make([]byte, 128<<10)
			var sum wcCounts
			ok := true
			for i, a := range args {
				var r io.Reader
				var closer io.Closer
				if a == "-" {
					r = stdin
				} else {
					if fi := infos[i]; fi != nil && fi.IsDir {
						s.errorf(stderr, "wc: %s: Is a directory\n", a)
						write(wcCounts{}, a)
						ok = false
						continue
					}
					rc, err := s.fs.Get(s.toVirtualPath(dir, a))
					if err != nil {
						s.errorf(stderr, "wc: %s: %v\n", a, err)
						ok = false
						continue
					}
					r, closer = rc, rc
				}
				c, err := wcCount(r, buf, words, chars)
				if closer != nil {
					if cerr := closer.Close(); err == nil {
						err = cerr
					}
				}
				if err != nil {
					s.errorf(stderr, "wc: %s: %v\n", a, err)
					ok = false
					continue
				}
				sum.add(c)
				name := a
				if fromStdin {
					name = ""
				}
				write(c, name)
			}
			if len(args) > 1 {
				write(sum, "total")
			}
			if err := out.Flush(); err != nil {
				return err
			}
			if !ok {
				return exitErr(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&lines, "lines", "l", false, "Print the newline count")
	cmd.Flags().BoolVarP(&words, "words", "w", false, "Print the word count")
	cmd.Flags().BoolVarP(&chars, "chars", "m", false, "Print the character count")
	cmd.Flags().BoolVarP(&byteCount, "bytes", "c", false, "Print the byte count")
	return cmd
}

func fileType(fi *vfs.FileInfo) string {
	switch {
	case fi.IsDir:
		return "directory"
	case fi.Size == 0:
		return "regular empty file"
	default:
		return "regular file"
	}
}

const statTimeFormat = "2006-01-02 15:04:05.000000000 -0700"

func (s *Shell) newStatCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:               "stat [flags] <file...>",
		Short:             "Display file status",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, dir := getHC(cmd)
			out := bufio.NewWriter(stdout)
			ok := true
			for _, a := range args {
				fi, err := s.fs.Stat(s.toVirtualPath(dir, a))
				if err != nil {
					s.errorf(stderr, "stat: %s: %v\n", a, err)
					ok = false
					continue
				}
				if format != "" {
					out.WriteString(statFormat(format, a, fi))
					out.WriteByte('\n')
					continue
				}
				fmt.Fprintf(out, "  File: %s\n  Size: %-15d Type: %s\n", a, fi.Size, fileType(fi))
				if fi.MimeType != "" {
					fmt.Fprintf(out, "  MIME: %s\n", fi.MimeType)
				}
				fmt.Fprintf(out, "Modify: %s\n", fi.ModTime.Format(statTimeFormat))
			}
			if err := out.Flush(); err != nil {
				return err
			}
			if !ok {
				return exitErr(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&format, "format", "c", "", "Use FORMAT: %n name, %s size, %F type, %y modified, %Y modified as seconds, %m MIME type")
	return cmd
}

func statFormat(format, name string, fi *vfs.FileInfo) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 == len(format) {
			b.WriteByte(format[i])
			continue
		}
		i++
		switch format[i] {
		case 'n':
			b.WriteString(name)
		case 's':
			b.WriteString(strconv.FormatInt(fi.Size, 10))
		case 'F':
			b.WriteString(fileType(fi))
		case 'y':
			b.WriteString(fi.ModTime.Format(statTimeFormat))
		case 'Y':
			b.WriteString(strconv.FormatInt(fi.ModTime.Unix(), 10))
		case 'm':
			b.WriteString(fi.MimeType)
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

func basename(name, suffix string) string {
	trimmed := strings.TrimRight(name, "/")
	if trimmed == "" {
		if name == "" {
			return ""
		}
		return "/"
	}
	base := path.Base(trimmed)
	if suffix != "" && base != suffix {
		base = strings.TrimSuffix(base, suffix)
	}
	return base
}

func (s *Shell) newBasenameCmd() *cobra.Command {
	var multiple bool
	var suffix string
	cmd := &cobra.Command{
		Use:   "basename [flags] <name> [suffix]",
		Short: "Strip directory and suffix from file names",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, _ := getHC(cmd)
			if suffix != "" {
				multiple = true
			}
			if !multiple {
				if len(args) > 2 {
					s.errorf(stderr, "basename: extra operand %s\n", args[2])
					return exitErr(1)
				}
				if len(args) == 2 {
					suffix = args[1]
				}
				args = args[:1]
			}
			out := bufio.NewWriter(stdout)
			for _, a := range args {
				out.WriteString(basename(a, suffix))
				out.WriteByte('\n')
			}
			return out.Flush()
		},
	}
	cmd.Flags().BoolVarP(&multiple, "multiple", "a", false, "Support multiple arguments and treat each as a name")
	cmd.Flags().StringVarP(&suffix, "suffix", "s", "", "Remove a trailing suffix; implies -a")
	return cmd
}

// sniffType names a file's content type from what the VFS recorded, or from
// its first 512 bytes, all http.DetectContentType looks at.
func (s *Shell) sniffType(vp string, fi *vfs.FileInfo, head []byte) (string, error) {
	switch {
	case fi.IsDir:
		return "directory", nil
	case fi.Size == 0:
		return "inode/x-empty", nil
	case fi.MimeType != "":
		return fi.MimeType, nil
	}
	rc, err := s.fs.Get(vp)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	n, err := io.ReadFull(rc, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	return http.DetectContentType(head[:n]), nil
}

func (s *Shell) newFileCmd() *cobra.Command {
	var brief bool
	cmd := &cobra.Command{
		Use:               "file [flags] <file...>",
		Short:             "Determine file type",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr, _, dir := getHC(cmd)
			out := bufio.NewWriter(stdout)
			ok := true
			head := make([]byte, 512)
			for _, a := range args {
				vp := s.toVirtualPath(dir, a)
				fi, err := s.fs.Stat(vp)
				var kind string
				if err == nil {
					kind, err = s.sniffType(vp, fi, head)
				}
				if err != nil {
					s.errorf(stderr, "file: %s: %v\n", a, err)
					ok = false
					continue
				}
				if !brief {
					out.WriteString(a + ": ")
				}
				out.WriteString(kind)
				out.WriteByte('\n')
			}
			if err := out.Flush(); err != nil {
				return err
			}
			if !ok {
				return exitErr(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&brief, "brief", "b", false, "Do not prepend file names")
	cmd.Flags().Bool("mime-type", true, "Print the MIME type (always on)")
	return cmd
}
