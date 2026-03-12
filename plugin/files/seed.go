//go:build seed

package files

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
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
