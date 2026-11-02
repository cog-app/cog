package ssh

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
	"git.duckfam.us/jj-software/cog/vfs"

	gossh "golang.org/x/crypto/ssh"
)

// authKeysFileHandler implements vfs.FileHandler for /.ssh/authorized_keys,
// backed by the SSH key store database.
type authKeysFileHandler struct {
	keys     *keys.Store
	username string
}

func newAuthKeysFileHandler(ks *keys.Store, username string) *authKeysFileHandler {
	return &authKeysFileHandler{keys: ks, username: username}
}

func (h *authKeysFileHandler) Stat() (fs.FileInfo, error) {
	content, err := h.renderContent()
	if err != nil {
		return nil, err
	}
	return &authKeysFileInfo{size: int64(len(content))}, nil
}

func (h *authKeysFileHandler) Open(flag int, perm os.FileMode) (vfs.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND) != 0 {
		return &authKeysFile{handler: h, buf: &bytes.Buffer{}}, nil
	}
	content, err := h.renderContent()
	if err != nil {
		return nil, err
	}
	return &authKeysFile{handler: h, reader: bytes.NewReader(content)}, nil
}

func (h *authKeysFileHandler) renderContent() ([]byte, error) {
	stored, err := h.keys.List(h.username)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, k := range stored {
		b.WriteString(strings.TrimSpace(k.PublicKey))
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// authKeysFile implements vfs.File for the authorized_keys virtual file.
type authKeysFile struct {
	handler *authKeysFileHandler
	reader  *bytes.Reader // non-nil for read mode
	buf     *bytes.Buffer // non-nil for write mode
}

func (f *authKeysFile) Read(p []byte) (int, error) {
	if f.reader != nil {
		return f.reader.Read(p)
	}
	return 0, io.EOF
}

func (f *authKeysFile) ReadAt(p []byte, off int64) (int, error) {
	if f.reader != nil {
		return f.reader.ReadAt(p, off)
	}
	return 0, io.EOF
}

func (f *authKeysFile) Write(p []byte) (int, error) {
	if f.buf != nil {
		return f.buf.Write(p)
	}
	return 0, fs.ErrPermission
}

func (f *authKeysFile) WriteAt([]byte, int64) (int, error) {
	return 0, fmt.Errorf("random writes not supported on authorized_keys")
}

func (f *authKeysFile) Seek(offset int64, whence int) (int64, error) {
	if f.reader != nil {
		return f.reader.Seek(offset, whence)
	}
	return 0, nil
}

func (f *authKeysFile) Stat() (fs.FileInfo, error) {
	return f.handler.Stat()
}

func (f *authKeysFile) Truncate(int64) error { return nil }

func (f *authKeysFile) Close() error {
	if f.buf == nil || f.buf.Len() == 0 {
		return nil
	}
	// Parse written data as authorized_keys lines and add new keys, named
	// by their comments as keys added in settings are.
	// Get existing fingerprints to skip duplicates.
	existing, err := f.handler.keys.List(f.handler.username)
	if err != nil {
		return fmt.Errorf("list existing keys: %w", err)
	}
	fps := make(map[string]bool, len(existing))
	for _, k := range existing {
		fps[k.Fingerprint] = true
	}

	data := f.buf.Bytes()
	for len(data) > 0 {
		pk, _, _, rest, err := gossh.ParseAuthorizedKey(data)
		if err != nil {
			break
		}
		fp := gossh.FingerprintSHA256(pk)
		if !fps[fp] {
			line := strings.TrimSpace(string(data[:len(data)-len(rest)]))
			if _, err := f.handler.keys.Add(f.handler.username, "", line); err != nil {
				return fmt.Errorf("add key: %w", err)
			}
			fps[fp] = true
		}
		data = rest
	}
	return nil
}

// authKeysFileInfo implements fs.FileInfo for the authorized_keys file.
type authKeysFileInfo struct {
	size int64
}

func (i *authKeysFileInfo) Name() string       { return "authorized_keys" }
func (i *authKeysFileInfo) Size() int64        { return i.size }
func (i *authKeysFileInfo) Mode() fs.FileMode  { return 0o600 }
func (i *authKeysFileInfo) ModTime() time.Time { return time.Now() }
func (i *authKeysFileInfo) IsDir() bool        { return false }
func (i *authKeysFileInfo) Sys() any           { return nil }
