package web

import (
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
	adminUserNewPage(pd, errMsg).Render(r.Context(), w)
}

func (h *adminHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		setFlash(w, "error", "Username and password are required.")
		http.Redirect(w, r, "/admin/users/new", http.StatusSeeOther)
		return
	}

	u, err := h.p.Users.Create(username, password)
	if err != nil {
		setFlash(w, "error", "Failed to create user: "+err.Error())
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

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
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
	if _, err := h.p.Users.GetByID(id); err != nil {
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
	if h.p.LogBuffer != nil {
		entries = h.p.LogBuffer.Entries()
	}
	adminLogsPage(pd, entries).Render(r.Context(), w)
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

	regexStr := r.URL.Query().Get("regex")
	var re *regexp.Regexp
	if regexStr != "" {
		re, _ = regexp.Compile(regexStr)
	}

	if len(levelSet) == 0 && re == nil {
		return entries
	}

	filtered := make([]logbuf.Entry, 0, len(entries))
	for i := range entries {
		if len(levelSet) > 0 && !levelSet[entries[i].Level] {
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
	if h.p.Settings != nil {
		expiryHours = h.p.Settings.GetInt(settings.KeyInviteExpiryHours, settings.DefaultInviteExpiryHours)
	}

	var smtp smtpSettingsData
	if h.p.Settings != nil {
		smtp.Host, _ = h.p.Settings.Get(settings.KeySMTPHost)
		smtp.Port = strconv.Itoa(h.p.Settings.GetInt(settings.KeySMTPPort, 587))
		smtp.User, _ = h.p.Settings.Get(settings.KeySMTPUser)
		smtp.Pass, _ = h.p.Settings.Get(settings.KeySMTPPass)
		smtp.From, _ = h.p.Settings.Get(settings.KeySMTPFrom)
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
	adminSettingsPage(pd, pubReg, strconv.Itoa(expiryHours), smtp, env, status, flashMsg, flashKind).Render(r.Context(), w)
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
	}
	setFlash(w, "success", "Settings saved.")
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
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
