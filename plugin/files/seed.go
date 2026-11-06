package files

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"
)

// Seed creates sample files and directories for a user.
func (p *Plugin) Seed(username string) error {
	type seedFile struct {
		path    string
		content string
	}
	pngData, err := generatePatternPNG()
	if err != nil {
		return fmt.Errorf("seed png: %w", err)
	}
	zipData, err := generateSeedZip()
	if err != nil {
		return fmt.Errorf("seed zip: %w", err)
	}
	tarData, err := generateSeedTarGz()
	if err != nil {
		return fmt.Errorf("seed tar.gz: %w", err)
	}
	seeds := []seedFile{
		{"Documents/welcome.md", "# Welcome to Cog\n\nThis is a sample document.\n"},
		{"Documents/notes.txt", "Some quick notes for testing.\n"},
		{"Photos/readme.txt", "Drop images here.\n"},
		{"Photos/pattern.png", string(pngData)},
		{"Projects/hello.go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello from Cog!\")\n}\n"},
		{"Archives/sample.zip", string(zipData)},
		{"Archives/project.tar.gz", string(tarData)},
	}

	fs, err := p.userFS(username)
	if err != nil {
		return err
	}
	for _, s := range seeds {
		vpath := "/" + s.path
		if _, err := fs.Stat(vpath); err == nil {
			continue
		}
		parentDir := "/" + filepath.Dir(s.path)
		if err := fs.Mkdir(parentDir); err != nil {
			p.log.Error("seed mkdir", "path", parentDir, "err", err)
			continue
		}
		if err := fs.Put(vpath, strings.NewReader(s.content)); err != nil {
			p.log.Error("seed write", "path", s.path, "err", err)
			continue
		}
		p.log.Info("seeded file", "path", s.path)
	}

	return nil
}

func generateSeedZip() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	files := map[string]string{
		"readme.txt":       "This is a sample ZIP archive.\n",
		"src/main.go":      "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello from archive!\")\n}\n",
		"src/util.go":      "package main\n\nfunc add(a, b int) int { return a + b }\n",
		"docs/guide.md":    "# Guide\n\nThis is documentation inside an archive.\n",
		"docs/config.json": `{"name": "sample", "version": "1.0"}` + "\n",
		"data/numbers.csv": "id,value\n1,100\n2,200\n3,300\n",
	}

	for name, content := range files {
		fh := &zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		}
		w, err := zw.CreateHeader(fh)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, content); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func generateSeedTarGz() ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	files := map[string]string{
		"README.md":       "# Project\n\nSample tar.gz archive for testing.\n",
		"Makefile":        "build:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n",
		"cmd/main.go":     "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"tar.gz project\")\n}\n",
		"internal/lib.go": "package internal\n\nfunc Version() string { return \"0.1.0\" }\n",
	}

	mtime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:    name,
			Size:    int64(len(content)),
			Mode:    0644,
			ModTime: mtime,
		}); err != nil {
			return nil, err
		}
		if _, err := io.WriteString(tw, content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func generatePatternPNG() ([]byte, error) {
	const size = 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			fx, fy := float64(x)/float64(size), float64(y)/float64(size)
			r := uint8(128 + 127*math.Sin(fx*2*math.Pi*3))
			g := uint8(128 + 127*math.Sin(fy*2*math.Pi*3+2))
			b := uint8(128 + 127*math.Sin((fx+fy)*2*math.Pi*2+4))
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
