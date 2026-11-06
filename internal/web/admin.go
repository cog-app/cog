package web

import (
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"git.duckfam.us/jj-software/cog/ui"
	"log/slog"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/email"
	"git.duckfam.us/jj-software/cog/internal/federation"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/logbuf"
	"git.duckfam.us/jj-software/cog/internal/sandbox"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
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
	retired, err := h.p.Users.Retired()
	if err != nil {
		h.p.Log.Error("list retired usernames", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	ui.Render(w, r, adminUsersPage(pd, h.p.Users.List(), retired, flashMsg, flashKind))
}

// ReleaseUsername lets a new account take the name of a deleted one.
func (h *adminHandler) ReleaseUsername(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	err := h.p.Users.Release(name)
	if errors.Is(err, user.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		setFlash(w, "error", "The name could not be released.", err)
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	setFlash(w, "success", fmt.Sprintf("“%s” can be used by a new account.", name))
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
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
	if errors.Is(err, user.ErrUsernameRetired) {
		setFlashForm(w, "error", fmt.Sprintf("“%s” belonged to a deleted account. Release it under Users first if it should be reused.", username), keep)
		http.Redirect(w, r, "/admin/users/new", http.StatusSeeOther)
		return
	}
	if errors.Is(err, user.ErrUsernameTaken) {
		setFlashForm(w, "error", fmt.Sprintf("A user named “%s” already exists.", username), keep)
		http.Redirect(w, r, "/admin/users/new", http.StatusSeeOther)
		return
	}
	if errors.Is(err, user.ErrInvalidUsername) {
		setFlashForm(w, "error", err.Error(), keep)
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
			setFlash(w, "error", fmt.Sprintf("User “%s” was created, but could not be made an admin.", u.Username), err)
			http.Redirect(w, r, "/admin/users/"+u.ID, http.StatusSeeOther)
			return
		}
	}

	if err := createRootMount(h.p, u); err != nil {
		setFlash(w, "error", fmt.Sprintf("User “%s” was created, but their home folder could not be set up.", u.Username), err)
		http.Redirect(w, r, "/admin/users/"+u.ID, http.StatusSeeOther)
		return
	}

	setFlash(w, "success", fmt.Sprintf("User “%s” created.", u.Username))
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// createRootMount gives a new user a root mount on the default volume. With
// no volume or mount store, or no volume named "default", there is nothing to
// mount and it does nothing.
func createRootMount(p *services.Providers, u *user.User) error {
	if p.Volumes == nil || p.Mounts == nil {
		return nil
	}
	defVol, err := p.Volumes.GetByName("default")
	if errors.Is(err, volume.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create root mount for %s: %w", u.Username, err)
	}
	if _, err := p.Mounts.Create(u.ID, "/", defVol.ID, u.Username+"/files", false); err != nil {
		return fmt.Errorf("create root mount for %s: %w", u.Username, err)
	}
	return nil
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
	if errors.Is(err, user.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.p.Log.Error("edit user: look up user", "user", id, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	ui.Render(w, r, adminUserEditPage(pd, u, flashMsg, flashKind))
}

func (h *adminHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	before, err := h.p.Users.GetByID(id)
	if errors.Is(err, user.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		setFlash(w, "error", "Failed to update user.", err)
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}

	displayName := r.FormValue("display_name")
	isAdmin := r.FormValue("is_admin") == "1"
	// An address an admin sets counts as confirmed: they vouch for it.
	addr := strings.TrimSpace(r.FormValue("email"))
	if addr != "" {
		if parsed, err := mail.ParseAddress(addr); err != nil || parsed.Name != "" || parsed.Address != addr {
			setFlash(w, "error", "That doesn't look like an email address.")
			http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
			return
		}
	}
	// A deleted account's address still reaches this server in replies to
	// its invitations; it goes to someone else only once its name is
	// released.
	if addr != "" && !strings.EqualFold(addr, before.Email) {
		name, err := h.p.Users.RetiredWithEmail(addr)
		if err != nil {
			setFlash(w, "error", "Failed to update user.", err)
			http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
			return
		}
		if name != "" {
			setFlash(w, "error", fmt.Sprintf("That address belonged to the deleted account “%s”. Release that name under Users first if the address should be reused.", name))
			http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
			return
		}
	}
	err = h.p.Users.Update(id, user.UpdateOpts{DisplayName: &displayName, IsAdmin: &isAdmin, Email: &addr})
	if errors.Is(err, user.ErrEmailTaken) {
		setFlash(w, "error", "Another account has that email address.")
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}
	if err != nil {
		setFlash(w, "error", "Failed to update user.", err)
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
	if _, err := h.p.Users.GetByID(id); errors.Is(err, user.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		setFlash(w, "error", "Failed to reset password.", err)
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}
	newPass := r.FormValue("new_password")
	if newPass == "" {
		setFlash(w, "error", "Password cannot be empty.")
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}
	if err := h.p.Users.UpdatePassword(id, newPass); err != nil {
		setFlash(w, "error", "Failed to reset password.", err)
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}
	// Audit security.md #37: no captured cookie or app password minted
	// with the old password survives an admin-driven reset.
	if err := h.p.SignOutEverywhere(id); err != nil {
		h.p.Log.Error("sign out on admin password reset", "user", id, "err", err)
	}

	setFlash(w, "success", "Password reset. The user is signed out everywhere, including their apps.")
	http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
}

func (h *adminHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.p.DeleteUser(id); err != nil {
		h.p.Log.Error("delete user failed", "user", id, "err", err)
		httperr.Internal(w, r, err)
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
		h.p.Log.Error("list groups", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	ui.Render(w, r, adminGroupsPage(pd, groups, flashMsg, flashKind))
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
			setFlash(w, "error", "Group created, but its owner could not be added.", err)
			http.Redirect(w, r, "/admin/groups/"+g.ID, http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
}

func (h *adminHandler) EditGroupForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g, err := h.p.Groups.Get(id)
	if errors.Is(err, group.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.p.Log.Error("edit group: look up group", "group", id, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	pd := pageDataWithNav(r, "Group: "+g.Name, h.p.CSRFSecret, h.p.Plugins)
	gh := groupHandler{h.handler}
	d, err := gh.buildGroupDetailData(w, r, g, true, "/admin/groups")
	if err != nil {
		h.p.Log.Error("edit group: build detail", "group", id, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	ui.Render(w, r, adminGroupDetailPage(pd, d))
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
		h.p.Log.Error("remove group member", "group", groupID, "member", memberID, "err", err)
		httperr.Internal(w, r, err)
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
	ui.Render(w, r, adminLogsPage(pd, entries, sources))
}

func (h *adminHandler) LogEntries(w http.ResponseWriter, r *http.Request) {
	var entries []logbuf.Entry
	if h.p.LogBuffer != nil {
		entries = h.p.LogBuffer.Entries()
	}
	entries = filterLogEntries(entries, r)
	ui.Render(w, r, logEntriesPartial(entries))
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
	var unsandboxed string
	if err := sandbox.Status(); err != nil {
		unsandboxed = "Previews, OCR and media tools run unsandboxed, with access to every user's files: " + err.Error()
	}
	ui.Render(w, r, adminPluginsPage(pd, plugins, unsandboxed))
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

// federationData is the federation card of the admin settings page.
type federationData struct {
	Enabled bool
	Peers   string // the admin's allowed servers, one per line
	// EnvPeers is COG_FEDERATION_PEERS, servers allowed whatever the
	// admin's list says.
	EnvPeers string
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
	fed := federationData{EnvPeers: h.p.Config.FederationPeers}
	if h.p.Settings != nil {
		var err error
		if mfaRequired, err = h.p.Settings.Get(settings.KeyMFARequired); err != nil {
			h.p.Log.Error("admin settings: read mfa setting", "err", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		if fed.Enabled, err = h.p.FedPolicy.Enabled(); err != nil {
			h.p.Log.Error("admin settings: read federation setting", "err", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		peers, err := h.p.Settings.Get(settings.KeyFederationPeers)
		if err != nil {
			h.p.Log.Error("admin settings: read federation peers", "err", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		fed.Peers = strings.Join(federation.ParsePeers(peers), "\n")
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
	ui.Render(w, r, adminSettingsPage(pd, pubReg, strconv.Itoa(expiryHours), strconv.Itoa(tombstoneDays), env, status, flashMsg, flashKind, mfaRequired, fed))
}

func (h *adminHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	pubReg := r.FormValue("public_registration") == "1"
	expiryStr := r.FormValue("invite_expiry_hours")
	expiryHours, err := strconv.Atoi(expiryStr)
	if err != nil || expiryHours < 1 {
		expiryHours = settings.DefaultInviteExpiryHours
	}
	if h.p.Settings != nil {
		days, err := strconv.Atoi(r.FormValue("dav_tombstone_days"))
		if err != nil || days < 1 {
			days = settings.DefaultDAVTombstoneDays
		}
		mfaRequired := r.FormValue("mfa_required")
		if mfaRequired != "all" && mfaRequired != "admins" {
			mfaRequired = ""
		}
		if err := h.p.Settings.SetMany(map[string]string{
			settings.KeyPublicRegistration: settings.FormatBool(pubReg),
			settings.KeyInviteExpiryHours:  strconv.Itoa(expiryHours),
			settings.KeyDAVTombstoneDays:   strconv.Itoa(days),
			settings.KeyMFARequired:        mfaRequired,
			settings.KeyFederationEnabled:  settings.FormatBool(r.FormValue("federation_enabled") == "1"),
			settings.KeyFederationPeers:    strings.Join(federation.ParsePeers(r.FormValue("federation_peers")), ","),
		}); err != nil {
			setFlash(w, "error", "Settings could not be saved.", err)
			http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
			return
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
		vals, err := h.p.Settings.GetMany(settings.KeySMTPHost, settings.KeySMTPUser, settings.KeySMTPPass, settings.KeySMTPFrom, settings.KeyEmailOverrideHTML, settings.KeyEmailOverrideText)
		if err != nil {
			h.p.Log.Error("admin email: read settings", "err", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		smtpData.Host = vals[settings.KeySMTPHost]
		smtpData.Port = strconv.Itoa(h.p.Settings.GetInt(settings.KeySMTPPort, 587))
		smtpData.User = vals[settings.KeySMTPUser]
		smtpData.Pass = vals[settings.KeySMTPPass]
		smtpData.From = vals[settings.KeySMTPFrom]
		overrideHTML = vals[settings.KeyEmailOverrideHTML]
		overrideText = vals[settings.KeyEmailOverrideText]
	}

	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	ui.Render(w, r, adminEmailPage(pd, smtpData, overrideHTML, overrideText, email.DefaultHTML(), email.DefaultText(), flashMsg, flashKind))
}

func (h *adminHandler) UpdateEmail(w http.ResponseWriter, r *http.Request) {
	if h.p.Settings != nil {
		// Validate template overrides before saving anything, so a bad
		// template doesn't leave the SMTP half of the form saved.
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

		smtpPort, err := strconv.Atoi(r.FormValue("smtp_port"))
		if err != nil || smtpPort < 1 {
			smtpPort = 587
		}
		if err := h.p.Settings.SetMany(map[string]string{
			settings.KeySMTPHost:          r.FormValue("smtp_host"),
			settings.KeySMTPPort:          strconv.Itoa(smtpPort),
			settings.KeySMTPUser:          r.FormValue("smtp_user"),
			settings.KeySMTPPass:          r.FormValue("smtp_pass"),
			settings.KeySMTPFrom:          r.FormValue("smtp_from"),
			settings.KeyEmailOverrideHTML: htmlOverride,
			settings.KeyEmailOverrideText: textOverride,
		}); err != nil {
			setFlash(w, "error", "Email settings could not be saved.", err)
			http.Redirect(w, r, "/admin/email", http.StatusSeeOther)
			return
		}
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
		ui.Render(w, r, smtpTestAlert("error", "SMTP not configured", "Save the SMTP host and from address first."))
		return
	}
	addr := fmt.Sprintf("%s:%d", sender.Host, sender.Port)
	c, err := smtp.Dial(addr)
	if err != nil {
		ui.Render(w, r, smtpTestAlert("error", "Connection failed", err.Error()))
		return
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		ui.Render(w, r, smtpTestAlert("error", "EHLO failed", err.Error()))
		return
	}
	if sender.User != "" {
		auth := smtp.PlainAuth("", sender.User, sender.Pass, sender.Host)
		if err := c.Auth(auth); err != nil {
			ui.Render(w, r, smtpTestAlert("error", "Authentication failed", err.Error()))
			return
		}
	}
	if err := c.Quit(); err != nil {
		ui.Render(w, r, smtpTestAlert("error", "QUIT failed", err.Error()))
		return
	}
	ui.Render(w, r, smtpTestAlert("success", "Connection successful", fmt.Sprintf("Connected to %s and authenticated.", addr)))
}
