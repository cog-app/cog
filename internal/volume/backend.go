package volume

import (
	"fmt"
	"io"
	"time"
)

// FileInfo describes a file or directory in a volume.
type FileInfo struct {
	Name     string
	Path     string
	Size     int64
	ModTime  time.Time
	IsDir    bool
	MimeType string
}

// Backend provides filesystem-like operations on a volume.
type Backend interface {
	List(path string) ([]FileInfo, error)
	Stat(path string) (*FileInfo, error)
	Get(path string) (io.ReadCloser, error)
	Put(path string, content io.Reader) error
	Mkdir(path string) error
	Delete(path string) error
	Rename(oldPath, newPath string) error
}

// NewBackend creates a Backend for the given volume.
func NewBackend(vol *Volume) (Backend, error) {
	parsed, err := ParseURI(vol.URI)
	if err != nil {
		return nil, err
	}
	switch parsed.Scheme {
	case "file":
		return newFileBackend(parsed.Path), nil
	case "webdav":
		cfg, err := ParseWebDAVConfig(vol.Config)
		if err != nil {
			return nil, err
		}
		return newWebDAVBackend(cfg), nil
	case "mem":
		return nil, fmt.Errorf("mem:// volumes do not support browsing")
	default:
		return nil, fmt.Errorf("unsupported backend scheme: %q", parsed.Scheme)
	}
}
