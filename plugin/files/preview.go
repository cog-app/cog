package files

import (
	"context"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/sandbox"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

var (
	libreOfficePath string
	inkscapePath    string
	magickPath      string
	magickIsGM      bool // true if magickPath is GraphicsMagick (gm)
	pythonPath      string
	loMu            sync.Mutex
)

func init() {
	libreOfficePath, _ = exec.LookPath("libreoffice")
	inkscapePath, _ = exec.LookPath("inkscape")
	pythonPath, _ = exec.LookPath("python3")
	// Prefer ImageMagick, fall back to GraphicsMagick.
	if p, err := exec.LookPath("magick"); err == nil {
		magickPath = p
	} else if p, err := exec.LookPath("convert"); err == nil {
		magickPath = p
	} else if p, err := exec.LookPath("gm"); err == nil {
		magickPath = p
		magickIsGM = true
	}
	// Check that Python has Pillow available for PSD support.
	if pythonPath != "" {
		// unsandboxed: reads no input, only checks Pillow is installed.
		if err := exec.Command(pythonPath, "-c", "from PIL import Image").Run(); err != nil {
			pythonPath = ""
		}
	}
}

// InkscapeAvailable reports whether Inkscape is installed.
func InkscapeAvailable() bool {
	return inkscapePath != ""
}

// MagickAvailable reports whether ImageMagick or GraphicsMagick is installed.
func MagickAvailable() bool {
	return magickPath != ""
}

// PillowAvailable reports whether Python 3 with Pillow is installed.
func PillowAvailable() bool {
	return pythonPath != ""
}

// LibreOfficeAvailable reports whether LibreOffice is installed.
func LibreOfficeAvailable() bool {
	return libreOfficePath != ""
}

// IsOfficeDocument reports whether the MIME type is a convertible office format.
func IsOfficeDocument(mimeType string) bool {
	switch mimeType {
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document", // .docx
		"application/msword", // .doc
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", // .xlsx
		"application/vnd.ms-excel", // .xls
		"application/vnd.openxmlformats-officedocument.presentationml.presentation", // .pptx
		"application/vnd.ms-powerpoint",                                             // .ppt
		"application/vnd.oasis.opendocument.text",                                   // .odt
		"application/vnd.oasis.opendocument.spreadsheet",                            // .ods
		"application/vnd.oasis.opendocument.presentation",                           // .odp
		"application/rtf": // .rtf
		return true
	}
	return false
}

// isNativeImage returns true if Go can decode the image directly.
func isNativeImage(mimeType string) bool {
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

// isInkscapeFormat returns true if Inkscape can convert the format to PNG.
func isInkscapeFormat(mimeType string) bool {
	switch mimeType {
	case "image/svg+xml", // .svg
		"application/postscript",  // .ai, .eps
		"image/x-eps",             // .eps (alternate)
		"application/eps",         // .eps (alternate)
		"application/illustrator": // .ai (alternate)
		return true
	}
	return false
}

// isXCF returns true for GIMP's native format.
func isXCF(mimeType string) bool {
	return mimeType == "image/x-xcf"
}

// isPSD returns true for Photoshop's native format.
func isPSD(mimeType string) bool {
	switch mimeType {
	case "image/vnd.adobe.photoshop", // .psd
		"image/x-photoshop", // .psd (alternate)
		"image/x-psd":       // .psd (alternate)
		return true
	}
	return false
}

// canConvertToPNG reports whether we have a tool to convert this MIME type.
func canConvertToPNG(mimeType string) bool {
	if isInkscapeFormat(mimeType) && InkscapeAvailable() {
		return true
	}
	// XCF: any *Magick variant handles it.
	if isXCF(mimeType) && MagickAvailable() {
		return true
	}
	// PSD: Pillow (via Python) or full ImageMagick (not GraphicsMagick).
	if isPSD(mimeType) && (PillowAvailable() || (MagickAvailable() && !magickIsGM)) {
		return true
	}
	return false
}

// HasPreview returns true if the MIME type supports thumbnail generation.
func HasPreview(mimeType string) bool {
	if isNativeImage(mimeType) {
		return true
	}
	return canConvertToPNG(mimeType)
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
	filesDir := p.ctx.UserFilesDir(username)
	cacheDir := filepath.Join(filesDir, "..", "thumbnails", fmt.Sprintf("%d", size))
	cachePath := filepath.Join(cacheDir, meta.ETag+".jpg")

	// Cache hit?
	if f, err := os.Open(cachePath); err == nil {
		return f, nil
	}

	// Cache miss — generate thumbnail.
	srcPath := filepath.Join(filesDir, filePath)

	// For non-native formats, convert to PNG first.
	if !isNativeImage(meta.MimeType) {
		pngPath, err := p.convertToPNG(srcPath, meta.MimeType, meta.ETag)
		if err != nil {
			return nil, fmt.Errorf("convert to png: %w", err)
		}
		srcPath = pngPath
	}

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
	// Written beside the cache entry and renamed into place, so a request
	// for the same thumbnail meanwhile never opens a half-written one.
	cacheFile, err := os.CreateTemp(cacheDir, meta.ETag+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create cache: %w", err)
	}
	err = jpeg.Encode(cacheFile, dst, &jpeg.Options{Quality: 80})
	if cerr := cacheFile.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(cacheFile.Name(), cachePath)
	}
	if err != nil {
		if rmErr := os.Remove(cacheFile.Name()); rmErr != nil {
			p.log.Error("remove partial thumbnail", "path", cacheFile.Name(), "err", rmErr)
		}
		return nil, fmt.Errorf("write thumbnail: %w", err)
	}

	// Return the cached file.
	return os.Open(cachePath)
}

// convertToPNG converts a non-native image format to PNG, caching the result.
// Returns the path to the cached PNG file.
func (p *Plugin) convertToPNG(srcPath, mimeType, etag string) (string, error) {
	// Cache converted PNGs alongside thumbnails.
	cacheDir := filepath.Join(filepath.Dir(srcPath), "..", "converted")
	cachePath := filepath.Join(cacheDir, etag+".png")

	// Cache hit?
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, nil
	}

	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir cache: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The converters parse what a user uploaded, so each runs sandboxed,
	// reading only the upload and writing only the cache.
	spec := sandbox.Spec{Read: []string{srcPath}, Write: []string{cacheDir}}
	var cmd *exec.Cmd
	switch {
	case isInkscapeFormat(mimeType):
		// Inkscape draws the files an SVG links to into the PNG the uploader
		// gets back, so links outside the document are refused even where
		// the sandbox would hide the files.
		if mimeType == "image/svg+xml" {
			if err := checkSVG(srcPath); err != nil {
				return "", err
			}
		}
		cmd = sandbox.CommandContext(ctx, spec, inkscapePath,
			srcPath, "--export-type=png", "--export-filename="+cachePath)
	case (isXCF(mimeType) || isPSD(mimeType)) && MagickAvailable() && !magickIsGM:
		// ImageMagick handles both XCF and PSD. The coder prefixes stop it
		// sniffing the content and picking a coder that reads other files,
		// such as MVG's text:@/path.
		cmd = sandbox.CommandContext(ctx, spec, magickPath,
			magickCoder(mimeType)+":"+srcPath+"[0]", "-flatten", "png:"+cachePath)
	case isXCF(mimeType) && magickIsGM:
		// GraphicsMagick handles XCF but not PSD.
		cmd = sandbox.CommandContext(ctx, spec, magickPath,
			"convert", "xcf:"+srcPath+"[0]", "-flatten", "png:"+cachePath)
	case isPSD(mimeType) && PillowAvailable():
		// Pillow handles PSD reliably. The paths are arguments, not source.
		cmd = sandbox.CommandContext(ctx, spec, pythonPath, "-c",
			"import sys; from PIL import Image; Image.open(sys.argv[1], formats=['PSD']).convert('RGBA').save(sys.argv[2], 'PNG')",
			srcPath, cachePath)
	default:
		return "", fmt.Errorf("unsupported format: %s", mimeType)
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("convert %s: %w: %s", mimeType, err, out)
	}

	if _, err := os.Stat(cachePath); err != nil {
		return "", fmt.Errorf("conversion produced no output")
	}

	return cachePath, nil
}

