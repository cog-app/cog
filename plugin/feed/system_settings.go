package feed

import (
	"database/sql"
	"fmt"
)

// SettingYouTubeMode is how remote presentations serve their segments,
// server wide (RootDB).
var SettingYouTubeMode = StringSetting("hls_youtube_mode", YouTubeDirect)

// RootDB returns feed's server-wide database, which holds the settings the
// admin chooses for every user in its feed_settings table.
func (p *Plugin) RootDB() (*sql.DB, error) {
	db, err := p.ctx.RootDB()
	if err != nil {
		return nil, fmt.Errorf("feed root db: %w", err)
	}
	if p.rootReady.Load() {
		return db, nil
	}
	// Idempotent, so two first callers racing here is harmless.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS feed_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
)`); err != nil {
		return nil, fmt.Errorf("feed root db: create feed_settings: %w", err)
	}
	p.rootReady.Store(true)
	return db, nil
}

// WhisperSettings is how the transcribe worker runs whisper.cpp.
type WhisperSettings struct {
	Model WhisperModel
	// ModelsDir holds the model files; empty means the user's cache.
	ModelsDir string
	// CLIPath is the whisper-cli binary; empty means whisper-cli on PATH.
	CLIPath string
}

// WhisperSettings reads the server's whisper settings.
func (p *Plugin) WhisperSettings() (WhisperSettings, error) {
	db, err := p.RootDB()
	if err != nil {
		return WhisperSettings{}, err
	}
	var ws WhisperSettings
	if ws.Model, err = SettingWhisperModel.Get(db); err != nil {
		return WhisperSettings{}, fmt.Errorf("feed: read whisper model: %w", err)
	}
	if ws.ModelsDir, err = SettingWhisperModelsDir.Get(db); err != nil {
		return WhisperSettings{}, fmt.Errorf("feed: read whisper models dir: %w", err)
	}
	if ws.CLIPath, err = SettingWhisperCLIPath.Get(db); err != nil {
		return WhisperSettings{}, fmt.Errorf("feed: read whisper-cli path: %w", err)
	}
	return ws, nil
}
