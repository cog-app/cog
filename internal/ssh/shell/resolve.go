package shell

import (
	"fmt"
	"path/filepath"
	"strings"
)

// resolve maps a virtual or relative path to a real filesystem path within
// the user's files directory. It returns an error if the resolved path
// escapes the sandbox.
func (s *Shell) resolve(base, path string) (string, error) {
	if filepath.IsAbs(path) {
		// Strip /files prefix for virtual-to-real mapping.
		clean := filepath.Clean(path)
		if strings.HasPrefix(clean, "/files") {
			clean = strings.TrimPrefix(clean, "/files")
			if clean == "" {
				clean = "/"
			}
		}
		path = filepath.Join(s.filesDir, clean)
	} else {
		path = filepath.Join(base, path)
	}
	path = filepath.Clean(path)

	// Evaluate symlinks so we catch escapes via symlink targets.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// If the file doesn't exist yet (e.g. creating a new file),
		// evaluate the parent and append the base name.
		parent := filepath.Dir(path)
		resolved, err = filepath.EvalSymlinks(parent)
		if err != nil {
			return "", fmt.Errorf("path not found: %s", s.virtualPath(path))
		}
		resolved = filepath.Join(resolved, filepath.Base(path))
	}

	if !strings.HasPrefix(resolved, s.filesDir) {
		return "", fmt.Errorf("access denied: path outside home directory")
	}
	return resolved, nil
}

// virtualPath converts a real filesystem path back to a virtual path for display.
func (s *Shell) virtualPath(realPath string) string {
	rel, err := filepath.Rel(s.filesDir, realPath)
	if err != nil {
		return realPath
	}
	vp := "/files/" + filepath.ToSlash(rel)
	if vp == "/files/." {
		return "/files"
	}
	return filepath.Clean(vp)
}

// virtualToReal maps a virtual path to a real filesystem path.
// Returns "" for non-navigable virtual dirs (/.ssh, etc.).
// "/" maps to the real system root — the interpreter's stat/readdir handlers
// route through the VFS so only virtual entries (.ssh, files) are visible.
func (s *Shell) virtualToReal(vp string) string {
	vp = filepath.Clean(vp)
	if vp == "/" {
		return "/"
	}
	if vp == "/files" {
		return s.filesDir
	}
	if strings.HasPrefix(vp, "/files/") {
		return filepath.Join(s.filesDir, strings.TrimPrefix(vp, "/files/"))
	}
	return "" // not navigable (e.g. /.ssh)
}