// magickCoder names the ImageMagick coder for a layered format convertToPNG
// hands it.
func magickCoder(mimeType string) string {
	if isPSD(mimeType) {
		return "psd"
	}
	return "xcf"
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
	filesDir := p.ctx.UserFilesDir(username)
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
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			p.log.Error("remove libreoffice temp dir", "path", tmpDir, "err", err)
		}
	}()

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

	// Each conversion gets a fresh profile, so one document cannot change
	// how the next is opened, set to run no macros and fetch nothing a
	// document links to.
	profile := filepath.Join(tmpDir, "profile")
	if err := os.MkdirAll(filepath.Join(profile, "user"), 0o700); err != nil {
		return nil, fmt.Errorf("create libreoffice profile: %w", err)
	}
	if err := os.WriteFile(filepath.Join(profile, "user", "registrymodifications.xcu"), []byte(libreOfficeSettings), 0o600); err != nil {
		return nil, fmt.Errorf("create libreoffice profile: %w", err)
	}

	// One conversion at a time: each is a whole LibreOffice process.
	loMu.Lock()
	defer loMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := sandbox.CommandContext(ctx, sandbox.Spec{Write: []string{tmpDir}}, libreOfficePath,
		"-env:UserInstallation="+(&url.URL{Scheme: "file", Path: profile}).String(),
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

// libreOfficeSettings lock down the profile a conversion runs with: no
// macros, links in the document never updated (Writer and Calc number
// "never" differently), formulas never recalculated on load — which is
// what would run WEBSERVICE or fetch an external reference — and links
// from documents outside trusted locations blocked.
const libreOfficeSettings = `<?xml version="1.0" encoding="UTF-8"?>
<oor:items xmlns:oor="http://openoffice.org/2001/registry" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
<item oor:path="/org.openoffice.Office.Common/Security/Scripting"><prop oor:name="MacroSecurityLevel" oor:op="fuse"><value>3</value></prop></item>
<item oor:path="/org.openoffice.Office.Common/Security/Scripting"><prop oor:name="DisableMacrosExecution" oor:op="fuse"><value>true</value></prop></item>
<item oor:path="/org.openoffice.Office.Common/Security/Scripting"><prop oor:name="BlockUntrustedRefererLinks" oor:op="fuse"><value>true</value></prop></item>
<item oor:path="/org.openoffice.Office.Writer/Content/Update"><prop oor:name="Link" oor:op="fuse"><value>2</value></prop></item>
<item oor:path="/org.openoffice.Office.Calc/Content/Update"><prop oor:name="Link" oor:op="fuse"><value>1</value></prop></item>
<item oor:path="/org.openoffice.Office.Calc/Formula/Load"><prop oor:name="OOXMLRecalcMode" oor:op="fuse"><value>1</value></prop></item>
<item oor:path="/org.openoffice.Office.Calc/Formula/Load"><prop oor:name="ODFRecalcMode" oor:op="fuse"><value>1</value></prop></item>
</oor:items>
`
