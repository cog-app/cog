package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	iofs "io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/imaging"
	"git.duckfam.us/jj-software/cog/internal/imgconv"
	"git.duckfam.us/jj-software/cog/internal/safeimage"
	"git.duckfam.us/jj-software/cog/internal/sandbox"
	"git.duckfam.us/jj-software/cog/plugin"

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
	if imgconv.Handles(mimeType) {
		return true
	}
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

// thumbDir holds cached thumbnails. Thumbnails made before EXIF orientation
// was applied lived under oldThumbDir; that directory is removed the first
// time a user's thumbnail is asked for, since every one in it is suspect.
const (
	thumbDir    = "thumbs"
	oldThumbDir = "thumbnails"
)

// Preview returns a JPEG thumbnail for an image file, on any mount. It
// caches thumbnails in the user's cache keyed by ETag + size.
func (p *Plugin) Preview(username, filePath string, size int) (plugin.StoredFile, error) {
	cache, name, err := p.ensurePreview(username, filePath, size, nil)
	if err != nil {
		return nil, err
	}
	return cache.Open(name)
}

// EnsurePreview caches a thumbnail of an image file if it is not cached
// already, and reports whether it had to make one.
func (p *Plugin) EnsurePreview(username, filePath string, size int) (generated bool, err error) {
	_, _, err = p.ensurePreview(username, filePath, size, &generated)
	return generated, err
}

func (p *Plugin) ensurePreview(username, filePath string, size int, generated *bool) (plugin.Storage, string, error) {
	size = plugin.ThumbnailSize(size)
	// A shared folder's thumbnails are its owner's, made once for everyone
	// it is shared with.
	if t, ok, err := p.shared(username, filePath); err != nil {
		return nil, "", err
	} else if ok {
		return p.ensurePreview(t.owner, t.path, size, generated)
	}

	meta, err := p.GetByPath(username, filePath)
	if err != nil {
		return nil, "", err
	}
	if !HasPreview(meta.MimeType) {
		return nil, "", fmt.Errorf("no preview for %s", meta.MimeType)
	}
	cache, err := p.ctx.UserCache(username)
	if err != nil {
		return nil, "", err
	}
	if _, done := p.oldThumbsGone.LoadOrStore(username, true); !done {
		if err := cache.RemoveAll(oldThumbDir); err != nil {
			p.log.Error("remove old thumbnails", "user", username, "err", err)
		}
	}

	name := fmt.Sprintf("%s/%d/%s.jpg", thumbDir, size, path.Base(meta.ETag))
	// Opening rather than statting marks the thumbnail used, so the cache
	// sweep keeps what the gallery's scans keep asking for.
	if f, err := cache.Open(name); err == nil {
		return cache, name, f.Close()
	} else if !errors.Is(err, iofs.ErrNotExist) {
		return nil, "", fmt.Errorf("open thumbnail: %w", err)
	}
	if err := p.generatePreview(username, filePath, meta, cache, name, size); err != nil {
		return nil, "", err
	}
	if generated != nil {
		*generated = true
	}
	return cache, name, nil
}

// carryThumbnails renames a file's cached thumbnails from its old etag to
// its new one. A move changes the etag but not a pixel, and without this a
// rename of a photo library regenerates every thumbnail in it. A thumbnail
// that cannot be carried is only made again, so failures are logged.
func (p *Plugin) carryThumbnails(username, oldETag, newETag string) {
	cache, err := p.ctx.UserCache(username)
	if err != nil {
		p.log.Warn("carry thumbnails", "user", username, "err", err)
		return
	}
	sizes, err := cache.ReadDir(thumbDir)
	if err != nil {
		p.log.Warn("carry thumbnails", "user", username, "err", err)
		return
	}
	for _, size := range sizes {
		if !size.IsDir() {
			continue
		}
		dir := thumbDir + "/" + size.Name() + "/"
		from, to := dir+path.Base(oldETag)+".jpg", dir+path.Base(newETag)+".jpg"
		if _, err := cache.Take(to, cache, from); err != nil && !errors.Is(err, iofs.ErrNotExist) {
			p.log.Warn("carry thumbnail", "user", username, "from", from, "err", err)
		}
	}
}

// generatePreview makes a size×size-bounded JPEG of an image, turned upright,
// and stores it in the cache as name.
func (p *Plugin) generatePreview(username, filePath string, meta *FileMeta, cache plugin.Storage, name string, size int) error {
	src, orientation, err := p.decodeSource(username, filePath, meta, cache)
	if err != nil {
		return err
	}
	b := src.Bounds()
	ow, oh := imaging.OrientedSize(b.Dx(), b.Dy(), orientation)
	w, h := imaging.Fit(ow, oh, size)
	w, h = imaging.OrientedSize(w, h, orientation)
	thumb := imaging.Orient(imaging.Resize(src, w, h), orientation)

	// Put replaces in one step, so a request for the same thumbnail
	// meanwhile never opens a half-written one.
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 80}); err != nil {
		return fmt.Errorf("encode thumbnail: %w", err)
	}
	if _, err := cache.Put(name, &buf); err != nil {
		return fmt.Errorf("write thumbnail: %w", err)
	}
	return nil
}

