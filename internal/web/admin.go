package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/email"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/logbuf"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/user"
)

type adminHandler struct{ handler }

// --- Index (redirect to settings) ---

func (h *adminHandler) Index(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// --- Users ---

func (h *adminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Users", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminUsersPage(pd, h.p.Users.List(), flashMsg, flashKind).Render(r.Context(), w)
}

func (h *adminHandler) NewUserForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "New User", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	form := newUserForm{Username: flash.Value("username"), IsAdmin: flash.Value("is_admin") == "1"}
	if err := adminUserNewPage(pd, errMsg, form).Render(r.Context(), w); err != nil {
		h.p.Log.Error("render new user page", "err", err)
	}
}

func (h *adminHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	keep := map[string]string{"username": username, "is_admin": r.FormValue("is_admin")}

	if username == "" || password == "" {
		setFlashForm(w, "error", "Username and password are required.", keep)
		http.Redirect(w, r, "/admin/users/new", http.StatusSeeOther)
		return
	}

	u, err := h.p.Users.Create(username, password)
	if errors.Is(err, user.ErrUsernameTaken) {
		setFlashForm(w, "error", fmt.Sprintf("A user named “%s” already exists.", username), keep)
		http.Redirect(w, r, "/admin/users/new", http.StatusSeeOther)
		return
	}
	if err != nil {
		h.p.Log.Error("create user", "username", username, "err", err)
		setFlashForm(w, "error", "The user could not be created. The server log has the details.", keep)
		http.Redirect(w, r, "/admin/users/new", http.StatusSeeOther)
		return
	}

	// Set admin if checked.
	if r.FormValue("is_admin") == "1" {
		isAdmin := true
		if err := h.p.Users.Update(u.ID, user.UpdateOpts{IsAdmin: &isAdmin}); err != nil {
			h.p.Log.Error("set admin failed", "user", u.ID, "err", err)
		}
	}

	// Create root mount pointing to default volume.
	if h.p.Volumes != nil && h.p.Mounts != nil {
		if defVol, err := h.p.Volumes.GetByName("default"); err == nil {
			subpath := u.Username + "/files"
			if _, err := h.p.Mounts.Create(u.ID, "/", defVol.ID, subpath, false); err != nil {
				h.p.Log.Error("create mount failed", "user", u.Username, "err", err)
			}
		}
	}

	setFlash(w, "success", fmt.Sprintf("User “%s” created.", u.Username))
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// newUserForm is what the new-user form shows: empty, or what was typed
// before an error sent the admin back to it.
type newUserForm struct {
	Username string
	IsAdmin  bool
}

func (h *adminHandler) EditUserForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Edit User", h.p.CSRFSecret, h.p.Plugins)
	id := r.PathValue("id")
	u, err := h.p.Users.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminUserEditPage(pd, u, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *adminHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	before, err := h.p.Users.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	displayName := r.FormValue("display_name")
	isAdmin := r.FormValue("is_admin") == "1"
	if err := h.p.Users.Update(id, user.UpdateOpts{DisplayName: &displayName, IsAdmin: &isAdmin}); err != nil {
		setFlash(w, "error", "Failed to update user.")
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}

	// Audit security.md #37: when admin status changes, invalidate the
	// affected user's existing sessions so a captured pre-elevation cookie
	// can't ride the new privilege level. The user re-logs in to pick up
	// the change.
	if before.IsAdmin != isAdmin {
		if err := h.p.Sessions.DeleteAllForUser(id); err != nil {
			h.p.Log.Warn("revoke sessions on admin change", "user", id, "err", err)
		}
	}

	setFlash(w, "success", "User updated.")
	http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
}

func (h *adminHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.p.Users.GetByID(id); err != nil {
		http.NotFound(w, r)
		return
	}
	newPass := r.FormValue("new_password")
	if newPass == "" {
		setFlash(w, "error", "Password cannot be empty.")
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}
	if err := h.p.Users.UpdatePassword(id, newPass); err != nil {
		setFlash(w, "error", "Failed to reset password.")
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}
	// Audit security.md #37: revoke the target user's existing sessions so
	// any captured cookie can't survive an admin-driven password reset.
	if err := h.p.Sessions.DeleteAllForUser(id); err != nil {
		h.p.Log.Warn("revoke sessions on admin password reset", "user", id, "err", err)
	}

	// Invalidate all sessions for the target user.
	if err := h.p.Sessions.DeleteAllForUser(id); err != nil {
		h.p.Log.Error("delete sessions after admin password reset", "err", err)
	}

	setFlash(w, "success", "Password reset.")
	http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
}

func (h *adminHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Delete mounts before user (no ON DELETE CASCADE).
	if h.p.Mounts != nil {
		if err := h.p.Mounts.DeleteByUser(id); err != nil {
			h.p.Log.Error("delete mounts failed", "user", id, "err", err)
		}
	}
	if err := h.p.Users.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Return empty body — HTMX will remove the row.
	w.WriteHeader(http.StatusOK)
}

// --- Groups ---

func (h *adminHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Groups", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	groups, err := seq.Collect(h.p.Groups.List())
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	adminGroupsPage(pd, groups, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *adminHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" {
		setFlash(w, "error", "Group name is required.")
		http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
		return
	}
	ownerID := r.FormValue("owner_id")
	var ownerPtr *string
	if ownerID != "" {
		ownerPtr = &ownerID
	}
	g, err := h.p.Groups.Create(name, nil, ownerPtr)
	if err != nil {
		setFlash(w, "error", "Failed to create group: "+err.Error())
		http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
		return
	}
	if ownerID != "" {
		if err := h.p.Groups.AddMember(g.ID, ownerID, group.Owner); err != nil {
			h.p.Log.Error("add owner to group failed", "owner", ownerID, "group", g.ID, "err", err)
		}
	}
	http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
}

func (h *adminHandler) EditGroupForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g, err := h.p.Groups.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pd := pageDataWithNav(r, "Group: "+g.Name, h.p.CSRFSecret, h.p.Plugins)
	gh := groupHandler{h.handler}
	d, err := gh.buildGroupDetailData(w, r, g, true, "/admin/groups")
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	adminGroupDetailPage(pd, d).Render(r.Context(), w)
}

