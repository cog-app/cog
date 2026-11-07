package web

import (
	"encoding/json"
	"errors"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"io"
	"net/http"
	"net/url"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/email"
	pwpolicy "git.duckfam.us/jj-software/cog/internal/password"
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
	if auth.LocalPath(next) {
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
	if err := ui.LoginPage(flashMsg, flashKind, next, flash.Value("username"), regEnabled).Render(r.Context(), w); err != nil {
		h.p.Log.Error("render login page", "err", err)
	}
}

func (h *loginHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")
	next := r.FormValue("next")

	// The guard counts failures by client and by account (audit
	// security.md #51): one attacker hammering many accounts is refused,
	// and a distributed one rotating addresses against one account is
	// slowed.
	var u *user.User
	var authErr error
	_, err := h.p.Guard.Try(ratelimit.ClientIP(r), ratelimit.AccountKey(username), func() bool {
		u, authErr = h.p.Users.Authenticate(username, password)
		return authErr == nil
	})
	if errors.Is(err, ratelimit.ErrLimited) {
		setFlash(w, "error", "Too many failed login attempts. Please try again later.")
		http.Redirect(w, r, loginRedirect("/login", next), http.StatusSeeOther)
		return
	}
	if authErr != nil {
		if !errors.Is(authErr, user.ErrAuthFailed) {
			h.p.Log.Error("authenticate", "username", username, "err", authErr)
		}
		setFlashForm(w, "error", "Invalid username or password.", map[string]string{"username": username})
		http.Redirect(w, r, loginRedirect("/login", next), http.StatusSeeOther)
		return
	}

	// If MFA is enabled, redirect to verification instead of creating a session.
	if u.MFAMethod != "" {
		pending, err := h.p.MFAPending.Create(u.ID, next)
		if err != nil {
			h.p.Log.Error("create mfa pending", "user", u.ID, "err", err)
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
	sess, err := h.p.Sessions.Start(userID, r)
	if err != nil {
		h.p.Log.Error("create session", "user", userID, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Delete the user's prior session if the browser presented one (audit
	// security.md #49: re-login otherwise accumulates rows). Use Validate
	// to confirm the cookie maps to a real session before deleting — we
	// don't want a spoofed cookie to wipe an unrelated session.
	if cookie, cerr := r.Cookie(auth.SessionCookieName); cerr == nil {
		prior, verr := h.p.Sessions.Validate(cookie.Value)
		if verr != nil {
			h.p.Log.Warn("validate prior session on re-login", "err", verr)
		}
		if prior != nil && prior.UserID == userID {
			if derr := h.p.Sessions.Delete(prior.ID); derr != nil {
				h.p.Log.Warn("delete prior session on re-login", "err", derr)
			}
		}
	}

	sess.SetCookie(w, cookieCfg)
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
	if err != nil {
		h.p.Log.Error("validate mfa pending", "err", err)
	}
	if err != nil || pending == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Look up the user to determine MFA method.
	u, err := h.p.Users.GetByID(pending.UserID)
	if err != nil {
		h.p.Log.Error("mfa: look up user", "user", pending.UserID, "err", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	next := r.URL.Query().Get("next")
	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	ui.Render(w, r, ui.MFAPage(errMsg, next, u.MFAMethod))
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
		var errs []error
		if err != nil {
			errs = append(errs, err)
		}
		setFlash(w, "error", "Session expired. Please log in again.", errs...)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Rate limit MFA attempts by client and by account: whoever holds a
	// pending login already has the password, and a six-digit code falls
	// to enough guesses spread over many addresses.
	u, err := h.p.Users.GetByID(pending.UserID)
	if err != nil {
		setFlash(w, "error", "Session expired. Please log in again.", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Validate the code based on the user's MFA method.
	valid, err := h.p.Guard.Try(ratelimit.ClientIP(r), "mfa:"+pending.UserID, func() bool {
		var ok bool
		var vErr error
		switch u.MFAMethod {
		case "totp":
			ok, vErr = h.p.TOTP.Validate(pending.UserID, code)
		case "email":
			ok, vErr = h.p.EmailOTP.Validate(pending.UserID, code)
		}
		if vErr != nil {
			h.p.Log.Error("validate mfa code", "user", pending.UserID, "method", u.MFAMethod, "err", vErr)
		}
		return vErr == nil && ok
	})
	if errors.Is(err, ratelimit.ErrLimited) {
		setFlash(w, "error", "Too many failed attempts. Please try again later.")
		http.Redirect(w, r, loginRedirect("/login/mfa", next), http.StatusSeeOther)
		return
	}
	if !valid {
		setFlash(w, "error", "Invalid authentication code.")
		http.Redirect(w, r, loginRedirect("/login/mfa", next), http.StatusSeeOther)
		return
	}

	// MFA passed — clean up pending token and create session.
	if err := h.p.MFAPending.Delete(cookie.Value); err != nil {
		h.p.Log.Warn("delete mfa pending after success", "err", err)
	}
	cookieCfg.Clear(w, auth.MFAPendingCookieName)

	// Use next from the pending token if not provided in form.
	if next == "" {
		next = pending.NextURL
	}
	h.createSessionAndRedirect(w, r, pending.UserID, next)
}

func (h *loginHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		// MFA-pending tokens for this user are not cleaned up here:
		// MFAPendingStore has no list-by-user, so we rely on the short (5m)
		// TTL plus the per-user cleanup that fires on the next Create
		// (audit security.md #46). Just close the active session.
		if err := h.p.Sessions.Delete(cookie.Value); err != nil {
			h.p.Log.Warn("delete session on logout", "err", err)
		}
	}
	// Clear the MFA-pending cookie too in case the user logged out mid-MFA.
	cookieCfg.Clear(w, auth.MFAPendingCookieName)
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
	if err := ui.RegisterPage(errMsg, next, flash.Value("username")).Render(r.Context(), w); err != nil {
		h.p.Log.Error("render register page", "err", err)
	}
}

func (h *loginHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if h.p.Settings == nil || !h.p.Settings.GetBool(settings.KeyPublicRegistration) {
		http.NotFound(w, r)
		return
	}

	// Every registration counts against the client's limit, so one client
	// cannot create accounts without end.
	ip := ratelimit.ClientIP(r)
	if h.p.Guard.Limited(ip) {
		setFlash(w, "error", "Too many attempts. Please try again later.")
		http.Redirect(w, r, "/register", http.StatusSeeOther)
		return
	}
	h.p.Guard.Record(ip)

	username := r.FormValue("username")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")
	next := r.FormValue("next")

	keep := map[string]string{"username": username}
	if username == "" || password == "" {
		setFlashForm(w, "error", "Username and password are required.", keep)
		http.Redirect(w, r, loginRedirect("/register", next), http.StatusSeeOther)
		return
	}
	if password != confirm {
		setFlashForm(w, "error", "Passwords do not match.", keep)
		http.Redirect(w, r, loginRedirect("/register", next), http.StatusSeeOther)
		return
	}

	u, err := h.p.Users.Create(username, password)
	if errors.Is(err, user.ErrInvalidUsername) || errors.Is(err, pwpolicy.ErrWeak) || errors.Is(err, pwpolicy.ErrUnchecked) {
		// Says nothing about other accounts, so it is safe to show.
		setFlashForm(w, "error", err.Error(), keep)
		http.Redirect(w, r, loginRedirect("/register", next), http.StatusSeeOther)
		return
	}
	if err != nil {
		// Log the real error for ops; tell the user nothing specific so
		// "username already exists" doesn't leak account existence
		// (audit security.md #24).
		h.p.Log.Info("register failed", "username", username, "err", err)
		setFlashForm(w, "error", "Registration failed. Please try a different username.", keep)
		http.Redirect(w, r, loginRedirect("/register", next), http.StatusSeeOther)
		return
	}

	// The account exists either way, so a failed mount is logged rather
	// than undoing the registration; an admin can add the mount later.
	if err := createRootMount(h.p, u); err != nil {
		h.p.Log.Error("create root mount on register", "user", u.Username, "err", err)
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
	ui.Render(w, r, ui.ForgotPasswordPage(errMsg, successMsg))
}

func (h *loginHandler) HandleForgotPassword(w http.ResponseWriter, r *http.Request) {
	// Each request may send an email, so each counts against the client.
	ip := ratelimit.ClientIP(r)
	if h.p.Guard.Limited(ip) {
		setFlash(w, "error", "Too many attempts. Please try again later.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}
	h.p.Guard.Record(ip)

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

	// Try to find user by username or email — both indexed lookups, so the
	// response time doesn't leak account existence via O(N) list iteration
	// (audit security.md #23).
	u, err := h.p.Users.GetByUsername(identifier)
	if err != nil {
		if !errors.Is(err, user.ErrNotFound) {
			h.p.Log.Error("forgot password: look up username", "err", err)
		}
		eu, eerr := h.p.Users.GetByEmail(identifier)
		switch {
		case eerr == nil:
			u = eu
		case !errors.Is(eerr, user.ErrNotFound):
			h.p.Log.Error("forgot password: look up email", "err", eerr)
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
		if errors.Is(err, auth.ErrInvalidResetToken) {
			setFlash(w, "error", "Invalid or expired reset link. Please request a new one.")
			http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
			return
		}
		h.p.Log.Error("validate password reset token", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	ui.Render(w, r, ui.ResetPasswordPage(errMsg, token))
}

func (h *loginHandler) HandleResetPassword(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")

	back := "/reset-password?token=" + url.QueryEscape(token)
	if password != confirm {
		setFlash(w, "error", "Passwords do not match.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if password == "" {
		setFlash(w, "error", "Password cannot be empty.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	// The policy is asked before the link is spent, so a refused password
	// costs the user another try, not another link.
	if err := h.p.Users.CheckPassword(password); err != nil {
		setFlash(w, "error", err.Error())
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}

	// Using the token and checking it are one step, so the same link cannot
	// set two passwords.
	userID, err := h.p.PasswordResets.Consume(token)
	if errors.Is(err, auth.ErrInvalidResetToken) {
		setFlash(w, "error", "Invalid or expired reset link. Please request a new one.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}
	if err != nil {
		// Not a bad link: the check itself failed. Send the user back to the
		// form to try again rather than telling them the link is dead.
		setFlash(w, "error", "Something went wrong checking the reset link. Please try again.", err)
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}

	if err := h.p.Users.UpdatePassword(userID, password); err != nil {
		// The link is spent; a new one is the way to try again.
		setFlash(w, "error", "Your password could not be changed. Please request a new reset link.", err)
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}

	// Signing the user out everywhere is what makes a reset lock out
	// whoever had the old password; log the failure so admins notice if
	// their sessions linger.
	if err := h.p.SignOutEverywhere(userID); err != nil {
		h.p.Log.Error("sign out after reset", "user", userID, "err", err)
	}

	setFlash(w, "success", "Password reset. Sign in with your new password, and sign your apps in again.")
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
	if err != nil {
		h.p.Log.Error("validate mfa pending", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if pending == nil {
		http.Error(w, "session expired", http.StatusUnauthorized)
		return
	}

	u, err := h.p.Users.GetByID(pending.UserID)
	if err != nil {
		h.p.Log.Error("webauthn login: look up user", "user", pending.UserID, "err", err)
		http.Error(w, "user not found", http.StatusInternalServerError)
		return
	}

	displayName := u.DisplayName
	if displayName == "" {
		displayName = u.Username
	}

	data, err := h.p.WebAuthn.BeginLogin(u.ID, u.Username, displayName)
	if err != nil {
		h.p.Log.Error("begin webauthn login", "user", u.ID, "err", err)
		httperr.Internal(w, r, err)
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
	if err != nil {
		h.p.Log.Error("validate mfa pending", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if pending == nil {
		http.Error(w, "session expired", http.StatusUnauthorized)
		return
	}

	u, err := h.p.Users.GetByID(pending.UserID)
	if err != nil {
		h.p.Log.Error("webauthn login: look up user", "user", pending.UserID, "err", err)
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

	if err := h.p.WebAuthn.FinishLogin(u.ID, u.Username, displayName, body); errors.Is(err, auth.ErrWebAuthnRejected) {
		h.p.Log.Info("webauthn login verification failed", "user", u.ID, "err", err)
		http.Error(w, "verification failed", http.StatusForbidden)
		return
	} else if err != nil {
		h.p.Log.Error("webauthn login", "user", u.ID, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// MFA passed — clean up and create session.
	if err := h.p.MFAPending.Delete(cookie.Value); err != nil {
		h.p.Log.Warn("delete mfa pending after webauthn", "err", err)
	}
	cookieCfg.Clear(w, auth.MFAPendingCookieName)

	sess, err := h.p.Sessions.Start(u.ID, r)
	if err != nil {
		h.p.Log.Error("create session", "user", u.ID, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	sess.SetCookie(w, cookieCfg)

	next := pending.NextURL
	if next == "" {
		next = "/"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"redirect": safeNext(next)})
}
