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
	content, err := h.plugin.readWordListText(username)
	if err != nil {
		h.plugin.log.Error("profanity settings", "user", username, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	flash := r.URL.Query().Get("flash")
	plugin.SetTitle(w, "Profanity Filter")
	ui.Render(w, r, profanitySettingsPage(content, flash, plugin.CSRFToken(r)))
}

// SaveSettings handles POST to save the word list.
func (h *Pages) SaveSettings(w http.ResponseWriter, r *http.Request) {
	username := plugin.Username(r)
	content := r.FormValue("wordlist")
	if err := h.plugin.saveWordList(username, content); err != nil {
		h.plugin.log.Error("save profanity word list", "user", username, "err", err)
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
	cfg, err := h.plugin.settings(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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
	if err := h.plugin.saveSettings(db, adminSettingsData{
		TTSEngine:  r.FormValue("tts_engine"),
		TTSModel:   r.FormValue("tts_model"),
		TTSCommand: r.FormValue("tts_command"),
	}); err != nil {
		h.plugin.log.Error("save profanity admin settings", "err", err)
		http.Redirect(w, r, "/admin/plugins/profanity?flash=Error+saving", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/plugins/profanity?flash=Saved", http.StatusSeeOther)
}

type adminSettingsData struct {
	TTSEngine  string
	TTSModel   string
	TTSCommand string
}