func (h *adminHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	gh := groupHandler{h.handler}
	gh.UpdateGroup(w, r)
}

func (h *adminHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.p.Groups.Delete(id); err != nil {
		setFlash(w, "error", "Failed to delete group: "+err.Error())
		http.Redirect(w, r, "/admin/groups/"+id, http.StatusSeeOther)
		return
	}
	setFlash(w, "success", "Group deleted.")
	http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
}

func (h *adminHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	gh := groupHandler{h.handler}
	gh.AddMember(w, r)
}

func (h *adminHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	memberID := r.PathValue("member_id")
	if err := h.p.Groups.RemoveMember(groupID, memberID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- Logs ---

func (h *adminHandler) ShowLogs(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Logs", h.p.CSRFSecret, h.p.Plugins)
	var entries []logbuf.Entry
	var sources []string
	if h.p.LogBuffer != nil {
		entries = h.p.LogBuffer.Entries()
		sources = h.p.LogBuffer.Sources()
	}
	adminLogsPage(pd, entries, sources).Render(r.Context(), w)
}

func (h *adminHandler) LogEntries(w http.ResponseWriter, r *http.Request) {
	var entries []logbuf.Entry
	if h.p.LogBuffer != nil {
		entries = h.p.LogBuffer.Entries()
	}
	entries = filterLogEntries(entries, r)
	logEntriesPartial(entries).Render(r.Context(), w)
}

func filterLogEntries(entries []logbuf.Entry, r *http.Request) []logbuf.Entry {
	levels := r.URL.Query()["level"]
	levelSet := make(map[slog.Level]bool, len(levels))
	for _, l := range levels {
		switch strings.ToUpper(l) {
		case "DEBUG":
			levelSet[slog.LevelDebug] = true
		case "INFO":
			levelSet[slog.LevelInfo] = true
		case "WARN":
			levelSet[slog.LevelWarn] = true
		case "ERROR":
			levelSet[slog.LevelError] = true
		}
	}

	sources := r.URL.Query()["source"]
	sourceSet := make(map[string]bool, len(sources))
	for _, s := range sources {
		if s == "server" {
			sourceSet[""] = true // "server" matches entries with no plugin source
		} else {
			sourceSet[s] = true
		}
	}

	regexStr := r.URL.Query().Get("regex")
	var re *regexp.Regexp
	if regexStr != "" {
		var err error
		if re, err = regexp.Compile(regexStr); err != nil {
			// Half-typed patterns arrive with every keystroke; until the
			// pattern is valid, match it as plain text.
			re = regexp.MustCompile(regexp.QuoteMeta(regexStr))
		}
	}

	if len(levelSet) == 0 && len(sourceSet) == 0 && re == nil {
		return entries
	}

	filtered := make([]logbuf.Entry, 0, len(entries))
	for i := range entries {
		if len(levelSet) > 0 && !levelSet[entries[i].Level] {
			continue
		}
		if len(sourceSet) > 0 && !sourceSet[entries[i].Source] {
			continue
		}
		if re != nil && !re.MatchString(entries[i].Message) {
			continue
		}
		filtered = append(filtered, entries[i])
	}
	return filtered
}

// --- Plugins ---

// PluginInfo holds display data for the admin plugins page.
type PluginInfo struct {
	ID        string
	Enabled   bool
	Protected bool
}

func (h *adminHandler) ListPlugins(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Plugins", h.p.CSRFSecret, h.p.Plugins)
	var plugins []PluginInfo
	if h.p.Plugins != nil {
		for _, id := range h.p.Plugins.PluginIDs() {
			plugins = append(plugins, PluginInfo{
				ID:        id,
				Enabled:   h.p.Plugins.IsEnabled(id),
				Protected: h.p.Plugins.IsPluginProtected(id),
			})
		}
	}
	adminPluginsPage(pd, plugins).Render(r.Context(), w)
}

func (h *adminHandler) TogglePlugin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.p.Plugins == nil || h.p.Plugins.IsPluginProtected(id) {
		http.Error(w, "cannot toggle this plugin", http.StatusBadRequest)
		return
	}
	enabled := !h.p.Plugins.IsEnabled(id)
	h.p.Plugins.SetEnabled(id, enabled)
	if h.p.Plugins.IsDiscovered(id) {
		setFlash(w, "info", "Restart required to apply changes to process plugins.")
	}
	http.Redirect(w, r, "/admin/plugins", http.StatusSeeOther)
}

// --- Settings ---

// smtpSettingsData holds SMTP form values for the template.
type smtpSettingsData struct {
	Host string
	Port string
	User string
	Pass string
	From string
}

// envConfigData holds read-only environment configuration for display.
type envConfigData struct {
	Addr    string
	DBPath  string
	BaseURL string
	DataDir string
}

// statusData holds runtime status information for the admin settings page.
type statusData struct {
	InstanceUptime time.Duration
	ServerUptime   time.Duration
	CommitHash     string
	CommitDate     time.Time
}

func buildStatusData(startedAt time.Time) statusData {
	sd := statusData{
		InstanceUptime: time.Since(startedAt),
		ServerUptime:   readServerUptime(),
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				sd.CommitHash = s.Value
				if len(sd.CommitHash) > 10 {
					sd.CommitHash = sd.CommitHash[:10]
				}
			case "vcs.time":
				sd.CommitDate, _ = time.Parse(time.RFC3339, s.Value)
			}
		}
	}
	return sd
}

