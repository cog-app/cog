package volume

import (
	"fmt"

	"git.duckfam.us/jj-software/cog/vfs"
)

// Backend is an alias for vfs.FS. Existing code that references
// volume.Backend continues to compile.
type Backend = vfs.FS

// FileInfo is an alias for vfs.FileInfo. Existing code that references
// volume.FileInfo continues to compile.
type FileInfo = vfs.FileInfo

// NewBackend creates a vfs.FS for the given volume.
func NewBackend(vol *Volume) (vfs.FS, error) {
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
