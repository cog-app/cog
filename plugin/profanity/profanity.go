// Package profanity implements a content filter plugin that replaces profane words
// in text and identifies profane segments in captions for audio muting/replacement.
package profanity

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	internalplugin "git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/plugin"
)

//go:embed seed
var seedFS embed.FS

const PluginID = "profanity"

// Plugin is the profanity filter plugin. It implements plugin.ContentFilter
// so other plugins (RSS, media) can discover and use it.
type Plugin struct {
	internalplugin.Protected
	ctx plugin.PluginContext
	log *slog.Logger

	mu    sync.RWMutex
	cache map[string]*WordList // username -> cached word list
}

// New creates a new profanity filter plugin.
func New() *Plugin {
	return &Plugin{
		cache: make(map[string]*WordList),
	}
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx
	p.log = ctx.Logger()

	pages := &Pages{plugin: p}
	ctx.RegisterPage("settings", http.HandlerFunc(pages.Settings))
	ctx.RegisterAction("POST", "settings", http.HandlerFunc(pages.SaveSettings))

	// Admin settings for TTS configuration.
	ctx.RegisterSettingsPage(plugin.SettingsPage{
		Name:    "Audio Cleaning",
		Handler: http.HandlerFunc(pages.AdminSettings),
		Action:  http.HandlerFunc(pages.SaveAdminSettings),
	})

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

// --- ContentFilter interface ---

func (p *Plugin) FilterText(username, text string) string {
	wl := p.loadWordList(username)
	if wl.Empty() {
		return text
	}
	return wl.FilterText(text)
}

func (p *Plugin) FilterHTML(username, html string) string {
	wl := p.loadWordList(username)
	if wl.Empty() {
		return html
	}
	return wl.FilterHTML(html)
}

func (p *Plugin) AudioRanges(username string, captions []plugin.CaptionSegment) []plugin.AudioRange {
	wl := p.loadWordList(username)
	if wl.Empty() {
		return nil
	}
	return wl.AudioRanges(captions)
}

func (p *Plugin) CleanAudioFile(ctx context.Context, username, inputURL, outputPath string, captions []plugin.CaptionSegment) error {
	wl := p.loadWordList(username)
	if wl.Empty() {
		return nil
	}
	ranges := wl.AudioRanges(captions)
	if len(ranges) == 0 {
		return nil
	}

	cfg := p.ttsConfig()
	return CleanAudioFile(ctx, cfg, inputURL, outputPath, ranges)
}

func (p *Plugin) ttsConfig() TTSConfig {
	db, err := p.rootDB()
	if err != nil {
		return TTSConfig{Engine: TTSNone}
	}
	return TTSConfig{
		Engine:  TTSEngine(p.getSetting(db, "tts_engine", "none")),
		Model:   p.getSetting(db, "tts_model", ""),
		Command: p.getSetting(db, "tts_command", ""),
	}
}

func (p *Plugin) rootDB() (*sql.DB, error) {
	db, err := p.ctx.RootDB()
	if err != nil {
		return nil, err
	}
	db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`)
	return db, nil
}

func (p *Plugin) getSetting(db *sql.DB, key, defaultVal string) string {
	var val string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&val)
	if err != nil {
		return defaultVal
	}
	return val
}

func (p *Plugin) setSetting(db *sql.DB, key, val string) error {
	_, err := db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, val)
	return err
}

// --- Word list management ---

func (p *Plugin) wordListPath(username string) string {
	return filepath.Join(p.ctx.DataDir(), username, "profanity.txt")
}

func (p *Plugin) loadWordList(username string) *WordList {
	p.mu.RLock()
	if wl, ok := p.cache[username]; ok {
		p.mu.RUnlock()
		return wl
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock.
	if wl, ok := p.cache[username]; ok {
		return wl
	}

	path := p.wordListPath(username)
	f, err := os.Open(path)
	if err != nil {
		// No custom list; return empty.
		wl := &WordList{}
		p.cache[username] = wl
		return wl
	}
	defer f.Close()

	wl := ParseWordList(f)
	p.cache[username] = wl
	return wl
}

func (p *Plugin) saveWordList(username, content string) error {
	path := p.wordListPath(username)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}

	// Invalidate cache.
	p.mu.Lock()
	delete(p.cache, username)
	p.mu.Unlock()

	return nil
}

func (p *Plugin) readWordListText(username string) string {
	data, err := os.ReadFile(p.wordListPath(username))
	if err != nil {
		return ""
	}
	return string(data)
}

// Seed creates a default profanity word list for a user.
func (p *Plugin) Seed(username string) error {
	path := p.wordListPath(username)
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}
	data, err := fs.ReadFile(seedFS, "seed/default.txt")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Ensure Plugin implements ContentFilter at compile time.
var _ plugin.ContentFilter = (*Plugin)(nil)

// DisplayName returns a friendly name for the settings sidebar.
func displayName() string { return "Profanity Filter" }

// wordListPreview returns the first N entries for display.
func wordListPreview(content string) []string {
	var entries []string
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries = append(entries, line)
		if len(entries) >= 5 {
			break
		}
	}
	return entries
}