// decodeSource decodes the image a preview is made from. A JPEG Go's
// decoder refuses, being cut short, is converted as a format Go cannot read
// would be, keeping its own EXIF orientation.
func (p *Plugin) decodeSource(username, filePath string, meta *FileMeta, cache plugin.Storage) (image.Image, int, error) {
	if isNativeImage(meta.MimeType) {
		data, err := p.readAll(username, filePath)
		if err != nil {
			return nil, 1, err
		}
		src, _, orientation, err := imaging.Decode(data)
		if err == nil || errors.Is(err, safeimage.ErrTooLarge) || !imgconv.Handles(meta.MimeType) {
			if err != nil {
				return nil, 1, fmt.Errorf("decode image: %w", err)
			}
			return src, orientation, nil
		}
		p.log.Debug("decode failed, converting", "path", filePath, "err", err)
		src, _, err = p.decodeConverted(username, filePath, meta, cache)
		return src, imaging.Orientation(bytes.NewReader(data)), err
	}
	return p.decodeConverted(username, filePath, meta, cache)
}

func (p *Plugin) decodeConverted(username, filePath string, meta *FileMeta, cache plugin.Storage) (image.Image, int, error) {
	png, err := p.convertToPNG(username, cache, filePath, meta)
	if err != nil {
		return nil, 1, fmt.Errorf("convert to png: %w", err)
	}
	data, err := p.readCache(cache, png)
	if err != nil {
		return nil, 1, err
	}
	src, _, orientation, err := imaging.Decode(data)
	if err != nil {
		return nil, 1, fmt.Errorf("decode converted image: %w", err)
	}
	return src, orientation, nil
}

func (p *Plugin) readAll(username, filePath string) ([]byte, error) {
	rc, err := p.open(username, filePath)
	if err != nil {
		return nil, fmt.Errorf("open source: %w", err)
	}
	data, err := io.ReadAll(rc)
	if err = errors.Join(err, rc.Close()); err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	return data, nil
}

func (p *Plugin) readCache(cache plugin.Storage, name string) ([]byte, error) {
	f, err := cache.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open converted: %w", err)
	}
	data, err := io.ReadAll(f)
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, fmt.Errorf("read converted: %w", err)
	}
	return data, nil
}

// localCopy copies a user's file into the cache under name, for a program
// that needs a local path, and returns that path. The program reads the
// copy whatever mount the file is on.
func (p *Plugin) localCopy(username string, cache plugin.Storage, filePath, name string) (string, error) {
	rc, err := p.open(username, filePath)
	if err != nil {
		return "", err
	}
	_, err = cache.Put(name, rc)
	if err = errors.Join(err, rc.Close()); err != nil {
		return "", fmt.Errorf("copy %s: %w", filePath, err)
	}
	return cache.LocalPath(name)
}

// convertToPNG converts a non-native image format to PNG, caching the
// result, and returns the cache's name for it.
func (p *Plugin) convertToPNG(username string, cache plugin.Storage, filePath string, meta *FileMeta) (string, error) {
	mimeType := meta.MimeType
	name := "converted/" + path.Base(meta.ETag) + ".png"
	if _, err := cache.Stat(name); err == nil {
		return name, nil
	} else if !errors.Is(err, iofs.ErrNotExist) {
		return "", fmt.Errorf("stat converted: %w", err)
	}
	cachePath, err := cache.LocalPath(name)
	if err != nil {
		return "", err
	}

	input := "sources/" + path.Base(meta.ETag) + path.Ext(meta.Name)
	srcPath, err := p.localCopy(username, cache, filePath, input)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := cache.Remove(input); err != nil {
			p.log.Error("remove converter input", "user", username, "err", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The converters parse what a user uploaded, so each runs sandboxed,
	// reading only the upload and writing only the cache.
	spec := sandbox.Spec{Read: []string{srcPath}, Write: []string{filepath.Dir(cachePath)}}
	var cmd *exec.Cmd
	switch {
	case imgconv.Handles(mimeType):
		if err := imgconv.ToPNG(ctx, srcPath, cachePath); err != nil {
			return "", fmt.Errorf("convert %s: %w", mimeType, err)
		}
		return name, nil
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

	if _, err := cache.Stat(name); err != nil {
		return "", fmt.Errorf("conversion produced no output")
	}

	return name, nil
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
func (p *Plugin) PreviewPDF(username, filePath string) (plugin.StoredFile, error) {
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

	cache, err := p.ctx.UserCache(username)
	if err != nil {
		return nil, err
	}
	name := "previews/" + path.Base(meta.ETag) + ".pdf"
	if f, err := cache.Open(name); err == nil {
		return f, nil
	} else if !errors.Is(err, iofs.ErrNotExist) {
		return nil, fmt.Errorf("open preview: %w", err)
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
	ext := filepath.Ext(meta.Name)
	tmpFile := filepath.Join(tmpDir, "input"+ext)
	if err := p.copyOut(username, filePath, tmpFile); err != nil {
		return nil, err
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

	pdf, err := os.Open(pdfPath)
	if err != nil {
		return nil, fmt.Errorf("read converted pdf: %w", err)
	}
	_, err = cache.Put(name, pdf)
	if err = errors.Join(err, pdf.Close()); err != nil {
		return nil, fmt.Errorf("write cache: %w", err)
	}
	return cache.Open(name)
}

// copyOut copies a user's file to the local path dst, outside the data
// directory, for a program to read.
func (p *Plugin) copyOut(username, filePath, dst string) error {
	rc, err := p.open(username, filePath)
	if err != nil {
		return err
	}
	defer rc.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	_, err = io.Copy(f, rc)
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	return nil
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
