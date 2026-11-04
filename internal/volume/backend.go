package volume

import (
	"fmt"
	"net/http"
	"time"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
	"git.duckfam.us/jj-software/cog/vfs"
)

// Backend is an alias for vfs.FS. Existing code that references
// volume.Backend continues to compile.
type Backend = vfs.FS

// FileInfo is an alias for vfs.FileInfo. Existing code that references
// volume.FileInfo continues to compile.
type FileInfo = vfs.FileInfo

// webdavTimeout bounds each request to a WebDAV volume.
const webdavTimeout = 30 * time.Second

// NewBackend creates a vfs.FS for the given volume.
func NewBackend(vol *Volume) (vfs.FS, error) {
	parsed, err := ParseURI(vol.URI)
	if err != nil {
		return nil, err
	}
	switch parsed.Scheme {
	case "file":
		// A local path is the server's own filesystem. Only an admin may
		// name one, and an admin's volumes are the system's.
		if vol.OwnerUserID != nil {
			return nil, fmt.Errorf("volume %q: a user's volume cannot be a local path", vol.Name)
		}
		return newFileBackend(parsed.Path), nil
	case "webdav":
		cfg, err := ParseWebDAVConfig(vol.Config)
		if err != nil {
			return nil, err
		}
		// A user's WebDAV server is a URL the user typed: it may only
		// reach public addresses, not the server's own network.
		client := &http.Client{Timeout: webdavTimeout}
		if vol.OwnerUserID != nil {
			client = safehttp.Client(webdavTimeout)
		}
		return newWebDAVBackend(cfg, client), nil
	case "mem":
		return nil, fmt.Errorf("mem:// volumes do not support browsing")
	default:
		return nil, fmt.Errorf("unsupported backend scheme: %q", parsed.Scheme)
	}
}
