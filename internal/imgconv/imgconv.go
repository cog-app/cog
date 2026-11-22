// Package imgconv decodes photos Go's decoders refuse, with programs that
// read them: HEIF and AVIF through libheif's heif-dec, and JPEGs cut short
// through ffmpeg, which shows the part that is there where Go's decoder
// gives up. Both run sandboxed, as they parse what users uploaded.
package imgconv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/imaging"
	"git.duckfam.us/jj-software/cog/internal/sandbox"
)

// ErrUnsupported is a file no program here can convert.
var ErrUnsupported = errors.New("imgconv: no converter for this image")

type kind int

const (
	unknown kind = iota
	heifFile
	jpegFile
)

var tools = sync.OnceValue(func() map[kind]string {
	found := map[kind]string{}
	if p, err := exec.LookPath("heif-dec"); err == nil {
		found[heifFile] = p
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		found[jpegFile] = p
	}
	return found
})

// HEIFTypes are the MIME types heif-dec converts.
var HEIFTypes = []string{"image/heif", "image/heic", "image/heif-sequence", "image/heic-sequence", "image/avif"}

// HandlesHEIF reports whether heif-dec is installed.
func HandlesHEIF() bool { return tools()[heifFile] != "" }

// Handles reports whether a file of mimeType can be converted here.
func Handles(mimeType string) bool {
	if slices.Contains(HEIFTypes, mimeType) {
		return HandlesHEIF()
	}
	return mimeType == "image/jpeg" && tools()[jpegFile] != ""
}

// sniff tells the formats apart by content: a file's name or the type it
// was uploaded as may be wrong.
func sniff(path string) (kind, error) {
	f, err := os.Open(path)
	if err != nil {
		return unknown, err
	}
	defer f.Close()
	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return unknown, fmt.Errorf("read header: %w", err)
	}
	switch {
	case hdr[0] == 0xff && hdr[1] == 0xd8:
		return jpegFile, nil
	case string(hdr[4:8]) == "ftyp":
		switch string(hdr[8:12]) {
		case "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1", "avif", "avis":
			return heifFile, nil
		}
	}
	return unknown, nil
}

// ToPNG converts the image at src to a PNG at dst, upright for a HEIF,
// whose rotation libheif applies, and as stored for a JPEG, whose EXIF
// orientation is the caller's to apply as for any other.
func ToPNG(ctx context.Context, src, dst string) error {
	k, err := sniff(src)
	if err != nil {
		return err
	}
	tool := tools()[k]
	if tool == "" {
		return ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var args []string
	switch k {
	case heifFile:
		args = []string{"--quiet", "--png-compression-level", "1", src, dst}
	case jpegFile:
		args = []string{"-v", "error", "-noautorotate",
			"-protocol_whitelist", "file", "-format_whitelist", "jpeg_pipe", "-f", "jpeg_pipe",
			"-i", "file:" + src, "-frames:v", "1", "-update", "1", "-y", dst}
	}
	cmd := sandbox.CommandContext(ctx, sandbox.Spec{Read: []string{src}, Write: []string{filepath.Dir(dst)}}, tool, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", filepath.Base(tool), err, bytes.TrimSpace(out))
	}
	if _, err := os.Stat(dst); err != nil {
		return fmt.Errorf("%s wrote nothing: %w", filepath.Base(tool), err)
	}
	return nil
}

// Decode converts the image at path and decodes the result, reporting the
// EXIF orientation still to be applied, as imaging.Decode does.
func Decode(ctx context.Context, path string) (img image.Image, orientation int, err error) {
	dir, err := os.MkdirTemp("", "cog-imgconv-")
	if err != nil {
		return nil, 1, err
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(dir))
	}()
	png := filepath.Join(dir, "out.png")
	if err := ToPNG(ctx, path, png); err != nil {
		return nil, 1, err
	}
	data, err := os.ReadFile(png)
	if err != nil {
		return nil, 1, err
	}
	img, _, _, err = imaging.Decode(data)
	if err != nil {
		return nil, 1, fmt.Errorf("decode converted: %w", err)
	}
	orientation = 1
	if k, err := sniff(path); err == nil && k == jpegFile {
		f, err := os.Open(path)
		if err != nil {
			return nil, 1, err
		}
		orientation = imaging.Orientation(f)
		if err := f.Close(); err != nil {
			return nil, 1, err
		}
	}
	return img, orientation, nil
}
