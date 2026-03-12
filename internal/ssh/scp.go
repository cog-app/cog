package ssh

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/vfs"

	gossh "golang.org/x/crypto/ssh"
)

// serveSCP handles an SCP exec request. Returns exit code.
func (s *Server) serveSCP(channel gossh.Channel, filesDir string, userFS vfs.FS, command string) int {
	// SCP relies heavily on os.FileInfo and direct file handles.
	// VFS integration for SCP is deferred — it uses the filesDir fallback.
	_ = userFS
	// Resolve symlinks for containment.
	if resolved, err := filepath.EvalSymlinks(filesDir); err == nil {
		filesDir = resolved
	}

	args := parseSCPArgs(command)

	var err error
	switch {
	case args.sink:
		err = scpSink(channel, filesDir, args)
	case args.source:
		err = scpSource(channel, filesDir, args)
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

// scpResolve maps a virtual SCP path to a sandboxed real path.
func scpResolve(filesDir, vpath string) (string, error) {
	cleaned := filepath.Clean("/" + vpath)
	if strings.HasPrefix(cleaned, "/files") {
		cleaned = strings.TrimPrefix(cleaned, "/files")
		if cleaned == "" {
			cleaned = "/"
		}
	}
	real := filepath.Join(filesDir, cleaned)

	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		parent, err2 := filepath.EvalSymlinks(filepath.Dir(real))
		if err2 != nil {
			return "", fmt.Errorf("path not found: %s", vpath)
		}
		resolved = filepath.Join(parent, filepath.Base(real))
	}

	if !strings.HasPrefix(resolved, filesDir) {
		return "", fmt.Errorf("access denied: path outside home directory")
	}
	return resolved, nil
}

// scpSink receives files from the client (scp -t).
func scpSink(rw io.ReadWriter, filesDir string, args scpArgs) error {
	targetPath, err := scpResolve(filesDir, args.path)
	if err != nil {
		return err
	}

	// Signal ready.
	if _, err := rw.Write([]byte{0}); err != nil {
		return err
	}

	reader := bufio.NewReader(rw)
	return scpSinkLoop(reader, rw, targetPath, filesDir, args.recursive)
}

func scpSinkLoop(reader *bufio.Reader, rw io.Writer, targetPath, filesDir string, recursive bool) error {
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
			if err := scpReceiveFile(reader, rw, targetPath, filesDir, line); err != nil {
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
			dirPath := filepath.Join(targetPath, dirName)
			if !strings.HasPrefix(dirPath, filesDir) {
				return fmt.Errorf("access denied: path outside home directory")
			}
			if err := os.MkdirAll(dirPath, 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", dirName, err)
			}
			if _, err := rw.Write([]byte{0}); err != nil {
				return err
			}
			if err := scpSinkLoop(reader, rw, dirPath, filesDir, recursive); err != nil {
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

func scpReceiveFile(reader *bufio.Reader, rw io.Writer, targetPath, filesDir, line string) error {
	// Parse "C<mode> <size> <name>"
	parts := strings.SplitN(line[1:], " ", 3)
	if len(parts) < 3 {
		return fmt.Errorf("malformed C line: %s", line)
	}

	mode, err := strconv.ParseUint(parts[0], 8, 32)
	if err != nil {
		return fmt.Errorf("parse mode: %w", err)
	}

	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return fmt.Errorf("parse size: %w", err)
	}

	name := parts[2]

	// Determine destination: if targetPath is a directory, write inside it.
	destPath := targetPath
	info, statErr := os.Stat(targetPath)
	if statErr == nil && info.IsDir() {
		destPath = filepath.Join(targetPath, name)
	}

	if !strings.HasPrefix(destPath, filesDir) {
		return fmt.Errorf("access denied: path outside home directory")
	}

	// Signal ready to receive file data.
	if _, err := rw.Write([]byte{0}); err != nil {
		return err
	}

	f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(mode))
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}

	_, copyErr := io.CopyN(f, reader, size)
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("receive %s: %w", name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", name, closeErr)
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
func scpSource(rw io.ReadWriter, filesDir string, args scpArgs) error {
	srcPath, err := scpResolve(filesDir, args.path)
	if err != nil {
		return err
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}

	reader := bufio.NewReader(rw)

	if info.IsDir() {
		if !args.recursive {
			return fmt.Errorf("%s is a directory (use -r)", args.path)
		}
		return scpSendDir(reader, rw, srcPath, info)
	}
	return scpSendFile(reader, rw, srcPath, info)
}

func scpSendFile(reader *bufio.Reader, rw io.Writer, path string, info os.FileInfo) error {
	header := fmt.Sprintf("C%04o %d %s\n", info.Mode().Perm(), info.Size(), info.Name())
	if _, err := rw.Write([]byte(header)); err != nil {
		return err
	}

	if err := scpReadAck(reader); err != nil {
		return err
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(rw, f); err != nil {
		return err
	}

	// Send trailing NUL.
	if _, err := rw.Write([]byte{0}); err != nil {
		return err
	}

	return scpReadAck(reader)
}

func scpSendDir(reader *bufio.Reader, rw io.Writer, dirPath string, info os.FileInfo) error {
	header := fmt.Sprintf("D%04o 0 %s\n", info.Mode().Perm(), info.Name())
	if _, err := rw.Write([]byte(header)); err != nil {
		return err
	}

	if err := scpReadAck(reader); err != nil {
		return err
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return err
	}

	for _, e := range entries {
		childPath := filepath.Join(dirPath, e.Name())
		childInfo, err := e.Info()
		if err != nil {
			continue
		}
		if childInfo.IsDir() {
			if err := scpSendDir(reader, rw, childPath, childInfo); err != nil {
				return err
			}
		} else {
			if err := scpSendFile(reader, rw, childPath, childInfo); err != nil {
				return err
			}
		}
	}

	// End directory.
	if _, err := rw.Write([]byte("E\n")); err != nil {
		return err
	}
	return scpReadAck(reader)
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
	case 1:
		msg, _ := reader.ReadString('\n')
		return fmt.Errorf("scp warning: %s", strings.TrimSpace(msg))
	case 2:
		msg, _ := reader.ReadString('\n')
		return fmt.Errorf("scp error: %s", strings.TrimSpace(msg))
	default:
		return fmt.Errorf("unexpected ack byte: %d", b)
	}
}
