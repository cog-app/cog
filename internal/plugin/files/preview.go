package files

import (
	"context"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

var (
	libreOfficePath string
	loMu            sync.Mutex
)

func init() {
	libreOfficePath, _ = exec.LookPath("libreoffice")
}

// LibreOfficeAvailable reports whether LibreOffice is installed.
func LibreOfficeAvailable() bool {
	return libreOfficePath != ""
}

// IsOfficeDocument reports whether the MIME type is a convertible office format.
func IsOfficeDocument(mimeType string) bool {
	switch mimeType {
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document", // .docx
		"application/msword",          // .doc
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", // .xlsx
		"application/vnd.ms-excel",    // .xls
		"application/vnd.openxmlformats-officedocument.presentationml.presentation", // .pptx
		"application/vnd.ms-powerpoint", // .ppt
		"application/vnd.oasis.opendocument.text",         // .odt
		"application/vnd.oasis.opendocument.spreadsheet",  // .ods
		"application/vnd.oasis.opendocument.presentation", // .odp
		"application/rtf": // .rtf
		return true
	}
	return false
}

// HasPreview returns true if the MIME type supports thumbnail generation.
func HasPreview(mimeType string) bool {
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

// Preview returns a JPEG thumbnail for an image file.
// It caches thumbnails on disk keyed by ETag + size.
func (p *Plugin) Preview(username, filePath string, size int) (*os.File, error) {
	if size < 16 {
		size = 16
	} else if size > 1024 {
		size = 1024
	}

	meta, err := p.GetByPath(username, filePath)
	if err != nil {
		return nil, err
	}
	if !HasPreview(meta.MimeType) {
		return nil, fmt.Errorf("no preview for %s", meta.MimeType)
	}

	// Cache path: {dataDir}/{username}/thumbnails/{size}/{etag}.jpg
	filesDir := p.registry.UserFilesDir(username)
	cacheDir := filepath.Join(filesDir, "..", "thumbnails", fmt.Sprintf("%d", size))
	cachePath := filepath.Join(cacheDir, meta.ETag+".jpg")

	// Cache hit?
	if f, err := os.Open(cachePath); err == nil {
		return f, nil
	}

	// Cache miss — generate thumbnail.
	srcPath := filepath.Join(filesDir, filePath)
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return nil, fmt.Errorf("open source: %w", err)
	}
	defer srcFile.Close()

	src, _, err := image.Decode(srcFile)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	// Compute dimensions that fit within size x size, preserving aspect ratio.
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	dw, dh := size, size
	if sw > sh {
		dh = sh * size / sw
	} else {
		dw = sw * size / sh
	}
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)

	// Write to cache.
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir cache: %w", err)
	}
	cacheFile, err := os.Create(cachePath)
	if err != nil {
		return nil, fmt.Errorf("create cache: %w", err)
	}
	if err := jpeg.Encode(cacheFile, dst, &jpeg.Options{Quality: 80}); err != nil {
		cacheFile.Close()
		os.Remove(cachePath)
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	cacheFile.Close()

	// Return the cached file.
	return os.Open(cachePath)
}

// PreviewPDF converts an office document to PDF via LibreOffice and caches the result.
func (p *Plugin) PreviewPDF(username, filePath string) (*os.File, error) {
	if !LibreOfficeAvailable() {
		return nil, fmt.Errorf("libreoffice not available")
	}

	meta, err := p.GetByPath(username, filePath)
	if err != nil {
		return nil, err
	}
	if !IsOfficeDocument(meta.MimeType) {
		return nil, fmt.Errorf("not an office document: %s", meta.MimeType)
	}

	// Cache path: {filesDir}/../previews/{etag}.pdf
	filesDir := p.registry.UserFilesDir(username)
	cacheDir := filepath.Join(filesDir, "..", "previews")
	cachePath := filepath.Join(cacheDir, meta.ETag+".pdf")

	// Cache hit?
	if f, err := os.Open(cachePath); err == nil {
		return f, nil
	}

	// Cache miss — convert via LibreOffice.
	tmpDir, err := os.MkdirTemp("", "cog-lo-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Copy source file with original extension (LO needs it).
	srcPath := filepath.Join(filesDir, filePath)
	srcData, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	ext := filepath.Ext(meta.Name)
	tmpFile := filepath.Join(tmpDir, "input"+ext)
	if err := os.WriteFile(tmpFile, srcData, 0600); err != nil {
		return nil, fmt.Errorf("write temp: %w", err)
	}

	// Serialize LibreOffice invocations to avoid lock file conflicts.
	loMu.Lock()
	defer loMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, libreOfficePath,
		"--headless", "--convert-to", "pdf", "--outdir", tmpDir, tmpFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("libreoffice convert: %w: %s", err, out)
	}

	// Find the output PDF.
	pdfPath := filepath.Join(tmpDir, "input.pdf")
	if _, err := os.Stat(pdfPath); err != nil {
		return nil, fmt.Errorf("conversion produced no PDF")
	}

	// Move to cache.
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir cache: %w", err)
	}
	if err := os.Rename(pdfPath, cachePath); err != nil {
		// Rename may fail across filesystems; fall back to copy.
		data, readErr := os.ReadFile(pdfPath)
		if readErr != nil {
			return nil, fmt.Errorf("read converted pdf: %w", readErr)
		}
		if writeErr := os.WriteFile(cachePath, data, 0644); writeErr != nil {
			return nil, fmt.Errorf("write cache: %w", writeErr)
		}
	}

	return os.Open(cachePath)
}
