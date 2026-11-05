package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"git.duckfam.us/jj-software/cog/ui"
	"image/png"
	"io"
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/quota"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"golang.org/x/crypto/bcrypt"
)

type settingsHandler struct{ handler }

// RedirectToProfile redirects /settings → /settings/profile.
func (h *settingsHandler) RedirectToProfile(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings/profile", http.StatusSeeOther)
}

func (h *settingsHandler) ShowProfile(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Profile", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	if err := settingsProfilePage(pd, flashMsg, flashKind).Render(r.Context(), w); err != nil {
		h.p.Log.Error("render profile page", "err", err)
	}
}

func (h *settingsHandler) ShowSecurity(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	pd := pageDataWithNav(r, "Security", h.p.CSRFSecret, h.p.Plugins)

	appPasswords, err := h.p.AppPasswords.ListByUser(u.ID)
	if err != nil {
		h.p.Log.Error("list app passwords", "err", err)
	}

	var backupCodeCount int
	if u.MFAMethod == "totp" {
		backupCodeCount, err = h.p.TOTP.BackupCodeCount(u.ID)
		if err != nil {
			h.p.Log.Error("count backup codes", "user", u.Username, "err", err)
		}
	}

	var flash securityFlash
	if f := getFlash(w, r); f != nil {
		flash = securityFlash{Message: f.Message, Kind: f.Kind, For: f.Value("form"), Invalid: f.Value("invalid"), Fields: f.Form}
	}
	smtpConfigured := h.p.EmailSender() != nil
	var webauthnCreds []auth.WebAuthnCredential
	webauthnAvailable := h.p.WebAuthn != nil
	if webauthnAvailable {
		webauthnCreds, err = h.p.WebAuthn.ListCredentials(u.ID)
		if err != nil {
			h.p.Log.Error("list passkeys", "user", u.Username, "err", err)
		}
	}
	var sshKeys []keys.SSHKey
	if h.p.Config.SSHEnabled {
		sshKeys, err = h.p.SSHKeys.List(u.Username)
		if err != nil {
			h.p.Log.Error("list ssh keys", "user", u.Username, "err", err)
		}
	}
	if err := settingsSecurityPage(pd, flash, appPasswords, u.MFAMethod, backupCodeCount, smtpConfigured, webauthnAvailable, webauthnCreds, h.p.Config.SSHEnabled, sshKeys, h.listSignedInSessions(r, u.ID)).Render(r.Context(), w); err != nil {
		h.p.Log.Error("render security page", "err", err)
	}
}

