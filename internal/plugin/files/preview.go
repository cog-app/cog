package files

import (
	"fmt"
	"image"
	"image/jpeg"
	_ "image/gif"
	_ "image/png"
	"os"
	"path/filepath"

	_ "golang.org/x/image/webp"
	"golang.org/x/image/draw"
)

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
