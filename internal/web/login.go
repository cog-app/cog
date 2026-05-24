package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/email"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/ui"
)

type loginHandler struct{ handler }

func userInfo(u *user.User, baseURL string) email.UserInfo {
	return email.UserInfo{
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Email:       u.Email,
		AvatarURL:   baseURL + "/avatar/" + u.ID,
	}
}

// safeNext validates a next-redirect parameter to prevent open redirects.
func safeNext(next string) string {
	if next != "" && strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
		return next
	}
	return "/"
}

// loginRedirect builds a redirect URL preserving the next parameter.
func loginRedirect(base, next string) string {
	if next != "" {
		return base + "?next=" + url.QueryEscape(next)
	}
	return base
}

func (h *loginHandler) ShowLogin(w http.ResponseWriter, r *http.Request) {
	if auth.UserFromContext(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	next := r.URL.Query().Get("next")
	regEnabled := h.p.Settings != nil && h.p.Settings.GetBool(settings.KeyPublicRegistration)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	ui.LoginPage(flashMsg, flashKind, next, regEnabled).Render(r.Context(), w)
}

func (h *loginHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")
	next := r.FormValue("next")

	// Rate limit by client IP.
	if h.p.RateLimiter != nil {
		ip := ratelimit.ClientIP(r)
		if h.p.RateLimiter.IsLimited(ip) {
			setFlash(w, "error", "Too many failed login attempts. Please try again later.")
			http.Redirect(w, r, loginRedirect("/login", next), http.StatusSeeOther)
			return
		}
	}

	u, err := h.p.Users.Authenticate(username, password)
	if err != nil {
		if h.p.RateLimiter != nil {
			h.p.RateLimiter.RecordFailure(ratelimit.ClientIP(r))
		}
		setFlash(w, "error", "Invalid username or password.")
		http.Redirect(w, r, loginRedirect("/login", next), http.StatusSeeOther)
		return
	}

	// Clear rate limit on success.
	if h.p.RateLimiter != nil {
		h.p.RateLimiter.Reset(ratelimit.ClientIP(r))
	}

	// If MFA is enabled, redirect to verification instead of creating a session.
	if u.MFAMethod != "" {
		pending, err := h.p.MFAPending.Create(u.ID, next)
		if err != nil {
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		cookieCfg.Set(w, &http.Cookie{
			Name:   auth.MFAPendingCookieName,
			Value:  pending.Token,
			MaxAge: 300, // 5 minutes
		})

		// For email OTP, generate and send the code now.
		if u.MFAMethod == "email" && u.Email != "" {
			if sender := h.p.EmailSender(); sender != nil {
				code, err := h.p.EmailOTP.Create(u.ID)
				if err != nil {
					h.p.Log.Error("create email otp", "err", err)
				} else if err := sender.SendOTP(h.p.Settings, email.EmailData{
					BaseURL:      h.p.Config.BaseURL,
					InstanceName: "Cog",
					User:         userInfo(u, h.p.Config.BaseURL),
					Code:         code,
				}); err != nil {
					h.p.Log.Error("send email otp", "err", err)
				}
			}
		}

		http.Redirect(w, r, loginRedirect("/login/mfa", next), http.StatusSeeOther)
		return
	}

	h.createSessionAndRedirect(w, r, u.ID, next)
}

func (h *loginHandler) createSessionAndRedirect(w http.ResponseWriter, r *http.Request, userID, next string) {
	sess, err := h.p.Sessions.Create(userID)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	cookieCfg.Set(w, &http.Cookie{
		Name:  auth.SessionCookieName,
		Value: sess.ID,
	})
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

func (h *loginHandler) ShowMFA(w http.ResponseWriter, r *http.Request) {
	// Validate the pending MFA cookie.
	cookie, err := r.Cookie(auth.MFAPendingCookieName)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	pending, err := h.p.MFAPending.Validate(cookie.Value)
	if err != nil || pending == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Look up the user to determine MFA method.
	u, err := h.p.Users.GetByID(pending.UserID)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	next := r.URL.Query().Get("next")
	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	ui.MFAPage(errMsg, next, u.MFAMethod).Render(r.Context(), w)
}

func (h *loginHandler) HandleMFA(w http.ResponseWriter, r *http.Request) {
	next := r.FormValue("next")
	code := r.FormValue("code")

	cookie, err := r.Cookie(auth.MFAPendingCookieName)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	pending, err := h.p.MFAPending.Validate(cookie.Value)
	if err != nil || pending == nil {
		setFlash(w, "error", "Session expired. Please log in again.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Rate limit MFA attempts.
	if h.p.RateLimiter != nil {
		ip := ratelimit.ClientIP(r)
		if h.p.RateLimiter.IsLimited(ip) {
			setFlash(w, "error", "Too many failed attempts. Please try again later.")
			http.Redirect(w, r, loginRedirect("/login/mfa", next), http.StatusSeeOther)
			return
		}
	}

	// Validate the code based on the user's MFA method.
	u, err := h.p.Users.GetByID(pending.UserID)
	if err != nil {
		setFlash(w, "error", "Session expired. Please log in again.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	var valid bool
	switch u.MFAMethod {
	case "totp":
		valid, err = h.p.TOTP.Validate(pending.UserID, code)
	case "email":
		valid, err = h.p.EmailOTP.Validate(pending.UserID, code)
	default:
		valid = false
	}
	if err != nil || !valid {
		if h.p.RateLimiter != nil {
			h.p.RateLimiter.RecordFailure(ratelimit.ClientIP(r))
		}
		setFlash(w, "error", "Invalid authentication code.")
		http.Redirect(w, r, loginRedirect("/login/mfa", next), http.StatusSeeOther)
		return
	}

	// MFA passed — clean up pending token and create session.
	h.p.MFAPending.Delete(cookie.Value)
	cookieCfg.Clear(w, auth.MFAPendingCookieName)

	if h.p.RateLimiter != nil {
		h.p.RateLimiter.Reset(ratelimit.ClientIP(r))
	}

	// Use next from the pending token if not provided in form.
	if next == "" {
		next = pending.NextURL
	}
	h.createSessionAndRedirect(w, r, pending.UserID, next)
}

func (h *loginHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		h.p.Sessions.Delete(cookie.Value)
	}
	cookieCfg.Clear(w, auth.SessionCookieName)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *loginHandler) ShowRegister(w http.ResponseWriter, r *http.Request) {
	if auth.UserFromContext(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if h.p.Settings == nil || !h.p.Settings.GetBool(settings.KeyPublicRegistration) {
		http.NotFound(w, r)
		return
	}
	next := r.URL.Query().Get("next")
	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	ui.RegisterPage(errMsg, next).Render(r.Context(), w)
}

func (h *loginHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if h.p.Settings == nil || !h.p.Settings.GetBool(settings.KeyPublicRegistration) {
		http.NotFound(w, r)
		return
	}

	// Rate limit registration by client IP.
	if h.p.RateLimiter != nil {
		ip := ratelimit.ClientIP(r)
		if h.p.RateLimiter.IsLimited(ip) {
			setFlash(w, "error", "Too many attempts. Please try again later.")
			http.Redirect(w, r, "/register", http.StatusSeeOther)
			return
		}
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")
	next := r.FormValue("next")

	if username == "" || password == "" {
		setFlash(w, "error", "Username and password are required.")
		http.Redirect(w, r, loginRedirect("/register", next), http.StatusSeeOther)
		return
	}
	if password != confirm {
		setFlash(w, "error", "Passwords do not match.")
		http.Redirect(w, r, loginRedirect("/register", next), http.StatusSeeOther)
		return
	}

	u, err := h.p.Users.Create(username, password)
	if err != nil {
		setFlash(w, "error", "Registration failed: "+err.Error())
		http.Redirect(w, r, loginRedirect("/register", next), http.StatusSeeOther)
		return
	}

	// Create root mount.
	if h.p.Volumes != nil && h.p.Mounts != nil {
		if defVol, err := h.p.Volumes.GetByName("default"); err == nil {
			subpath := u.Username + "/files"
			h.p.Mounts.Create(u.ID, "/", defVol.ID, subpath, false)
		}
	}

	h.createSessionAndRedirect(w, r, u.ID, next)
}

func (h *loginHandler) ShowForgotPassword(w http.ResponseWriter, r *http.Request) {
	flash := getFlash(w, r)
	var errMsg, successMsg string
	if flash != nil {
		if flash.Kind == "success" {
			successMsg = flash.Message
		} else {
			errMsg = flash.Message
		}
	}
	ui.ForgotPasswordPage(errMsg, successMsg).Render(r.Context(), w)
}

func (h *loginHandler) HandleForgotPassword(w http.ResponseWriter, r *http.Request) {
	// Rate limit.
	if h.p.RateLimiter != nil {
		ip := ratelimit.ClientIP(r)
		if h.p.RateLimiter.IsLimited(ip) {
			setFlash(w, "error", "Too many attempts. Please try again later.")
			http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
			return
		}
	}

	identifier := r.FormValue("identifier")

	sender := h.p.EmailSender()
	if sender == nil {
		setFlash(w, "error", "Email is not configured on this server.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}

	// Always show success message to prevent user enumeration.
	defer func() {
		setFlash(w, "success", "If an account with that username or email exists, a reset link has been sent.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
	}()

	// Try to find user by username or email.
	u, err := h.p.Users.GetByUsername(identifier)
	if err != nil {
		// Try by email — iterate users to find matching email.
		for candidate, iterErr := range h.p.Users.List() {
			if iterErr != nil {
				break
			}
			if candidate.Email == identifier {
				u = &candidate
				break
			}
		}
	}

	if u == nil || u.Email == "" {
		return
	}

	token, err := h.p.PasswordResets.Create(u.ID)
	if err != nil {
		h.p.Log.Error("create password reset token", "err", err)
		return
	}

	resetURL := h.p.Config.BaseURL + "/reset-password?token=" + token
	if err := sender.SendPasswordReset(h.p.Settings, email.EmailData{
		BaseURL:      h.p.Config.BaseURL,
		InstanceName: "Cog",
		User:         userInfo(u, h.p.Config.BaseURL),
		ResetURL:     resetURL,
	}); err != nil {
		h.p.Log.Error("send password reset email", "err", err)
	}
}

func (h *loginHandler) ShowResetPassword(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if _, err := h.p.PasswordResets.Validate(token); err != nil {
		setFlash(w, "error", "Invalid or expired reset link. Please request a new one.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}

	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	ui.ResetPasswordPage(errMsg, token).Render(r.Context(), w)
}

func (h *loginHandler) HandleResetPassword(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")

	if password != confirm {
		setFlash(w, "error", "Passwords do not match.")
		http.Redirect(w, r, "/reset-password?token="+token, http.StatusSeeOther)
		return
	}
	if password == "" {
		setFlash(w, "error", "Password cannot be empty.")
		http.Redirect(w, r, "/reset-password?token="+token, http.StatusSeeOther)
		return
	}

	userID, err := h.p.PasswordResets.Validate(token)
	if err != nil {
		setFlash(w, "error", "Invalid or expired reset link. Please request a new one.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}

	if err := h.p.Users.UpdatePassword(userID, password); err != nil {
		setFlash(w, "error", "Failed to update password.")
		http.Redirect(w, r, "/reset-password?token="+token, http.StatusSeeOther)
		return
	}

	h.p.PasswordResets.MarkUsed(token)
	h.p.Sessions.DeleteAllForUser(userID)

	setFlash(w, "success", "Password reset successfully. Please log in with your new password.")
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// BeginWebAuthnLogin starts a WebAuthn assertion for MFA verification.
func (h *loginHandler) BeginWebAuthnLogin(w http.ResponseWriter, r *http.Request) {
	if h.p.WebAuthn == nil {
		http.Error(w, "WebAuthn not configured", http.StatusServiceUnavailable)
		return
	}

	cookie, err := r.Cookie(auth.MFAPendingCookieName)
	if err != nil {
		http.Error(w, "no pending mfa", http.StatusUnauthorized)
		return
	}
	pending, err := h.p.MFAPending.Validate(cookie.Value)
	if err != nil || pending == nil {
		http.Error(w, "session expired", http.StatusUnauthorized)
		return
	}

	u, err := h.p.Users.GetByID(pending.UserID)
	if err != nil {
		http.Error(w, "user not found", http.StatusInternalServerError)
		return
	}

	displayName := u.DisplayName
	if displayName == "" {
		displayName = u.Username
	}

	data, err := h.p.WebAuthn.BeginLogin(u.ID, u.Username, displayName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// FinishWebAuthnLogin completes WebAuthn MFA verification and creates a session.
func (h *loginHandler) FinishWebAuthnLogin(w http.ResponseWriter, r *http.Request) {
	if h.p.WebAuthn == nil {
		http.Error(w, "WebAuthn not configured", http.StatusServiceUnavailable)
		return
	}

	cookie, err := r.Cookie(auth.MFAPendingCookieName)
	if err != nil {
		http.Error(w, "no pending mfa", http.StatusUnauthorized)
		return
	}
	pending, err := h.p.MFAPending.Validate(cookie.Value)
	if err != nil || pending == nil {
		http.Error(w, "session expired", http.StatusUnauthorized)
		return
	}

	u, err := h.p.Users.GetByID(pending.UserID)
	if err != nil {
		http.Error(w, "user not found", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	displayName := u.DisplayName
	if displayName == "" {
		displayName = u.Username
	}

	if err := h.p.WebAuthn.FinishLogin(u.ID, u.Username, displayName, body); err != nil {
		http.Error(w, "verification failed", http.StatusForbidden)
		return
	}

	// MFA passed — clean up and create session.
	h.p.MFAPending.Delete(cookie.Value)
	cookieCfg.Clear(w, auth.MFAPendingCookieName)

	sess, err := h.p.Sessions.Create(u.ID)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	cookieCfg.Set(w, &http.Cookie{
		Name:  auth.SessionCookieName,
		Value: sess.ID,
	})

	next := pending.NextURL
	if next == "" {
		next = "/"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"redirect": safeNext(next)})
}