// CreateAppPassword mints a new app password.
//
// Audit security.md #21: the plaintext token must never enter a cookie, log,
// or browser-history surface. HTMX clients receive an inline HTML fragment
// containing the token (single round-trip, response body only). Non-HTMX
// callers are redirected back to the settings page with a generic flash —
// the token is created but not shown, since legacy form-post can't deliver
// it safely. The UI always uses the HTMX path.
func (h *settingsHandler) CreateAppPassword(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	name := r.FormValue("name")
	htmx := r.Header.Get("HX-Request") == "true"

	if name == "" {
		if htmx {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			ui.Render(w, r, appPasswordErrorFragment("App password name is required."))
			return
		}
		setFlash(w, "error", "App password name is required.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	plaintext, ap, err := h.p.AppPasswords.Create(u.ID, name)
	if err != nil {
		if htmx {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			ui.Render(w, r, appPasswordErrorFragment("Failed to create app password: "+err.Error()))
			return
		}
		setFlash(w, "error", "Failed to create app password: "+err.Error())
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	if htmx {
		all, err := h.p.AppPasswords.ListByUser(u.ID)
		if err != nil {
			h.p.Log.Error("list app passwords", "err", err)
			all = []auth.AppPassword{*ap}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := appPasswordCreatedFragment(plaintext, all).Render(r.Context(), w); err != nil {
			h.p.Log.Error("render app password", "err", err)
		}
		return
	}
	// Non-HTMX fallback: token created but not exposed to avoid cookie leak.
	setFlash(w, "success", "App password created. Use the UI to view the token.")
	http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
}

func (h *settingsHandler) DeleteAppPassword(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")

	err := h.p.AppPasswords.Delete(id, u.ID)
	if errors.Is(err, auth.ErrAppPasswordNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.p.Log.Error("delete app password", "user", u.Username, "id", id, "err", err)
		http.Error(w, "The app password could not be revoked.", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// AddSSHKey adds a public key the user pasted, for SSH, SCP and SFTP logins.
func (h *settingsHandler) AddSSHKey(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	name := strings.TrimSpace(r.FormValue("name"))
	line := strings.TrimSpace(r.FormValue("public_key"))
	back := func(kind, msg string) {
		form := map[string]string{"form": "ssh-key"}
		if kind == "error" {
			form["invalid"] = "public_key"
			form["name"] = name
			form["public_key"] = line
		}
		setFlashForm(w, kind, msg, form)
		http.Redirect(w, r, "/settings/security#ssh-keys", http.StatusSeeOther)
	}

	if line == "" {
		back("error", "Paste a public key.")
		return
	}
	_, err := h.p.SSHKeys.Add(u.Username, name, line)
	switch {
	case errors.Is(err, keys.ErrInvalidKey):
		back("error", "That is not an SSH public key. Paste one line, such as the contents of ~/.ssh/id_ed25519.pub.")
	case errors.Is(err, keys.ErrKeyExists):
		back("error", "You have already added that key.")
	case err != nil:
		h.p.Log.Error("add ssh key", "user", u.Username, "err", err)
		back("error", "The key could not be added. Try again.")
	default:
		back("success", "SSH key added.")
	}
}

// DeleteSSHKey removes one of the user's SSH keys. It answers the HTMX
// request that removes the key's row.
func (h *settingsHandler) DeleteSSHKey(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")
	err := h.p.SSHKeys.Delete(u.Username, id)
	if errors.Is(err, keys.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.p.Log.Error("delete ssh key", "user", u.Username, "id", id, "err", err)
		http.Error(w, "The key could not be removed.", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *settingsHandler) ShowStorage(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Storage", h.p.CSRFSecret, h.p.Plugins)
	u := auth.UserFromContext(r.Context())

	var mounts []storageMount
	if h.p.Mounts != nil {
		for m, err := range h.p.Mounts.ListByUser(u.ID) {
			if err != nil {
				h.p.Log.Error("list mounts", "user", u.Username, "err", err)
				break
			}
			mounts = append(mounts, storageMount{Mount: m})
		}
	}

	// Volumes the user can mount, for the select and the mount names.
	var volumes []volume.Volume
	var err error
	if h.p.Access != nil {
		volumes, err = seq.Collect(h.p.Access.ListAccessible(u.ID))
	} else if h.p.Volumes != nil {
		volumes, err = seq.Collect(h.p.Volumes.List())
	}
	if err != nil {
		h.p.Log.Error("list volumes", "user", u.Username, "err", err)
	}
	volumeNames := make(map[string]string, len(volumes))
	for _, v := range volumes {
		volumeNames[v.ID] = v.Name
	}
	for i := range mounts {
		mounts[i].VolumeName = volumeNames[mounts[i].VolumeID]
	}

	var ownedVolumes []volume.Volume
	for v, err := range h.p.Volumes.List() {
		if err != nil {
			h.p.Log.Error("list owned volumes", "user", u.Username, "err", err)
			break
		}
		if v.OwnerUserID != nil && *v.OwnerUserID == u.ID {
			ownedVolumes = append(ownedVolumes, v)
		}
	}

	// Compute usage and quota for the user's root volume.
	var usageInfo storageUsageInfo
	if fp := h.filesPlugin(); fp != nil {
		used, err := fp.Usage(u.Username)
		if err != nil {
			h.p.Log.Error("storage usage", "user", u.Username, "err", err)
		}
		usageInfo.Used = used
		usageInfo.UsedLabel = quota.FormatBytes(used)
	}
	if h.p.Mounts != nil {
		rootMount, err := h.p.Mounts.GetUserRoot(u.ID)
		if err != nil {
			h.p.Log.Error("root mount", "user", u.Username, "err", err)
		} else {
			var groupIDs []string
			for g, err := range h.p.Groups.ListGroupsForUser(u.ID) {
				if err != nil {
					h.p.Log.Error("list groups for quota", "user", u.Username, "err", err)
					break
				}
				groupIDs = append(groupIDs, g.ID)
			}
			usageInfo.Quota, err = h.p.Quotas.EffectiveQuota(u.ID, rootMount.VolumeID, groupIDs)
			if err != nil {
				h.p.Log.Error("effective quota", "user", u.Username, "err", err)
			} else if usageInfo.Quota > 0 {
				usageInfo.QuotaLabel = quota.FormatBytes(usageInfo.Quota)
				usageInfo.Percent = min(int(usageInfo.Used*100/usageInfo.Quota), 100)
			}
		}
	}

	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	if err := settingsStoragePage(pd, flashMsg, flashKind, mounts, volumes, ownedVolumes, usageInfo).Render(r.Context(), w); err != nil {
		h.p.Log.Error("render storage page", "err", err)
	}
}

// storageUsageInfo holds storage usage and quota for display.
type storageUsageInfo struct {
	Used       int64
	UsedLabel  string
	Quota      int64  // -1 = unlimited
	QuotaLabel string // empty if unlimited
	Percent    int    // 0-100, only meaningful when Quota > 0
}

// filesPlugin returns the files plugin from the registry.
func (h *settingsHandler) filesPlugin() *files.Plugin {
	if plug, ok := h.p.Plugins.Plugin("files"); ok {
		if fp, ok := plug.(*files.Plugin); ok {
			return fp
		}
	}
	return nil
}

func (h *settingsHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	displayName := r.FormValue("display_name")
	email := r.FormValue("email")
	// The email address is where password resets and email codes go, so
	// whoever changes it must know the password: a session left signed in
	// is not enough to take the account over.
	if email != u.Email {
		if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(r.FormValue("current_password"))); err != nil {
			setFlash(w, "error", "Enter your current password to change your email address.")
			http.Redirect(w, r, "/settings/profile", http.StatusSeeOther)
			return
		}
	}
	if err := h.p.Users.Update(u.ID, user.UpdateOpts{DisplayName: &displayName, Email: &email}); err != nil {
		setFlash(w, "error", "Failed to update profile.", err)
	} else {
		setFlash(w, "success", "Profile updated.")
	}
	http.Redirect(w, r, "/settings/profile", http.StatusSeeOther)
}

func (h *settingsHandler) UpdatePassword(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	current := r.FormValue("current_password")
	newPass := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	// Errors show beside the password form, not at the top of the page.
	fail := func(msg, field string) {
		setFlashForm(w, "error", msg, map[string]string{"form": "password", "invalid": field})
		http.Redirect(w, r, "/settings/security#password", http.StatusSeeOther)
	}
	if newPass != confirm {
		fail("The new passwords don't match.", "confirm_password")
		return
	}
	if newPass == "" {
		fail("Enter a new password.", "new_password")
		return
	}

	// Verify current password.
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)); err != nil {
		fail("Your current password is incorrect.", "current_password")
		return
	}

	if err := h.p.Users.UpdatePassword(u.ID, newPass); err != nil {
		h.p.Log.Error("update password", "user", u.Username, "err", err)
		fail("The password could not be changed. Try again.", "")
		return
	}

	// Invalidate all other sessions for this user.
	sessID := auth.SessionIDFromContext(r.Context())
	if err := h.p.Sessions.DeleteAllExcept(u.ID, sessID); err != nil {
		h.p.Log.Error("delete sessions after password change", "err", err)
	}
	// App passwords outlive a change unless the user asks: a routine change
	// should not sign out every sync client, but one made because the old
	// password leaked must.
	msg := "Password changed."
	if r.FormValue("revoke_app_passwords") != "" {
		if err := h.p.AppPasswords.DeleteAllForUser(u.ID); err != nil {
			h.p.Log.Error("delete app passwords after password change", "user", u.Username, "err", err)
			fail("Your password was changed, but your app passwords could not be revoked. Revoke them below.", "")
			return
		}
		msg = "Password changed, and your apps signed out. Sign them in again."
	}

	setFlash(w, "success", msg)
	http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
}

func (h *settingsHandler) CreateMount(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volumeID := r.FormValue("volume_id")
	userPath := r.FormValue("user_path")
	volumeSubpath := r.FormValue("volume_subpath")

	if volumeID == "" || userPath == "" {
		setFlash(w, "error", "Volume and path are required.")
		http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
		return
	}

	// The mount reaches anything in the volume, so the user needs access to
	// all of it, and a mount of a volume shared read-only is read-only.
	perms, err := h.p.Access.Permissions(u.ID, volumeID)
	if errors.Is(err, volume.ErrNotFound) {
		perms, err = 0, nil
	}
	if err != nil {
		setFlash(w, "error", "Could not check access to that volume.", err)
		http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
		return
	}
	if perms&plugin.PermRead == 0 {
		setFlash(w, "error", "You do not have access to that volume.")
		http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
		return
	}
	readOnly := perms&plugin.PermWrite == 0

	_, err = h.p.Mounts.Create(u.ID, userPath, volumeID, volumeSubpath, readOnly)
	if err != nil {
		setFlash(w, "error", "Failed to create mount: "+err.Error())
	} else {
		setFlash(w, "success", "Mount added.")
	}
	http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
}

func (h *settingsHandler) CreateVolume(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	name := r.FormValue("name")
	if name == "" {
		setFlash(w, "error", "Name is required.")
		http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
		return
	}

	// A user's own volume is a remote one. A local path is the server's
	// filesystem — the data directory, other users' files, the host — and
	// only an admin, on the admin page, may make a volume of one.
	if r.FormValue("conn_type") != "webdav" {
		setFlash(w, "error", "Only WebDAV volumes can be added here.")
		http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
		return
	}
	uri, config, err := buildVolumeURIAndConfig(r)
	if err != nil {
		setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
		return
	}

	_, err = h.p.Volumes.Create(name, "", uri, &u.ID, config)
	if err != nil {
		setFlash(w, "error", "Failed to create volume: "+err.Error())
	} else {
		setFlash(w, "success", "Volume created.")
	}
	http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
}

func (h *settingsHandler) DeleteVolume(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")

	vol, err := h.p.Volumes.GetByID(id)
	if errors.Is(err, volume.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.p.Log.Error("delete volume: look up volume", "volume", id, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	// Only owner can delete their volume.
	if vol.OwnerUserID == nil || *vol.OwnerUserID != u.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := h.p.Volumes.Delete(id); err != nil {
		h.p.Log.Error("delete volume", "volume", id, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *settingsHandler) DeleteMount(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")

	// Prevent deleting root mount.
	m, err := h.p.Mounts.GetByID(id)
	if errors.Is(err, mount.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.p.Log.Error("delete mount: look up mount", "mount", id, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if m.UserID != u.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if m.UserPath == "/" {
		http.Error(w, "cannot delete root mount", http.StatusBadRequest)
		return
	}

	if err := h.p.Mounts.Delete(id); err != nil {
		h.p.Log.Error("delete mount", "mount", id, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// SetupTOTP begins TOTP enrollment: generates a key and redirects to the
// security page where the QR code and verification form will be displayed.
//
// If the user already has confirmed TOTP, re-enrollment requires the current
// password — otherwise a hijacked session could silently overwrite the
// authenticator secret, locking the legitimate user out.
func (h *settingsHandler) SetupTOTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	if u.MFAMethod == "totp" {
		pw := r.FormValue("current_password")
		if pw == "" {
			setFlash(w, "error", "Enter your current password to re-enroll TOTP.")
			http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
			return
		}
		if _, err := h.p.Users.Authenticate(u.Username, pw); err != nil {
			setFlash(w, "error", "Current password is incorrect.")
			http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
			return
		}
	}

	key, err := h.p.TOTP.BeginSetup(u.ID, u.Username, "Cog")
	if err != nil {
		setFlash(w, "error", "Failed to set up TOTP: "+err.Error(), err)
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	// Encode QR code as data URI.
	img, err := key.Image(200, 200)
	if err != nil {
		setFlash(w, "error", "Failed to generate QR code.", err)
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		setFlash(w, "error", "Failed to encode QR code.", err)
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

	// Store the data URI and secret in a flash for the security page to display.
	setFlash(w, "totp_setup", dataURI+"|"+key.Secret())
	http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
}

// ConfirmTOTP verifies the TOTP code and enables MFA.
func (h *settingsHandler) ConfirmTOTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	code := r.FormValue("code")

	backupCodes, err := h.p.TOTP.ConfirmSetup(u.ID, code)
	if err != nil {
		// Usually a mistyped code, but this is also where a database
		// failure surfaces, so keep the reason in the log.
		h.p.Log.Info("confirm totp setup", "user", u.Username, "err", err)
		setFlash(w, "error", "Invalid code. Please try again.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	// Store backup codes in flash for one-time display.
	var msg strings.Builder
	msg.WriteString("totp_enabled:")
	for i, code := range backupCodes {
		if i > 0 {
			msg.WriteString(",")
		}
		msg.WriteString(code)
	}
	setFlash(w, "success", msg.String())
	http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
}

// DisableTOTP removes MFA after verifying the current password.
func (h *settingsHandler) DisableTOTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	password := r.FormValue("password")

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		setFlash(w, "error", "Current password is incorrect.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	if err := h.p.TOTP.Disable(u.ID); err != nil {
		setFlash(w, "error", "Failed to disable TOTP.", err)
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	setFlash(w, "success", "Two-factor authentication disabled.")
	http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
}

// EnableEmailOTP enables email-based OTP as the user's MFA method.
func (h *settingsHandler) EnableEmailOTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	if u.Email == "" {
		setFlash(w, "error", "You must set an email address in your profile before enabling email OTP.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	if sender := h.p.EmailSender(); sender == nil {
		setFlash(w, "error", "Email is not configured on this server.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	_, err := h.p.Users.SetMFAMethod(u.ID, "email")
	if err != nil {
		setFlash(w, "error", "Failed to enable email OTP.", err)
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	setFlash(w, "success", "Email OTP enabled. A code will be sent to your email at each login.")
	http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
}

// DisableEmailOTP disables email OTP after verifying the current password.
func (h *settingsHandler) DisableEmailOTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	password := r.FormValue("password")

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		setFlash(w, "error", "Current password is incorrect.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	if _, err := h.p.Users.SetMFAMethod(u.ID, ""); err != nil {
		setFlash(w, "error", "Failed to disable email OTP.", err)
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	setFlash(w, "success", "Email OTP disabled.")
	http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
}

// RegenerateBackupCodes creates new backup codes after password verification.
func (h *settingsHandler) RegenerateBackupCodes(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	password := r.FormValue("password")

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		setFlash(w, "error", "Current password is incorrect.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	backupCodes, err := h.p.TOTP.RegenerateBackupCodes(u.ID)
	if err != nil {
		setFlash(w, "error", "Failed to regenerate backup codes.", err)
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	var msg strings.Builder
	msg.WriteString("totp_enabled:")
	for i, code := range backupCodes {
		if i > 0 {
			msg.WriteString(",")
		}
		msg.WriteString(code)
	}
	setFlash(w, "success", msg.String())
	http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
}

// BeginWebAuthnRegistration starts a WebAuthn credential registration.
func (h *settingsHandler) BeginWebAuthnRegistration(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if h.p.WebAuthn == nil {
		http.Error(w, "WebAuthn not configured", http.StatusServiceUnavailable)
		return
	}

	displayName := u.DisplayName
	if displayName == "" {
		displayName = u.Username
	}

	data, err := h.p.WebAuthn.BeginRegistration(u.ID, u.Username, displayName)
	if err != nil {
		h.p.Log.Error("begin passkey registration", "user", u.Username, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// FinishWebAuthnRegistration completes a WebAuthn credential registration.
func (h *settingsHandler) FinishWebAuthnRegistration(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if h.p.WebAuthn == nil {
		http.Error(w, "WebAuthn not configured", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var req struct {
		Name       string          `json:"name"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		req.Name = "Passkey"
	}

	displayName := u.DisplayName
	if displayName == "" {
		displayName = u.Username
	}

	if err := h.p.WebAuthn.FinishRegistration(u.ID, u.Username, displayName, req.Name, req.Credential); errors.Is(err, auth.ErrWebAuthnRejected) {
		h.p.Log.Info("finish passkey registration", "user", u.Username, "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	} else if err != nil {
		h.p.Log.Error("finish passkey registration", "user", u.Username, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if u.MFAMethod == "" {
		if _, err := h.p.Users.SetMFAMethod(u.ID, "webauthn"); err != nil {
			h.p.Log.Error("set mfa method after passkey registration", "user", u.Username, "err", err)
			http.Error(w, "Passkey saved, but it could not be made your sign-in method.", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// DeleteWebAuthnCredential removes a WebAuthn credential.
func (h *settingsHandler) DeleteWebAuthnCredential(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")

	if h.p.WebAuthn == nil {
		http.Error(w, "WebAuthn not configured", http.StatusServiceUnavailable)
		return
	}

	if err := h.p.WebAuthn.DeleteCredential(id, u.ID); errors.Is(err, auth.ErrCredentialNotFound) {
		http.Error(w, "Passkey not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.p.Log.Error("delete passkey", "user", u.Username, "id", id, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Leaving "webauthn" as the MFA method with no passkeys left would lock
	// the user out at the next login, so a failure here is the request's.
	count, err := h.p.WebAuthn.CredentialCount(u.ID)
	if err != nil {
		h.p.Log.Error("count passkeys", "user", u.Username, "err", err)
		http.Error(w, "Passkey removed, but your sign-in method could not be updated.", http.StatusInternalServerError)
		return
	}
	if count == 0 && u.MFAMethod == "webauthn" {
		if _, err := h.p.Users.SetMFAMethod(u.ID, ""); err != nil {
			h.p.Log.Error("clear mfa method after last passkey", "user", u.Username, "err", err)
			http.Error(w, "Passkey removed, but your sign-in method could not be updated.", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}

// sshKeyName is how a key is shown: its name, or its type for a key added
// with neither a name nor a comment.
func sshKeyName(k keys.SSHKey) string {
	if k.Name != "" {
		return k.Name
	}
	if typ, _, ok := strings.Cut(k.PublicKey, " "); ok {
		return typ + " key"
	}
	return "Unnamed key"
}