func readServerUptime() time.Duration {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	field, _, _ := strings.Cut(string(data), " ")
	secs, err := strconv.ParseFloat(field, 64)
	if err != nil {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

func FormatDuration(d time.Duration) string {
	secs := int64(d.Seconds())
	if secs == 0 {
		return "0s"
	}
	var b strings.Builder
	for _, u := range [...][2]int64{
		{'y', 365 * 24 * 3600},
		{'d', 24 * 3600},
		{'h', 3600},
		{'m', 60},
		{'s', 1},
	} {
		if v := secs / u[1]; v > 0 {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, "%d%c", v, rune(u[0]))
			secs %= u[1]
		}
	}
	return b.String()
}

func (h *adminHandler) ShowSettings(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Settings", h.p.CSRFSecret, h.p.Plugins)
	pubReg := h.p.Settings != nil && h.p.Settings.GetBool(settings.KeyPublicRegistration)
	expiryHours := settings.DefaultInviteExpiryHours
	tombstoneDays := settings.DefaultDAVTombstoneDays
	if h.p.Settings != nil {
		expiryHours = h.p.Settings.GetInt(settings.KeyInviteExpiryHours, settings.DefaultInviteExpiryHours)
		tombstoneDays = h.p.Settings.GetInt(settings.KeyDAVTombstoneDays, settings.DefaultDAVTombstoneDays)
	}

	var mfaRequired string
	if h.p.Settings != nil {
		mfaRequired, _ = h.p.Settings.Get(settings.KeyMFARequired)
	}

	env := envConfigData{
		Addr:    h.p.Config.Addr,
		DBPath:  h.p.Config.DBPath,
		BaseURL: h.p.Config.BaseURL,
		DataDir: h.p.Config.DataDir,
	}

	status := buildStatusData(h.p.StartedAt)

	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminSettingsPage(pd, pubReg, strconv.Itoa(expiryHours), strconv.Itoa(tombstoneDays), env, status, flashMsg, flashKind, mfaRequired).Render(r.Context(), w)
}

func (h *adminHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	pubReg := r.FormValue("public_registration") == "1"
	expiryStr := r.FormValue("invite_expiry_hours")
	expiryHours, err := strconv.Atoi(expiryStr)
	if err != nil || expiryHours < 1 {
		expiryHours = settings.DefaultInviteExpiryHours
	}
	if h.p.Settings != nil {
		if err := h.p.Settings.SetBool(settings.KeyPublicRegistration, pubReg); err != nil {
			h.p.Log.Error("save setting failed", "key", "public_registration", "err", err)
		}
		if err := h.p.Settings.SetInt(settings.KeyInviteExpiryHours, expiryHours); err != nil {
			h.p.Log.Error("save setting failed", "key", "invite_expiry_hours", "err", err)
		}
		days, err := strconv.Atoi(r.FormValue("dav_tombstone_days"))
		if err != nil || days < 1 {
			days = settings.DefaultDAVTombstoneDays
		}
		if err := h.p.Settings.SetInt(settings.KeyDAVTombstoneDays, days); err != nil {
			h.p.Log.Error("save setting failed", "key", settings.KeyDAVTombstoneDays, "err", err)
		}

		// Save MFA enforcement setting.
		mfaRequired := r.FormValue("mfa_required")
		if mfaRequired != "all" && mfaRequired != "admins" {
			mfaRequired = ""
		}
		if err := h.p.Settings.Set(settings.KeyMFARequired, mfaRequired); err != nil {
			h.p.Log.Error("save setting failed", "key", "mfa_required", "err", err)
		}
	}
	setFlash(w, "success", "Settings saved.")
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// --- Email Settings ---

func (h *adminHandler) ShowEmail(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Email", h.p.CSRFSecret, h.p.Plugins)

	var smtpData smtpSettingsData
	var overrideHTML, overrideText string
	if h.p.Settings != nil {
		smtpData.Host, _ = h.p.Settings.Get(settings.KeySMTPHost)
		smtpData.Port = strconv.Itoa(h.p.Settings.GetInt(settings.KeySMTPPort, 587))
		smtpData.User, _ = h.p.Settings.Get(settings.KeySMTPUser)
		smtpData.Pass, _ = h.p.Settings.Get(settings.KeySMTPPass)
		smtpData.From, _ = h.p.Settings.Get(settings.KeySMTPFrom)
		overrideHTML, _ = h.p.Settings.Get(settings.KeyEmailOverrideHTML)
		overrideText, _ = h.p.Settings.Get(settings.KeyEmailOverrideText)
	}

	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminEmailPage(pd, smtpData, overrideHTML, overrideText, email.DefaultHTML(), email.DefaultText(), flashMsg, flashKind).Render(r.Context(), w)
}

func (h *adminHandler) UpdateEmail(w http.ResponseWriter, r *http.Request) {
	if h.p.Settings != nil {
		// Save SMTP settings.
		if err := h.p.Settings.Set(settings.KeySMTPHost, r.FormValue("smtp_host")); err != nil {
			h.p.Log.Error("save setting failed", "key", "smtp_host", "err", err)
		}
		smtpPort, err := strconv.Atoi(r.FormValue("smtp_port"))
		if err != nil || smtpPort < 1 {
			smtpPort = 587
		}
		if err := h.p.Settings.SetInt(settings.KeySMTPPort, smtpPort); err != nil {
			h.p.Log.Error("save setting failed", "key", "smtp_port", "err", err)
		}
		if err := h.p.Settings.Set(settings.KeySMTPUser, r.FormValue("smtp_user")); err != nil {
			h.p.Log.Error("save setting failed", "key", "smtp_user", "err", err)
		}
		if err := h.p.Settings.Set(settings.KeySMTPPass, r.FormValue("smtp_pass")); err != nil {
			h.p.Log.Error("save setting failed", "key", "smtp_pass", "err", err)
		}
		if err := h.p.Settings.Set(settings.KeySMTPFrom, r.FormValue("smtp_from")); err != nil {
			h.p.Log.Error("save setting failed", "key", "smtp_from", "err", err)
		}

		// Save template overrides — validate by parsing first.
		htmlOverride := r.FormValue("override_html")
		textOverride := r.FormValue("override_text")

		if htmlOverride != "" {
			if _, err := email.ValidateHTMLOverride(htmlOverride); err != nil {
				setFlash(w, "error", "Invalid HTML template: "+err.Error())
				http.Redirect(w, r, "/admin/email", http.StatusSeeOther)
				return
			}
		}
		if textOverride != "" {
			if _, err := email.ValidateTextOverride(textOverride); err != nil {
				setFlash(w, "error", "Invalid text template: "+err.Error())
				http.Redirect(w, r, "/admin/email", http.StatusSeeOther)
				return
			}
		}

		h.p.Settings.Set(settings.KeyEmailOverrideHTML, htmlOverride)
		h.p.Settings.Set(settings.KeyEmailOverrideText, textOverride)
	}
	setFlash(w, "success", "Email settings saved.")
	http.Redirect(w, r, "/admin/email", http.StatusSeeOther)
}

func (h *adminHandler) PreviewEmail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	data := email.SampleData(name, h.p.Config.BaseURL)
	format := r.URL.Query().Get("format")

	// POST: use overrides from the request body (live preview before saving).
	// GET: use saved overrides from the DB.
	var htmlBody, textBody string
	var err error
	if r.Method == "POST" {
		htmlBody, textBody, err = email.RenderWithOverrides(name, data, r.FormValue("override_html"), r.FormValue("override_text"))
	} else {
		htmlBody, textBody, err = email.Render(h.p.Settings, name, data)
	}
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><style>body{font-family:sans-serif;padding:1rem;color:#c00;}</style></head><body><strong>Template error:</strong> %s</body></html>`, err.Error())
		return
	}

	if format == "text" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><style>body{font-family:monospace;padding:1rem;white-space:pre-wrap;}</style></head><body>%s</body></html>`, textBody)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(htmlBody))
}

func (h *adminHandler) TestSMTP(w http.ResponseWriter, r *http.Request) {
	sender := h.p.EmailSender()
	if sender == nil {
		smtpTestAlert("error", "SMTP not configured", "Save the SMTP host and from address first.").Render(r.Context(), w)
		return
	}
	addr := fmt.Sprintf("%s:%d", sender.Host, sender.Port)
	c, err := smtp.Dial(addr)
	if err != nil {
		smtpTestAlert("error", "Connection failed", err.Error()).Render(r.Context(), w)
		return
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		smtpTestAlert("error", "EHLO failed", err.Error()).Render(r.Context(), w)
		return
	}
	if sender.User != "" {
		auth := smtp.PlainAuth("", sender.User, sender.Pass, sender.Host)
		if err := c.Auth(auth); err != nil {
			smtpTestAlert("error", "Authentication failed", err.Error()).Render(r.Context(), w)
			return
		}
	}
	c.Quit()
	smtpTestAlert("success", "Connection successful", fmt.Sprintf("Connected to %s and authenticated.", addr)).Render(r.Context(), w)
}
