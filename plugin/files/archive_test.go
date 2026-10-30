package files_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// --- Unit tests for archive logic ---

func makeTestZip(t *testing.T, entries map[string]string) (*bytes.Reader, int64) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		fh := &zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		w, err := zw.CreateHeader(fh)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	data := buf.Bytes()
	return bytes.NewReader(data), int64(len(data))
}

func makeTestTarGz(t *testing.T, entries map[string]string) (*bytes.Reader, int64) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	mtime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, content := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:    name,
			Size:    int64(len(content)),
			Mode:    0644,
			ModTime: mtime,
		}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	data := buf.Bytes()
	return bytes.NewReader(data), int64(len(data))
}

func TestListArchiveZipRoot(t *testing.T) {
	r, size := makeTestZip(t, map[string]string{
		"readme.txt":    "hello",
		"src/main.go":   "package main",
		"src/util.go":   "package main",
		"docs/guide.md": "# Guide",
	})

	entries, err := files.ListArchive(r, size, "zip", "")
	require.NoError(t, err)

	// Should see: docs/, src/, readme.txt (dirs first, then alphabetical)
	require.Equal(t, 3, len(entries))
	require.Equal(t, "docs", entries[0].Name)
	require.Equal(t, true, entries[0].IsDir)
	require.Equal(t, "src", entries[1].Name)
	require.Equal(t, true, entries[1].IsDir)
	require.Equal(t, "readme.txt", entries[2].Name)
	require.Equal(t, false, entries[2].IsDir)
}

func TestListArchiveZipSubdir(t *testing.T) {
	r, size := makeTestZip(t, map[string]string{
		"src/main.go": "package main",
		"src/util.go": "package main",
	})

	entries, err := files.ListArchive(r, size, "zip", "src")
	require.NoError(t, err)

	require.Equal(t, 2, len(entries))
	require.Equal(t, "main.go", entries[0].Name)
	require.Equal(t, "src/main.go", entries[0].Path)
	require.Equal(t, "util.go", entries[1].Name)
}

func TestListArchiveTarGzRoot(t *testing.T) {
	r, size := makeTestTarGz(t, map[string]string{
		"readme.txt":  "hello",
		"src/main.go": "package main",
	})

	entries, err := files.ListArchive(r, size, "tar.gz", "")
	require.NoError(t, err)

	require.Equal(t, 2, len(entries))
	require.Equal(t, "src", entries[0].Name)
	require.Equal(t, true, entries[0].IsDir)
	require.Equal(t, "readme.txt", entries[1].Name)
}

func TestReadArchiveFileZip(t *testing.T) {
	r, size := makeTestZip(t, map[string]string{
		"hello.txt": "world",
	})

	rc, entry, err := files.ReadArchiveFile(r, size, "zip", "hello.txt")
	require.NoError(t, err)
	defer rc.Close()

	require.Equal(t, "hello.txt", entry.Name)
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, "world", string(data))
}

func TestReadArchiveFileTarGz(t *testing.T) {
	r, size := makeTestTarGz(t, map[string]string{
		"nested/deep.txt": "content",
	})

	rc, entry, err := files.ReadArchiveFile(r, size, "tar.gz", "nested/deep.txt")
	require.NoError(t, err)
	defer rc.Close()

	require.Equal(t, "deep.txt", entry.Name)
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, "content", string(data))
}

func TestZipSlipPrevention(t *testing.T) {
	// Create a zip with a malicious path.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{
		Name:     "../../../etc/passwd",
		Method:   zip.Store,
		Modified: time.Now(),
	})
	require.NoError(t, err)
	w.Write([]byte("root:x:0:0"))
	// Also add a safe file.
	w, _ = zw.CreateHeader(&zip.FileHeader{
		Name:     "safe.txt",
		Method:   zip.Store,
		Modified: time.Now(),
	})
	w.Write([]byte("safe"))
	zw.Close()

	data := buf.Bytes()
	r := bytes.NewReader(data)

	entries, err := files.ListArchive(r, int64(len(data)), "zip", "")
	require.NoError(t, err)

	// Only safe.txt should appear.
	require.Equal(t, 1, len(entries))
	require.Equal(t, "safe.txt", entries[0].Name)
}

