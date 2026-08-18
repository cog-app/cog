package gallery

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
)

func TestMatchScanFolders(t *testing.T) {
	// The files plugin stores every path with a leading slash, so these are the
	// only shapes matchScanFolders is ever called with in production.
	tests := []struct {
		name     string
		path     string
		patterns []string
		want     bool
	}{
		{"default pattern, files-plugin path", "/Photos/a.jpg", []string{"Photos/"}, true},
		{"default pattern, nested", "/Photos/2019/a.jpg", []string{"Photos/"}, true},
		{"default pattern, other folder", "/Notes/a.jpg", []string{"Photos/"}, false},
		{"default pattern, root file", "/a.jpg", []string{"Photos/"}, false},

		{"pattern written with leading slash", "/Photos/a.jpg", []string{"/Photos/"}, true},
		{"pattern without slash on unslashed path", "Photos/a.jpg", []string{"Photos/"}, true},

		{"root pattern matches everything", "/a.jpg", []string{"/"}, true},
		{"root pattern matches nested", "/Photos/a.jpg", []string{"/"}, true},

		{"glob on files-plugin path", "/Photos/a.jpg", []string{"Photos/*.jpg"}, true},
		{"glob wrong extension", "/Photos/a.png", []string{"Photos/*.jpg"}, false},
		{"glob truncated to pattern depth", "/Photos/2019/a.jpg", []string{"Photos/*"}, true},

		{"case sensitive", "/photos/a.jpg", []string{"Photos/"}, false},
		{"second pattern matches", "/Camera/a.jpg", []string{"Photos/", "Camera/"}, true},
		{"no patterns", "/Photos/a.jpg", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, matchScanFolders(tt.path, tt.patterns))
		})
	}
}
