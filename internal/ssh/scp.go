package ssh

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/vfs"

	gossh "golang.org/x/crypto/ssh"
)

// serveSCP handles an SCP exec request on userFS. Returns exit code.
func (s *Server) serveSCP(channel gossh.Channel, userFS vfs.FS, command string) int {
	args := parseSCPArgs(command)

	var err error
	switch {
	case args.sink:
		err = scpSink(channel, userFS, args)
	case args.source:
		err = scpSource(channel, userFS, args)
	default:
		err = fmt.Errorf("scp: neither -t nor -f specified")
	}

	if err != nil {
		s.log.Debug("scp error", "err", err)
		fmt.Fprintf(channel.Stderr(), "scp: %v\n", err)
		return 1
	}
	return 0
}

type scpArgs struct {
	sink      bool // -t (to)
	source    bool // -f (from)
	recursive bool // -r
	path      string
}

func parseSCPArgs(command string) scpArgs {
	fields := strings.Fields(command)
	var a scpArgs
	for i := 1; i < len(fields); i++ {
		switch {
		case fields[i] == "-t":
			a.sink = true
		case fields[i] == "-f":
			a.source = true
		case fields[i] == "-r":
			a.recursive = true
		case strings.HasPrefix(fields[i], "-"):
			// Handle combined flags like -rt, -rf
			for _, c := range fields[i][1:] {
				switch c {
				case 't':
					a.sink = true
				case 'f':
					a.source = true
				case 'r':
					a.recursive = true
				}
			}
		default:
			a.path = fields[i]
		}
	}
	return a
}

// scpPath maps the path a client names to one in the session's VFS. The
// client's home is /files, so a relative path, and an absolute one outside
// /files, is taken to be within it.
func scpPath(p string) string {
	p = strings.TrimPrefix(p, "~")
	clean := path.Clean("/" + p)
	if clean == "/files" || strings.HasPrefix(clean, "/files/") {
		return clean
	}
	return path.Join("/files", clean)
}

// scpName checks a name the client sent for a file or directory: it must
// name an entry of the target directory, not reach elsewhere through it.
func scpName(name string) error {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return fmt.Errorf("invalid name %q", name)
	}
	return nil
}

// scpSink receives files from the client (scp -t).
func scpSink(rw io.ReadWriter, fsys vfs.FS, args scpArgs) error {
	// Signal ready.
	if _, err := rw.Write([]byte{0}); err != nil {
		return err
	}

	reader := bufio.NewReader(rw)
	return scpSinkLoop(reader, rw, fsys, scpPath(args.path), args.recursive)
}

func scpSinkLoop(reader *bufio.Reader, rw io.Writer, fsys vfs.FS, target string, recursive bool) error {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read control line: %w", err)
		}
		line = strings.TrimRight(line, "\n")

		if len(line) == 0 {
			continue
		}

		switch line[0] {
		case 'C':
			// C<mode> <size> <name>
			if err := scpReceiveFile(reader, rw, fsys, target, line); err != nil {
				return err
			}

		case 'D':
			// D<mode> 0 <dirname> — enter directory
			if !recursive {
				return fmt.Errorf("directory transfer requires -r")
			}
			parts := strings.SplitN(line[1:], " ", 3)
			if len(parts) < 3 {
				return fmt.Errorf("malformed D line: %s", line)
			}
			dirName := parts[2]
			if err := scpName(dirName); err != nil {
				return err
			}
			// Like the file case: into target if it is a directory,
			// otherwise as target.
			dir := target
			if fi, err := fsys.Stat(target); err == nil && fi.IsDir {
				dir = path.Join(target, dirName)
			} else if err != nil && !isNotExist(err) {
				return fmt.Errorf("stat %s: %w", target, err)
			}
			if err := fsys.Mkdir(dir); err != nil {
				return fmt.Errorf("mkdir %s: %w", dirName, err)
			}
			if _, err := rw.Write([]byte{0}); err != nil {
				return err
			}
			if err := scpSinkLoop(reader, rw, fsys, dir, recursive); err != nil {
				return err
			}

		case 'E':
			// E — leave directory
			if _, err := rw.Write([]byte{0}); err != nil {
				return err
			}
			return nil

		case '\x01', '\x02':
			// Warning or error from client.
			return fmt.Errorf("client error: %s", line[1:])

		default:
			return fmt.Errorf("unknown SCP control: %q", line)
		}
	}
}