func TestArchiveFormat(t *testing.T) {
	tests := []struct {
		name   string
		format string
	}{
		{"file.zip", "zip"},
		{"file.tar", "tar"},
		{"file.tar.gz", "tar.gz"},
		{"file.tgz", "tar.gz"},
		{"file.tar.bz2", "tar.bz2"},
		{"file.tbz2", "tar.bz2"},
		{"file.txt", ""},
		{"file.ZIP", "zip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.format, files.ArchiveFormat(tt.name))
		})
	}
}

func TestExtractDestination(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/docs/photos.zip", "/docs/photos"},
		{"/docs/project.tar.gz", "/docs/project"},
		{"/data.tgz", "/data"},
		{"/archive.tar.bz2", "/archive"},
		{"/stuff.tbz2", "/stuff"},
		{"/plain.tar", "/plain"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			require.Equal(t, tt.want, files.ExtractDestination(tt.input))
		})
	}
}

func TestEmptyArchive(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.Close()
	data := buf.Bytes()
	r := bytes.NewReader(data)

	entries, err := files.ListArchive(r, int64(len(data)), "zip", "")
	require.NoError(t, err)
	require.Equal(t, 0, len(entries))
}

// --- Integration tests ---

func TestArchiveBrowseZip(t *testing.T) {
	fp := files.New()
	env := cogtest.New(t, fp)
	u := env.CreateUser(t, "archiver", "pass")

	// Create a zip file.
	zipData, _ := makeTestZip(t, map[string]string{
		"hello.txt":     "world",
		"sub/nested.md": "# Nested",
	})

	_, err := fp.Upload("archiver", "/test.zip", zipData)
	require.NoError(t, err)

	// Navigate to the archive — should show listing, not binary view.
	resp := env.Get(t, "/apps/files/test.zip", u.Auth)
	resp.Status(http.StatusOK)
	resp.Contains("hello.txt")
	resp.Contains("sub")
	resp.Contains("Extract all")

	// Navigate into subdirectory.
	resp = env.Get(t, "/apps/files/test.zip/sub", u.Auth)
	resp.Status(http.StatusOK)
	resp.Contains("nested.md")

	// Download file from archive.
	resp = env.Get(t, "/apps/files/test.zip/hello.txt?dl=1", u.Auth)
	resp.Status(http.StatusOK)
	resp.Contains("world")

	// Raw serve file from archive.
	resp = env.Get(t, "/apps/files/test.zip/hello.txt?raw=1", u.Auth)
	resp.Status(http.StatusOK)
	resp.Equals("world")

	// View a file inside archive.
	resp = env.Get(t, "/apps/files/test.zip/hello.txt", u.Auth)
	resp.Status(http.StatusOK)
	resp.Contains("world") // text content rendered

	// Download the archive itself.
	resp = env.Get(t, "/apps/files/test.zip?dl=1", u.Auth)
	resp.Status(http.StatusOK)
}

func TestArchiveExtract(t *testing.T) {
	fp := files.New()
	env := cogtest.New(t, fp)
	u := env.CreateUser(t, "extractor", "pass")

	zipData, _ := makeTestZip(t, map[string]string{
		"readme.txt":  "hello",
		"src/main.go": "package main",
	})
	_, err := fp.Upload("extractor", "/docs/code.zip", zipData)
	require.NoError(t, err)

	// Create a session for CSRF.
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	token := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	// Extract the archive.
	resp := env.PostForm(t, "/apps/files/docs/code.zip?extract=1",
		url.Values{"csrf_token": {token}},
		cookie, cogtest.StopRedirect)
	resp.Status(http.StatusSeeOther)

	// Verify extracted files exist.
	meta, err := fp.GetByPath("extractor", "/docs/code/readme.txt")
	require.NoError(t, err)
	require.Equal(t, "readme.txt", meta.Name)

	meta, err = fp.GetByPath("extractor", "/docs/code/src/main.go")
	require.NoError(t, err)
	require.Equal(t, "main.go", meta.Name)
}
