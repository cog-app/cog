package files

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxArchiveEntries = 10000

// archiveReader is the interface needed by archive operations.
// *os.File and *bytes.Reader both satisfy this.
type archiveReader interface {
	io.ReaderAt
	io.ReadSeeker
}

// ArchiveEntry represents a single file or directory inside an archive.
type ArchiveEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"` // full path within archive
	Size     int64  `json:"size"`
	MTime    int64  `json:"mtime"`
	MimeType string `json:"mime_type"`
	IsDir    bool   `json:"is_dir"`
}

// isArchiveMIME returns true if the MIME type is a supported archive format.
func isArchiveMIME(mimeType string) bool {
	switch mimeType {
	case "application/zip",
		"application/x-tar",
		"application/gzip", "application/x-gzip",
		"application/x-bzip2":
		return true
	}
	return false
}

// isArchiveFile returns true if the file metadata indicates a supported archive.
// Checks both MIME type and extension (needed for .tar.gz which detects as gzip).
func isArchiveFile(meta *FileMeta) bool {
	return ArchiveFormat(meta.Name) != ""
}

// ArchiveFormat returns the archive format based on filename extension.
// Returns "zip", "tar", "tar.gz", "tar.bz2", or "".
func ArchiveFormat(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"):
		return "tar.bz2"
	case strings.HasSuffix(lower, ".tar"):
		return "tar"
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	}
	return ""
}

// ListArchive lists entries within an archive at a given internal directory path.
// internalDir should be "" or a path like "subdir/nested" (no leading/trailing slash).
// Returns immediate children sorted: directories first, then by name.
func ListArchive(f archiveReader, size int64, format, internalDir string) ([]ArchiveEntry, error) {
	internalDir = cleanInternalPath(internalDir)

	switch format {
	case "zip":
		return listZip(f, size, internalDir)
	case "tar", "tar.gz", "tar.bz2":
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("seek: %w", err)
		}
		return listTar(f, format, internalDir)
	default:
		return nil, fmt.Errorf("unsupported archive format: %s", format)
	}
}

// ReadArchiveFile extracts a single file from an archive and returns its content.
// The caller must close the returned ReadCloser.
func ReadArchiveFile(f archiveReader, size int64, format, entryPath string) (io.ReadCloser, *ArchiveEntry, error) {
	entryPath = cleanInternalPath(entryPath)
	if entryPath == "" {
		return nil, nil, fmt.Errorf("empty entry path")
	}

	switch format {
	case "zip":
		return readZipFile(f, size, entryPath)
	case "tar", "tar.gz", "tar.bz2":
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, nil, fmt.Errorf("seek: %w", err)
		}
		return readTarFile(f, format, entryPath)
	default:
		return nil, nil, fmt.Errorf("unsupported archive format: %s", format)
	}
}

// extractDestination returns the directory path where an archive should be extracted.
// e.g. "/docs/photos.tar.gz" -> "/docs/photos"
func ExtractDestination(archivePath string) string {
	dir := path.Dir(archivePath)
	base := path.Base(archivePath)
	// Strip all archive extensions.
	name := base
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"):
		name = name[:len(name)-7]
	case strings.HasSuffix(lower, ".tar.bz2"):
		name = name[:len(name)-8]
	case strings.HasSuffix(lower, ".tgz"):
		name = name[:len(name)-4]
	case strings.HasSuffix(lower, ".tbz2"):
		name = name[:len(name)-5]
	default:
		ext := filepath.Ext(name)
		if ext != "" {
			name = name[:len(name)-len(ext)]
		}
	}
	if name == "" {
		name = "extracted"
	}
	return path.Join(dir, name)
}

// isSafePath returns true if an archive entry path is safe (no traversal).
func isSafePath(p string) bool {
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return false
	}
	cleaned := filepath.Clean(p)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// --- ZIP implementation ---

func listZip(f io.ReaderAt, size int64, internalDir string) ([]ArchiveEntry, error) {
	zr, err := zip.NewReader(f.(io.ReaderAt), size)
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}

	seen := make(map[string]bool)
	var entries []ArchiveEntry
	prefix := ""
	if internalDir != "" {
		prefix = internalDir + "/"
	}

	for _, zf := range zr.File {
		entryPath := cleanInternalPath(zf.Name)
		if entryPath == "" || !isSafePath(entryPath) {
			continue
		}
		// Skip symlinks.
		if zf.FileInfo().Mode()&os.ModeSymlink != 0 || zf.FileInfo().Mode()&os.ModeIrregular != 0 {
			continue
		}

		// Check if entry is a direct child of internalDir.
		var childName string
		var childIsDir bool
		if internalDir == "" {
			// Root level: take first segment.
			parts := strings.SplitN(entryPath, "/", 2)
			childName = parts[0]
			childIsDir = len(parts) > 1 || strings.HasSuffix(zf.Name, "/")
		} else {
			if !strings.HasPrefix(entryPath, prefix) {
				continue
			}
			rest := entryPath[len(prefix):]
			if rest == "" {
				continue // the directory itself
			}
			parts := strings.SplitN(rest, "/", 2)
			childName = parts[0]
			childIsDir = len(parts) > 1 || strings.HasSuffix(zf.Name, "/")
		}

		if childName == "" {
			continue
		}

		childPath := childName
		if internalDir != "" {
			childPath = internalDir + "/" + childName
		}

		if seen[childPath] {
			continue
		}
		seen[childPath] = true

		entry := ArchiveEntry{
			Name:  childName,
			Path:  childPath,
			MTime: zf.Modified.Unix(),
			IsDir: childIsDir,
		}
		if !childIsDir {
			entry.Size = int64(zf.UncompressedSize64)
			entry.MimeType = detectMimeType(childName)
		}
		entries = append(entries, entry)

		if len(entries) >= maxArchiveEntries {
			break
		}
	}

	sortArchiveEntries(entries)
	return entries, nil
}

