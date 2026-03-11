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
	// Virtual absolute paths start with /; make them relative to filesDir.
	if filepath.IsAbs(path) {
		path = filepath.Join(s.filesDir, path)
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
	vp := "/" + filepath.ToSlash(rel)
	// Clean up "/."-style paths.
	if vp == "/." {
		return "/"
	}
	return filepath.Clean(vp)
}
