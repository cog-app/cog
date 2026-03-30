package notes

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed seed
var seedFS embed.FS

// Seed creates sample notes for a user demonstrating advanced markdown features.
func (p *Plugin) Seed(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return fmt.Errorf("seed notes: %w", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&count)
	if count > 0 {
		return nil
	}

	if err := p.ensureNotesDirFor(username); err != nil {
		return fmt.Errorf("seed notes: ensure dir: %w", err)
	}

	entries, err := fs.Glob(seedFS, "seed/*.md")
	if err != nil {
		return fmt.Errorf("seed notes: glob: %w", err)
	}
	sort.Strings(entries)

	for _, path := range entries {
		data, err := fs.ReadFile(seedFS, path)
		if err != nil {
			p.log.Error("seed note: read failed", "path", path, "error", err)
			continue
		}

		title, category, favorite, content := parseFrontmatter(data)
		if title == "" {
			title = strings.TrimSuffix(strings.TrimPrefix(path, "seed/"), ".md")
		}

		filename := FilenameFromTitle(title)
		etag := ComputeETag(content)

		if err := p.writeNoteContent(username, filename, content); err != nil {
			p.log.Error("seed note: write failed", "title", title, "error", err)
			continue
		}

		note, err := CreateNoteMeta(db, filename, category, etag)
		if err != nil {
			p.log.Error("seed note: meta failed", "title", title, "error", err)
			continue
		}

		if favorite {
			ToggleFavorite(db, note.ID)
		}

		p.log.Info("seeded note", "title", title)
	}

	return nil
}

// parseFrontmatter extracts YAML frontmatter delimited by "---" lines.
// Returns title, category, favorite, and the remaining content body.
func parseFrontmatter(data []byte) (title, category string, favorite bool, content string) {
	const sep = "---"

	body := string(data)
	if !strings.HasPrefix(body, sep+"\n") {
		return "", "", false, body
	}

	rest := body[len(sep)+1:]
	end := strings.Index(rest, "\n"+sep+"\n")
	if end < 0 {
		return "", "", false, body
	}

	fm := rest[:end]
	content = strings.TrimLeft(rest[end+len("\n"+sep+"\n"):], "\n")

	for _, line := range bytes.Split([]byte(fm), []byte("\n")) {
		key, val, ok := strings.Cut(string(line), ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "title":
			title = val
		case "category":
			category = val
		case "favorite":
			favorite = val == "true"
		}
	}

	return title, category, favorite, content
}
