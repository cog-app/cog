package profanity

import (
	"git.duckfam.us/jj-software/cog/ui"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// Pages holds the web UI handlers for the profanity plugin.
type Pages struct {
	plugin *Plugin
}

// Settings shows the user's profanity word list editor.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	username := plugin.Username(r)
	content := h.plugin.readWordListText(username)
	flash := r.URL.Query().Get("flash")
	plugin.SetTitle(w, "Profanity Filter")
	ui.Render(w, r, profanitySettingsPage(content, flash, plugin.CSRFToken(r)))
}

// SaveSettings handles POST to save the word list.
func (h *Pages) SaveSettings(w http.ResponseWriter, r *http.Request) {
	username := plugin.Username(r)
	content := r.FormValue("wordlist")
	if err := h.plugin.saveWordList(username, content); err != nil {
		http.Redirect(w, r, "/apps/profanity/settings?flash=Error+saving", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/apps/profanity/settings?flash=Saved", http.StatusSeeOther)
}

// AdminSettings shows the admin TTS/Whisper configuration page.
func (h *Pages) AdminSettings(w http.ResponseWriter, r *http.Request) {
	db, err := h.plugin.rootDB()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cfg := adminSettingsData{
		TTSEngine:  h.plugin.getSetting(db, "tts_engine", "none"),
		TTSModel:   h.plugin.getSetting(db, "tts_model", ""),
		TTSCommand: h.plugin.getSetting(db, "tts_command", ""),
	}
	flash := r.URL.Query().Get("flash")
	ui.Render(w, r, profanityAdminPage(cfg, flash, plugin.CSRFToken(r)))
}

// SaveAdminSettings handles POST to save admin TTS configuration.
func (h *Pages) SaveAdminSettings(w http.ResponseWriter, r *http.Request) {
	db, err := h.plugin.rootDB()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.plugin.setSetting(db, "tts_engine", r.FormValue("tts_engine"))
	h.plugin.setSetting(db, "tts_model", r.FormValue("tts_model"))
	h.plugin.setSetting(db, "tts_command", r.FormValue("tts_command"))
	http.Redirect(w, r, "/admin/plugins/profanity?flash=Saved", http.StatusSeeOther)
}

type adminSettingsData struct {
	TTSEngine  string
	TTSModel   string
	TTSCommand string
}
