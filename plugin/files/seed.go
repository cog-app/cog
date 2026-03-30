package files

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
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
	seeds := []seedFile{
		{"Documents/welcome.md", "# Welcome to Cog\n\nThis is a sample document.\n"},
		{"Documents/notes.txt", "Some quick notes for testing.\n"},
		{"Photos/readme.txt", "Drop images here.\n"},
		{"Photos/pattern.png", string(generatePatternPNG())},
		{"Projects/hello.go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello from Cog!\")\n}\n"},
		{"Archives/sample.zip", string(generateSeedZip())},
		{"Archives/project.tar.gz", string(generateSeedTarGz())},
	}

	if fs := p.userFS(username); fs != nil {
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
	} else {
		filesDir := p.ctx.UserFilesDir(username)
		for _, s := range seeds {
			fp := filepath.Join(filesDir, s.path)
			if _, err := os.Stat(fp); err == nil {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
				p.log.Error("seed mkdir", "path", filepath.Dir(fp), "err", err)
				continue
			}
			if err := os.WriteFile(fp, []byte(s.content), 0o644); err != nil {
				p.log.Error("seed write", "path", fp, "err", err)
				continue
			}
			p.log.Info("seeded file", "path", s.path)
		}
	}
	return nil
}

func generateSeedZip() []byte {
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
			continue
		}
		w.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}

func generateSeedTarGz() []byte {
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
		tw.WriteHeader(&tar.Header{
			Name:    name,
			Size:    int64(len(content)),
			Mode:    0644,
			ModTime: mtime,
		})
		tw.Write([]byte(content))
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

func generatePatternPNG() []byte {
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
	png.Encode(&buf, img)
	return buf.Bytes()
}
