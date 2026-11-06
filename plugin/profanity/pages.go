package profanity

import (
	"git.duckfam.us/jj-software/cog/internal/httperr"
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
		httperr.Internal(w, r, err)
		return
	}
	plugin.SetTitle(w, "Profanity Filter")
	ui.Render(w, r, profanitySettingsPage(content, plugin.CSRFToken(r)))
}

// SaveSettings handles POST to save the word list.
func (h *Pages) SaveSettings(w http.ResponseWriter, r *http.Request) {
	username := plugin.Username(r)
	content := r.FormValue("wordlist")
	if err := h.plugin.saveWordList(username, content); err != nil {
		h.plugin.log.Error("save profanity word list", "user", username, "err", err)
		plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: "The word list was not saved: " + err.Error()})
		http.Redirect(w, r, "/apps/profanity/settings", http.StatusSeeOther)
		return
	}
	plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: "Word list saved."})
	http.Redirect(w, r, "/apps/profanity/settings", http.StatusSeeOther)
}

// AdminSettings shows the admin TTS/Whisper configuration page.
func (h *Pages) AdminSettings(w http.ResponseWriter, r *http.Request) {
	db, err := h.plugin.rootDB()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	cfg, err := h.plugin.settings(db)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	ui.Render(w, r, profanityAdminPage(cfg, plugin.CSRFToken(r)))
}

// SaveAdminSettings handles POST to save admin TTS configuration.
func (h *Pages) SaveAdminSettings(w http.ResponseWriter, r *http.Request) {
	db, err := h.plugin.rootDB()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if err := h.plugin.saveSettings(db, adminSettingsData{
		TTSEngine:  r.FormValue("tts_engine"),
		TTSModel:   r.FormValue("tts_model"),
		TTSCommand: r.FormValue("tts_command"),
	}); err != nil {
		h.plugin.log.Error("save profanity admin settings", "err", err)
		plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: "The settings were not saved: " + err.Error()})
		http.Redirect(w, r, "/admin/plugins/profanity", http.StatusSeeOther)
		return
	}
	plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: "Settings saved."})
	http.Redirect(w, r, "/admin/plugins/profanity", http.StatusSeeOther)
}

type adminSettingsData struct {
	TTSEngine  string
	TTSModel   string
	TTSCommand string
}
