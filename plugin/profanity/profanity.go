// Package profanity implements a content filter plugin that replaces profane words
// in text and identifies profane segments in captions for audio muting/replacement.
package profanity

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
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

// ErrNoRanges: the word list is empty or matches none of the captions, so
// there is nothing to clean and no file was written.
var ErrNoRanges = errors.New("profanity: nothing to mute")

// CleanAudioFile writes a muted copy of the audio at inputURL to outputPath.
// It returns ErrNoRanges, writing nothing, when there is nothing to mute.
func (p *Plugin) CleanAudioFile(ctx context.Context, username, inputURL, outputPath string, captions []plugin.CaptionSegment) error {
	wl := p.loadWordList(username)
	if wl.Empty() {
		return ErrNoRanges
	}
	ranges := wl.AudioRanges(captions)
	if len(ranges) == 0 {
		return ErrNoRanges
	}

	cfg, err := p.ttsConfig()
	if err != nil {
		return err
	}
	return CleanAudioFile(ctx, cfg, inputURL, outputPath, ranges)
}

func (p *Plugin) ttsConfig() (TTSConfig, error) {
	db, err := p.rootDB()
	if err != nil {
		return TTSConfig{}, err
	}
	s, err := p.settings(db)
	if err != nil {
		return TTSConfig{}, err
	}
	return TTSConfig{Engine: TTSEngine(s.TTSEngine), Model: s.TTSModel, Command: s.TTSCommand}, nil
}

func (p *Plugin) rootDB() (*sql.DB, error) {
	db, err := p.ctx.RootDB()
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`); err != nil {
		return nil, fmt.Errorf("create profanity settings table: %w", err)
	}
	return db, nil
}

func (p *Plugin) getSetting(db *sql.DB, key, defaultVal string) (string, error) {
	var val string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultVal, nil
	}
	if err != nil {
		return "", fmt.Errorf("read setting %s: %w", key, err)
	}
	return val, nil
}

// settings reads the admin's TTS settings, with their defaults.
func (p *Plugin) settings(db *sql.DB) (adminSettingsData, error) {
	var s adminSettingsData
	var err error
	if s.TTSEngine, err = p.getSetting(db, "tts_engine", "none"); err != nil {
		return s, err
	}
	if s.TTSModel, err = p.getSetting(db, "tts_model", ""); err != nil {
		return s, err
	}
	if s.TTSCommand, err = p.getSetting(db, "tts_command", ""); err != nil {
		return s, err
	}
	return s, nil
}

// saveSettings writes the admin's TTS settings together.
func (p *Plugin) saveSettings(db *sql.DB, s adminSettingsData) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin save settings: %w", err)
	}
	defer tx.Rollback()
	for _, kv := range [][2]string{
		{"tts_engine", s.TTSEngine},
		{"tts_model", s.TTSModel},
		{"tts_command", s.TTSCommand},
	} {
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, kv[0], kv[1]); err != nil {
			return fmt.Errorf("save setting %s: %w", kv[0], err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit settings: %w", err)
	}
	return nil
}

// --- Word list management ---

// wordList is the name of a user's word list in their storage.
const wordList = "words.txt"

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

	store, err := p.ctx.UserStorage(username)
	if err != nil {
		// Not cached, so the next call tries again.
		p.log.Error("open profanity word list", "user", username, "err", err)
		return &WordList{}
	}
	f, err := store.Open(wordList)
	if errors.Is(err, fs.ErrNotExist) {
		// No custom list; return empty.
		wl := &WordList{}
		p.cache[username] = wl
		return wl
	}
	if err != nil {
		// Not cached, so the next call tries again.
		p.log.Error("open profanity word list", "user", username, "err", err)
		return &WordList{}
	}
	defer f.Close()

	wl, err := ParseWordList(f)
	if err != nil {
		p.log.Error("read profanity word list", "user", username, "err", err)
		return &WordList{}
	}
	p.cache[username] = wl
	return wl
}

func (p *Plugin) saveWordList(username, content string) error {
	store, err := p.ctx.UserStorage(username)
	if err != nil {
		return err
	}
	if _, err := store.Put(wordList, strings.NewReader(content)); err != nil {
		return err
	}

	// Invalidate cache.
	p.mu.Lock()
	delete(p.cache, username)
	p.mu.Unlock()

	return nil
}

func (p *Plugin) readWordListText(username string) (string, error) {
	store, err := p.ctx.UserStorage(username)
	if err != nil {
		return "", fmt.Errorf("read word list: %w", err)
	}
	f, err := store.Open(wordList)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read word list: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("read word list: %w", err)
	}
	return string(data), nil
}

// Seed creates a default profanity word list for a user.
func (p *Plugin) Seed(username string) error {
	store, err := p.ctx.UserStorage(username)
	if err != nil {
		return err
	}
	if _, err := store.Stat(wordList); err == nil {
		return nil // already exists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat word list: %w", err)
	}
	f, err := seedFS.Open("seed/default.txt")
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = store.Put(wordList, f)
	return err
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
