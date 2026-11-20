package volume

import (
	"path"
	"path/filepath"
	"strings"
)

// ServerFiles is where the server keeps its own files: the data directory,
// which the default volume is, and the system database. Anything that shows
// a volume to a user — the volume browser, a mount, SSH's /volumes — keeps
// them out of reach, since deleting one under a running server destroys
// data.
type ServerFiles struct {
	DataDir string
	DBPath  string
}

// isDataDir reports whether vol is the data directory itself.
func (sf ServerFiles) isDataDir(vol *Volume) bool {
	if vol.Scheme() != "file" || sf.DataDir == "" {
		return false
	}
	root, err := filepath.Abs(vol.URIPath())
	if err != nil {
		return false
	}
	dataDir, err := filepath.Abs(sf.DataDir)
	return err == nil && root == dataDir
}

// MountExposesInternal reports whether a mount of subpath on vol would put
// one of the server's own files in a user's tree. It refuses the internal
// files themselves and the directories that hold them: the data directory
// itself, with the system database, and a user's directory, with their
// plugin databases. isUser says whether a name is an account's.
func (sf ServerFiles) MountExposesInternal(vol *Volume, subpath string, isUser func(name string) bool) bool {
	if !sf.isDataDir(vol) {
		return false
	}
	clean := strings.TrimPrefix(path.Clean("/"+subpath), "/")
	if clean == "" || sf.Internal(vol, clean) {
		return true
	}
	return !strings.Contains(clean, "/") && isUser(clean)
}

// Internal reports whether p, on vol, is one of the server's own files: the
// system database and its journal files, a search index, or a plugin's
// database.
func (sf ServerFiles) Internal(vol *Volume, p string) bool {
	if !sf.isDataDir(vol) {
		return false
	}
	dataDir, err := filepath.Abs(sf.DataDir)
	if err != nil {
		return false
	}

	clean := strings.TrimPrefix(path.Clean("/"+p), "/")
	if clean == "" {
		return false
	}
	segs := strings.Split(clean, "/")
	for _, s := range segs {
		if s == ".search" || s == "app_data" {
			return true
		}
	}
	if len(segs) == 1 && strings.HasPrefix(segs[0], ".cog-secret") {
		return true // the master secret (services.loadSecret)
	}
	if strings.HasPrefix(segs[0], "cog-chunk") {
		return true // uploads in progress (compat.ChunkedUploadHandler)
	}
	if len(segs) == 1 {
		// The system database, when it lives in the data directory: where
		// COG_DB puts it, or system.db when nothing says otherwise.
		db := "system.db"
		if sf.DBPath != "" {
			dbPath, err := filepath.Abs(sf.DBPath)
			if err != nil || filepath.Dir(dbPath) != dataDir {
				return false
			}
			db = filepath.Base(dbPath)
		}
		// system.db, system.db-wal, system.db-shm, system.db-journal
		if segs[0] == db || strings.HasPrefix(segs[0], db+"-") {
			return true
		}
	}
	return false
}