func readZipFile(f io.ReaderAt, size int64, entryPath string) (io.ReadCloser, *ArchiveEntry, error) {
	zr, err := zip.NewReader(f.(io.ReaderAt), size)
	if err != nil {
		return nil, nil, fmt.Errorf("open zip: %w", err)
	}

	for _, zf := range zr.File {
		cleaned := cleanInternalPath(zf.Name)
		if cleaned == entryPath && !strings.HasSuffix(zf.Name, "/") {
			rc, err := zf.Open()
			if err != nil {
				return nil, nil, fmt.Errorf("open entry: %w", err)
			}
			entry := &ArchiveEntry{
				Name:     path.Base(entryPath),
				Path:     entryPath,
				Size:     int64(zf.UncompressedSize64),
				MTime:    zf.Modified.Unix(),
				MimeType: detectMimeType(path.Base(entryPath)),
			}
			return rc, entry, nil
		}
	}
	return nil, nil, fmt.Errorf("entry not found: %s", entryPath)
}

// --- TAR implementation ---

func listTar(r io.Reader, format, internalDir string) ([]ArchiveEntry, error) {
	tr, err := wrapTarReader(r, format)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var entries []ArchiveEntry
	prefix := ""
	if internalDir != "" {
		prefix = internalDir + "/"
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}

		// Skip symlinks and other non-regular/non-directory entries.
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			continue
		}

		entryPath := cleanInternalPath(hdr.Name)
		if entryPath == "" || !isSafePath(entryPath) {
			continue
		}

		var childName string
		var childIsDir bool
		if internalDir == "" {
			parts := strings.SplitN(entryPath, "/", 2)
			childName = parts[0]
			childIsDir = len(parts) > 1 || hdr.Typeflag == tar.TypeDir
		} else {
			if !strings.HasPrefix(entryPath, prefix) {
				continue
			}
			rest := entryPath[len(prefix):]
			if rest == "" {
				continue
			}
			parts := strings.SplitN(rest, "/", 2)
			childName = parts[0]
			childIsDir = len(parts) > 1 || hdr.Typeflag == tar.TypeDir
		}

		if childName == "" {
			continue
		}

		childPath := childName
		if internalDir != "" {
			childPath = internalDir + "/" + childName
		}

		if seen[childPath] {
			continue
		}
		seen[childPath] = true

		entry := ArchiveEntry{
			Name:  childName,
			Path:  childPath,
			MTime: hdr.ModTime.Unix(),
			IsDir: childIsDir,
		}
		if !childIsDir {
			entry.Size = hdr.Size
			entry.MimeType = detectMimeType(childName)
		}
		entries = append(entries, entry)

		if len(entries) >= maxArchiveEntries {
			break
		}
	}

	sortArchiveEntries(entries)
	return entries, nil
}

func readTarFile(r io.Reader, format, entryPath string) (io.ReadCloser, *ArchiveEntry, error) {
	tr, err := wrapTarReader(r, format)
	if err != nil {
		return nil, nil, err
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read tar: %w", err)
		}

		cleaned := cleanInternalPath(hdr.Name)
		if cleaned == entryPath && hdr.Typeflag != tar.TypeDir {
			entry := &ArchiveEntry{
				Name:     path.Base(entryPath),
				Path:     entryPath,
				Size:     hdr.Size,
				MTime:    hdr.ModTime.Unix(),
				MimeType: detectMimeType(path.Base(entryPath)),
			}
			// Wrap the tar reader section in an io.NopCloser since the
			// underlying reader will be closed by the caller.
			return io.NopCloser(tr), entry, nil
		}
	}
	return nil, nil, fmt.Errorf("entry not found: %s", entryPath)
}

func wrapTarReader(r io.Reader, format string) (*tar.Reader, error) {
	switch format {
	case "tar":
		return tar.NewReader(r), nil
	case "tar.gz":
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("open gzip: %w", err)
		}
		return tar.NewReader(gz), nil
	case "tar.bz2":
		return tar.NewReader(bzip2.NewReader(r)), nil
	default:
		return nil, fmt.Errorf("unsupported tar format: %s", format)
	}
}

// --- Helpers ---

// cleanInternalPath normalizes an archive entry path: removes leading/trailing
// slashes, cleans "." and ".." segments.
func cleanInternalPath(p string) string {
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	p = path.Clean(p)
	if p == "." {
		return ""
	}
	return p
}

func sortArchiveEntries(entries []ArchiveEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		a, b := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if a != b {
			return a < b
		}
		return entries[i].Name < entries[j].Name
	})
}

// isArchiveEntryDir checks if an internal path represents a directory in the archive.
// Used when navigating into archive subdirectories.
func isArchiveEntryDir(f archiveReader, size int64, format, entryPath string) bool {
	entryPath = cleanInternalPath(entryPath)
	if entryPath == "" {
		return true // root
	}

	// Check if any entry has this path as a prefix → it's a directory.
	entries, err := ListArchive(f, size, format, entryPath)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// dosEpoch is 1980-01-01, the earliest time a zip entry can record. An
// archive written without timestamps stores zero, which decodes to just
// before it (1979-11-30); such a time means "unknown", not a date.
const dosEpoch = 315532800

// formatArchiveTime formats an entry's time for display, or "" when the
// archive did not record one.
func formatArchiveTime(unix int64, now time.Time) string {
	if unix < dosEpoch {
		return ""
	}
	return shortTime(unix, now)
}