func scpReceiveFile(reader *bufio.Reader, rw io.Writer, fsys vfs.FS, target, line string) error {
	// Parse "C<mode> <size> <name>". The mode is checked but not kept:
	// files take their permissions from the store, not the client.
	parts := strings.SplitN(line[1:], " ", 3)
	if len(parts) < 3 {
		return fmt.Errorf("malformed C line: %s", line)
	}

	if _, err := strconv.ParseUint(parts[0], 8, 32); err != nil {
		return fmt.Errorf("parse mode: %w", err)
	}

	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return fmt.Errorf("parse size: %w", err)
	}

	name := parts[2]
	if size < 0 {
		return fmt.Errorf("negative size for %s", name)
	}
	if err := scpName(name); err != nil {
		return err
	}

	// Determine destination: if target is a directory, write inside it.
	dest := target
	if fi, err := fsys.Stat(target); err == nil && fi.IsDir {
		dest = path.Join(target, name)
	} else if err != nil && !isNotExist(err) {
		return fmt.Errorf("stat %s: %w", target, err)
	}

	// Signal ready to receive file data.
	if _, err := rw.Write([]byte{0}); err != nil {
		return err
	}

	// The length is known, so a file over quota is refused before any of
	// it is written.
	body := &io.LimitedReader{R: reader, N: size}
	if err := fsys.Put(dest, body); err != nil {
		return fmt.Errorf("receive %s: %w", name, err)
	}
	if body.N > 0 {
		return fmt.Errorf("receive %s: %w", name, io.ErrUnexpectedEOF)
	}

	// Read trailing NUL byte.
	b := make([]byte, 1)
	if _, err := io.ReadFull(reader, b); err != nil {
		return fmt.Errorf("read ack: %w", err)
	}

	// Acknowledge.
	if _, err := rw.Write([]byte{0}); err != nil {
		return err
	}

	return nil
}

// scpSource sends files to the client (scp -f).
func scpSource(rw io.ReadWriter, fsys vfs.FS, args scpArgs) error {
	src := scpPath(args.path)
	info, err := fsys.Stat(src)
	if err != nil {
		return err
	}

	reader := bufio.NewReader(rw)

	if info.IsDir {
		if !args.recursive {
			return fmt.Errorf("%s is a directory (use -r)", args.path)
		}
		return scpSendDir(reader, rw, fsys, src)
	}
	return scpSendFile(reader, rw, fsys, src, info.Size)
}

func scpSendFile(reader *bufio.Reader, rw io.Writer, fsys vfs.FS, p string, size int64) error {
	header := fmt.Sprintf("C0644 %d %s\n", size, path.Base(p))
	if _, err := rw.Write([]byte(header)); err != nil {
		return err
	}

	if err := scpReadAck(reader); err != nil {
		return err
	}

	f, err := fsys.Get(p)
	if err != nil {
		return err
	}
	defer f.Close()

	// The header promised size bytes; a file that changed since cannot
	// send a different number without corrupting the stream.
	if _, err := io.CopyN(rw, f, size); err != nil {
		return fmt.Errorf("send %s: %w", p, err)
	}

	// Send trailing NUL.
	if _, err := rw.Write([]byte{0}); err != nil {
		return err
	}

	return scpReadAck(reader)
}

func scpSendDir(reader *bufio.Reader, rw io.Writer, fsys vfs.FS, dir string) error {
	header := fmt.Sprintf("D0755 0 %s\n", path.Base(dir))
	if _, err := rw.Write([]byte(header)); err != nil {
		return err
	}

	if err := scpReadAck(reader); err != nil {
		return err
	}

	entries, err := fsys.List(dir)
	if err != nil {
		return err
	}

	for _, e := range entries {
		child := path.Join(dir, e.Name)
		if e.IsDir {
			err = scpSendDir(reader, rw, fsys, child)
		} else {
			err = scpSendFile(reader, rw, fsys, child, e.Size)
		}
		if err != nil {
			return err
		}
	}

	// End directory.
	if _, err := rw.Write([]byte("E\n")); err != nil {
		return err
	}
	return scpReadAck(reader)
}

// isNotExist reports whether err means the path does not exist.
func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, vfs.ErrNotFound)
}

// scpReadAck reads an SCP acknowledgment byte from the client.
func scpReadAck(reader *bufio.Reader) error {
	b, err := reader.ReadByte()
	if err != nil {
		return fmt.Errorf("read ack: %w", err)
	}
	switch b {
	case 0:
		return nil
	case 1, 2:
		kind := "warning"
		if b == 2 {
			kind = "error"
		}
		msg, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("scp %s: %s (message cut short: %w)", kind, strings.TrimSpace(msg), err)
		}
		return fmt.Errorf("scp %s: %s", kind, strings.TrimSpace(msg))
	default:
		return fmt.Errorf("unexpected ack byte: %d", b)
	}
}
