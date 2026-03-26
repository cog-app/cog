package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"iter"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/quota"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
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
	settingsProfilePage(pd, flashMsg, flashKind).Render(r.Context(), w)
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
		backupCodeCount, _ = h.p.TOTP.BackupCodeCount(u.ID)
	}

	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	smtpConfigured := h.p.EmailSender() != nil
	var webauthnCreds []auth.WebAuthnCredential
	webauthnAvailable := h.p.WebAuthn != nil
	if webauthnAvailable {
		webauthnCreds, _ = h.p.WebAuthn.ListCredentials(u.ID)
	}
	settingsSecurityPage(pd, flashMsg, flashKind, appPasswords, u.MFAMethod, backupCodeCount, smtpConfigured, webauthnAvailable, webauthnCreds).Render(r.Context(), w)
}

func (h *settingsHandler) CreateAppPassword(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	name := r.FormValue("name")
	if name == "" {
		setFlash(w, "error", "App password name is required.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	plaintext, _, err := h.p.AppPasswords.Create(u.ID, name)
	if err != nil {
		setFlash(w, "error", "Failed to create app password: "+err.Error())
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	setFlash(w, "success", "token:"+plaintext)
	http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
}

func (h *settingsHandler) DeleteAppPassword(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")

	if err := h.p.AppPasswords.Delete(id, u.ID); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *settingsHandler) ShowStorage(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Storage", h.p.CSRFSecret, h.p.Plugins)
	u := auth.UserFromContext(r.Context())

	// Mounts as iterator.
	var mounts iter.Seq2[mount.Mount, error]
	if h.p.Mounts != nil {
		mounts = h.p.Mounts.ListByUser(u.ID)
	}

	// Volumes: collect for <select> dropdown + volumeNames map.
	var volumes []volume.Volume
	if h.p.Access != nil {
		volumes, _ = seq.Collect(h.p.Access.ListAccessible(u.ID))
	} else if h.p.Volumes != nil {
		volumes, _ = seq.Collect(h.p.Volumes.List())
	}

	volumeNames := make(map[string]string, len(volumes))
	for _, v := range volumes {
		volumeNames[v.ID] = v.Name
	}

	// Owned volumes as iterator + has-bool.
	ownedFilter := seq.Filter(h.p.Volumes.List(), func(v volume.Volume) bool {
		return v.OwnerUserID != nil && *v.OwnerUserID == u.ID
	})
	var ownedVolumes iter.Seq2[volume.Volume, error]
	var hasOwnedVolumes bool
	var ownedErr error
	_, ownedVolumes, ownedErr = seq.Peek(ownedFilter)
	hasOwnedVolumes = !errors.Is(ownedErr, seq.ErrEmpty)
	if !hasOwnedVolumes {
		ownedVolumes = seq.Filter(h.p.Volumes.List(), func(v volume.Volume) bool {
			return v.OwnerUserID != nil && *v.OwnerUserID == u.ID
		})
	}

	// Compute usage and quota for the user's root volume.
	var usageInfo storageUsageInfo
	if fp := h.filesPlugin(); fp != nil {
		used, err := fp.Usage(u.Username)
		if err == nil {
			usageInfo.Used = used
			usageInfo.UsedLabel = quota.FormatBytes(used)
		}
	}
	if h.p.Mounts != nil {
		if rootMount, err := h.p.Mounts.GetUserRoot(u.ID); err == nil {
			var groupIDs []string
			for g, err := range h.p.Groups.ListGroupsForUser(u.ID) {
				if err != nil {
					break
				}
				groupIDs = append(groupIDs, g.ID)
			}
			usageInfo.Quota = h.p.Quotas.EffectiveQuota(u.ID, rootMount.VolumeID, groupIDs)
			if usageInfo.Quota > 0 {
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
	settingsStoragePage(pd, flashMsg, flashKind, mounts, volumes, volumeNames, ownedVolumes, hasOwnedVolumes, usageInfo).Render(r.Context(), w)
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
	if err := h.p.Users.Update(u.ID, user.UpdateOpts{DisplayName: &displayName, Email: &email}); err != nil {
		setFlash(w, "error", "Failed to update profile.")
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

	if newPass != confirm {
		setFlash(w, "error", "New passwords do not match.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}
	if newPass == "" {
		setFlash(w, "error", "New password cannot be empty.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	// Verify current password.
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)); err != nil {
		setFlash(w, "error", "Current password is incorrect.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	if err := h.p.Users.UpdatePassword(u.ID, newPass); err != nil {
		setFlash(w, "error", "Failed to update password.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	// Invalidate all other sessions for this user.
	sessID := auth.SessionIDFromContext(r.Context())
	if err := h.p.Sessions.DeleteAllExcept(u.ID, sessID); err != nil {
		h.p.Log.Error("delete sessions after password change", "err", err)
	}

	setFlash(w, "success", "Password changed.")
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

	// Access check on the volume.
	if h.p.Access != nil {
		if ok, _ := h.p.Access.CanAccess(u.ID, volumeID); !ok {
			setFlash(w, "error", "You do not have access to that volume.")
			http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
			return
		}
	}

	_, err := h.p.Mounts.Create(u.ID, userPath, volumeID, volumeSubpath, false)
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
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Only owner can delete their volume.
	if vol.OwnerUserID == nil || *vol.OwnerUserID != u.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := h.p.Volumes.Delete(id); err != nil {
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
	if err != nil {
		http.NotFound(w, r)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// SetupTOTP begins TOTP enrollment: generates a key and redirects to the
// security page where the QR code and verification form will be displayed.
func (h *settingsHandler) SetupTOTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	key, err := h.p.TOTP.BeginSetup(u.ID, u.Username, "Cog")
	if err != nil {
		setFlash(w, "error", "Failed to set up TOTP: "+err.Error())
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	// Encode QR code as data URI.
	img, err := key.Image(200, 200)
	if err != nil {
		setFlash(w, "error", "Failed to generate QR code.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		setFlash(w, "error", "Failed to encode QR code.")
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
		setFlash(w, "error", "Invalid code. Please try again.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	// Store backup codes in flash for one-time display.
	msg := "totp_enabled:"
	for i, code := range backupCodes {
		if i > 0 {
			msg += ","
		}
		msg += code
	}
	setFlash(w, "success", msg)
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
		setFlash(w, "error", "Failed to disable TOTP.")
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
		setFlash(w, "error", "Failed to enable email OTP.")
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
		setFlash(w, "error", "Failed to disable email OTP.")
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
		setFlash(w, "error", "Failed to regenerate backup codes.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	msg := "totp_enabled:"
	for i, code := range backupCodes {
		if i > 0 {
			msg += ","
		}
		msg += code
	}
	setFlash(w, "success", msg)
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

	if err := h.p.WebAuthn.FinishRegistration(u.ID, u.Username, displayName, req.Name, req.Credential); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if u.MFAMethod == "" {
		h.p.Users.SetMFAMethod(u.ID, "webauthn")
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

	if err := h.p.WebAuthn.DeleteCredential(id, u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	count, _ := h.p.WebAuthn.CredentialCount(u.ID)
	if count == 0 && u.MFAMethod == "webauthn" {
		h.p.Users.SetMFAMethod(u.ID, "")
	}

	w.WriteHeader(http.StatusOK)
}
