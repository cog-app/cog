package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/compat"
	"git.duckfam.us/jj-software/cog/internal/federation"
	"git.duckfam.us/jj-software/cog/internal/jmap"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/quota"
	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/plugin"
	filesPlugin "git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/ui"
)

// New builds the full HTTP handler with all routes and middleware.
// All plugins (including first-party) are passed in via the plugins slice.
func New(p *services.Providers) http.Handler {
	cookieCfg = p.Cookies

	authMW := &auth.Middleware{
		Users:        p.Users,
		Sessions:     p.Sessions,
		AppPasswords: p.AppPasswords,
		RateLimiter:  p.RateLimiter,
		Cookies:      p.Cookies,
	}

	loginFlow := compat.NewLoginFlowHandler(p.Users, p.AppPasswords, p.Sessions, p.Config.BaseURL, p.CSRFSecret)
	loginFlow.RateLimiter = p.RateLimiter
	loginFlow.Clock = p.Clock
	// Run periodic GC on the in-memory pending-poll map so abandoned flows
	// don't leak (audit security.md #56). Goroutine lifetime = process.
	loginFlow.StartCleanup(context.Background())

	// Use the plugin registry's mux so plugin-registered routes share the same router.
	mux := p.Plugins.Mux()

	// --- Debug endpoints (admin-only; auth middleware applied to whole mux below) ---
	// pprof handlers read r.URL.Path and expect the "/debug/pprof/" prefix, so we
	// strip the "/admin" portion before dispatch.
	pprofGate := func(h http.Handler) http.Handler {
		return auth.RequireAuth(auth.RequireAdmin(http.StripPrefix("/admin", h)))
	}
	mux.Handle("GET /admin/debug/pprof/", pprofGate(http.HandlerFunc(pprof.Index)))
	mux.Handle("GET /admin/debug/pprof/cmdline", pprofGate(http.HandlerFunc(pprof.Cmdline)))
	mux.Handle("GET /admin/debug/pprof/profile", pprofGate(http.HandlerFunc(pprof.Profile)))
	mux.Handle("GET /admin/debug/pprof/symbol", pprofGate(http.HandlerFunc(pprof.Symbol)))
	mux.Handle("GET /admin/debug/pprof/trace", pprofGate(http.HandlerFunc(pprof.Trace)))

	// --- Static assets (content-hash cache busting via ui.Static) ---
	mux.Handle("GET /static/", http.StripPrefix("/static/", ui.Static))

	// Build quota checker for plugins.
	var fp *filesPlugin.Plugin
	if plug, ok := p.Plugins.Plugin("files"); ok {
		fp, _ = plug.(*filesPlugin.Plugin)
	}
	qc := buildQuotaChecker(p, fp)

	// --- Wire optional services for plugins ---
	p.Plugins.SetServices(buildRegistryServices(p.Shares, p.EmailSender, p.Users, p.Groups, qc, p.Access, p.Activities, p.Cookies))

	// --- Web UI handlers ---
	h := handler{p}
	loginH := loginHandler{h}
	settingsH := settingsHandler{h}
	dataH := dataHandler{h}
	adminH := adminHandler{h}
	adminVolH := adminVolumeHandler{h}
	volBrowserH := volumeBrowserHandler{h}
	avatarH := avatarHandler{h}
	groupH := groupHandler{h}

	// Login/logout/register — unauthenticated
	mux.HandleFunc("GET /login", loginH.ShowLogin)
	mux.HandleFunc("POST /login", loginH.HandleLogin)
	mux.HandleFunc("POST /logout", loginH.HandleLogout)
	mux.HandleFunc("GET /register", loginH.ShowRegister)
	mux.HandleFunc("POST /register", loginH.HandleRegister)
	mux.HandleFunc("GET /login/mfa", loginH.ShowMFA)
	mux.HandleFunc("POST /login/mfa", loginH.HandleMFA)
	mux.HandleFunc("POST /login/mfa/webauthn/begin", loginH.BeginWebAuthnLogin)
	mux.HandleFunc("POST /login/mfa/webauthn/finish", loginH.FinishWebAuthnLogin)
	mux.HandleFunc("GET /forgot-password", loginH.ShowForgotPassword)
	mux.HandleFunc("POST /forgot-password", loginH.HandleForgotPassword)
	mux.HandleFunc("GET /reset-password", loginH.ShowResetPassword)
	mux.HandleFunc("POST /reset-password", loginH.HandleResetPassword)

	// Avatar
	mux.Handle("GET /avatar/{id}", http.HandlerFunc(avatarH.Serve))
	mux.Handle("GET /avatar/{id}/initials", http.HandlerFunc(avatarH.ServeInitials))

	// Settings
	csrf := auth.CSRF{Secret: p.CSRFSecret, Cookies: p.Cookies, Log: p.Log}
	csrfWeb := func(h http.Handler) http.Handler {
		return auth.RequireAuthWeb(csrf.Require(h))
	}

	mux.Handle("GET /settings/{$}", auth.RequireAuthWeb(http.HandlerFunc(settingsH.RedirectToProfile)))
	mux.Handle("GET /settings/profile", auth.RequireAuthWeb(http.HandlerFunc(settingsH.ShowProfile)))
	mux.Handle("GET /settings/security", auth.RequireAuthWeb(http.HandlerFunc(settingsH.ShowSecurity)))
	mux.Handle("GET /settings/storage", auth.RequireAuthWeb(http.HandlerFunc(settingsH.ShowStorage)))
	mux.Handle("GET /settings/data", auth.RequireAuthWeb(http.HandlerFunc(dataH.ShowData)))
	mux.Handle("GET /settings/data/export/{id}", auth.RequireAuthWeb(http.HandlerFunc(dataH.Export)))
	mux.Handle("GET /settings/data/export.zip", auth.RequireAuthWeb(http.HandlerFunc(dataH.ExportAll)))
	mux.Handle("POST /settings/data/import", csrfWeb(http.HandlerFunc(dataH.Import)))
	mux.Handle("POST /settings/profile", csrfWeb(http.HandlerFunc(settingsH.UpdateProfile)))
	mux.Handle("POST /settings/password", csrfWeb(http.HandlerFunc(settingsH.UpdatePassword)))
	mux.Handle("POST /settings/mounts", csrfWeb(http.HandlerFunc(settingsH.CreateMount)))
	mux.Handle("DELETE /settings/mounts/{id}", csrfWeb(http.HandlerFunc(settingsH.DeleteMount)))
	mux.Handle("POST /settings/volumes", csrfWeb(http.HandlerFunc(settingsH.CreateVolume)))
	mux.Handle("DELETE /settings/volumes/{id}", csrfWeb(http.HandlerFunc(settingsH.DeleteVolume)))
	mux.Handle("POST /settings/totp/setup", csrfWeb(http.HandlerFunc(settingsH.SetupTOTP)))
	mux.Handle("POST /settings/totp/confirm", csrfWeb(http.HandlerFunc(settingsH.ConfirmTOTP)))
	mux.Handle("POST /settings/totp/disable", csrfWeb(http.HandlerFunc(settingsH.DisableTOTP)))
	mux.Handle("POST /settings/totp/backup-codes", csrfWeb(http.HandlerFunc(settingsH.RegenerateBackupCodes)))
	mux.Handle("POST /settings/email-otp/enable", csrfWeb(http.HandlerFunc(settingsH.EnableEmailOTP)))
	mux.Handle("POST /settings/email-otp/disable", csrfWeb(http.HandlerFunc(settingsH.DisableEmailOTP)))
	mux.Handle("POST /settings/webauthn/register/begin", csrfWeb(http.HandlerFunc(settingsH.BeginWebAuthnRegistration)))
	mux.Handle("POST /settings/webauthn/register/finish", csrfWeb(http.HandlerFunc(settingsH.FinishWebAuthnRegistration)))
	mux.Handle("DELETE /settings/webauthn/{id}", csrfWeb(http.HandlerFunc(settingsH.DeleteWebAuthnCredential)))
	mux.Handle("POST /settings/app-passwords", csrfWeb(http.HandlerFunc(settingsH.CreateAppPassword)))
	mux.Handle("DELETE /settings/app-passwords/{id}", csrfWeb(http.HandlerFunc(settingsH.DeleteAppPassword)))
	mux.Handle("DELETE /settings/sessions/{handle}", csrfWeb(http.HandlerFunc(settingsH.RevokeSession)))
	mux.Handle("POST /settings/sessions/revoke-others", csrfWeb(http.HandlerFunc(settingsH.RevokeOtherSessions)))
	mux.Handle("POST /settings/ssh-keys", csrfWeb(http.HandlerFunc(settingsH.AddSSHKey)))
	mux.Handle("DELETE /settings/ssh-keys/{id}", csrfWeb(http.HandlerFunc(settingsH.DeleteSSHKey)))
	mux.Handle("POST /settings/avatar", csrfWeb(http.HandlerFunc(avatarH.Upload)))
	mux.Handle("DELETE /settings/avatar", csrfWeb(http.HandlerFunc(avatarH.Delete)))

	// Settings: groups
	mux.Handle("GET /settings/groups", auth.RequireAuthWeb(http.HandlerFunc(groupH.ListGroups)))
	mux.Handle("GET /settings/groups/{id}", auth.RequireAuthWeb(http.HandlerFunc(groupH.ShowGroup)))
	mux.Handle("POST /settings/groups", csrfWeb(http.HandlerFunc(groupH.CreateGroup)))
	mux.Handle("POST /settings/groups/{id}", csrfWeb(http.HandlerFunc(groupH.UpdateGroup)))
	mux.Handle("POST /settings/groups/{id}/members", csrfWeb(http.HandlerFunc(groupH.AddMember)))
	mux.Handle("POST /settings/groups/{id}/members/{member_id}/type", csrfWeb(http.HandlerFunc(groupH.UpdateMemberType)))
	mux.Handle("DELETE /settings/groups/{id}/members/{member_id}", csrfWeb(http.HandlerFunc(groupH.RemoveMember)))
	mux.Handle("POST /settings/groups/{id}/invites", csrfWeb(http.HandlerFunc(groupH.CreateInvite)))
	mux.Handle("DELETE /settings/groups/{id}/invites/{invite_id}", csrfWeb(http.HandlerFunc(groupH.RevokeInvite)))
	mux.Handle("POST /settings/groups/{id}/delete", csrfWeb(http.HandlerFunc(groupH.DeleteGroup)))
	// Invite acceptance (works for both authenticated and unauthenticated users)
	mux.HandleFunc("GET /invite/{token}", groupH.AcceptInviteForm)
	mux.HandleFunc("POST /invite/{token}", groupH.AcceptInvite)

	// Admin — require auth + admin + CSRF on mutations
	adminAuth := func(h http.Handler) http.Handler {
		return auth.RequireAuthWeb(auth.RequireAdmin(h))
	}
	adminMut := func(h http.Handler) http.Handler {
		return auth.RequireAuthWeb(auth.RequireAdmin(csrf.Require(h)))
	}

	mux.Handle("GET /admin/{$}", adminAuth(http.HandlerFunc(adminH.Index)))
	mux.Handle("GET /admin/users", adminAuth(http.HandlerFunc(adminH.ListUsers)))
	mux.Handle("GET /admin/users/new", adminAuth(http.HandlerFunc(adminH.NewUserForm)))
	mux.Handle("POST /admin/users", adminMut(http.HandlerFunc(adminH.CreateUser)))
	mux.Handle("GET /admin/users/{id}", adminAuth(http.HandlerFunc(adminH.EditUserForm)))
	mux.Handle("POST /admin/users/{id}", adminMut(http.HandlerFunc(adminH.UpdateUser)))
	mux.Handle("POST /admin/users/{id}/password", adminMut(http.HandlerFunc(adminH.ResetPassword)))
	mux.Handle("DELETE /admin/users/{id}", adminMut(http.HandlerFunc(adminH.DeleteUser)))

	mux.Handle("GET /admin/groups", adminAuth(http.HandlerFunc(adminH.ListGroups)))
	mux.Handle("POST /admin/groups", adminMut(http.HandlerFunc(adminH.CreateGroup)))
	mux.Handle("GET /admin/groups/{id}", adminAuth(http.HandlerFunc(adminH.EditGroupForm)))
	mux.Handle("POST /admin/groups/{id}", adminMut(http.HandlerFunc(adminH.UpdateGroup)))
	mux.Handle("POST /admin/groups/{id}/delete", adminMut(http.HandlerFunc(adminH.DeleteGroup)))
	mux.Handle("POST /admin/groups/{id}/members", adminMut(http.HandlerFunc(adminH.AddMember)))
	mux.Handle("POST /admin/groups/{id}/members/{member_id}/type", adminMut(http.HandlerFunc(groupH.UpdateMemberType)))
	mux.Handle("DELETE /admin/groups/{id}/members/{member_id}", adminMut(http.HandlerFunc(adminH.RemoveMember)))
	mux.Handle("POST /admin/groups/{id}/invites", adminMut(http.HandlerFunc(groupH.CreateInvite)))
	mux.Handle("DELETE /admin/groups/{id}/invites/{invite_id}", adminMut(http.HandlerFunc(groupH.RevokeInvite)))
	mux.Handle("GET /admin/volumes", adminAuth(http.HandlerFunc(adminVolH.ListVolumes)))
	mux.Handle("GET /admin/volumes/new", adminAuth(http.HandlerFunc(adminVolH.NewVolumeForm)))
	mux.Handle("POST /admin/volumes", adminMut(http.HandlerFunc(adminVolH.CreateVolume)))
	mux.Handle("GET /admin/volumes/{id}", adminAuth(http.HandlerFunc(adminVolH.EditVolumeForm)))
	mux.Handle("POST /admin/volumes/{id}", adminMut(http.HandlerFunc(adminVolH.UpdateVolume)))
	mux.Handle("DELETE /admin/volumes/{id}", adminMut(http.HandlerFunc(adminVolH.DeleteVolume)))
	mux.Handle("POST /admin/volumes/{id}/quotas", adminMut(http.HandlerFunc(adminVolH.SetQuota)))
	mux.Handle("DELETE /admin/volumes/{id}/quotas/{quota_id}", adminMut(http.HandlerFunc(adminVolH.DeleteQuota)))
	mux.Handle("GET /api/admin/dir-suggest", adminAuth(http.HandlerFunc(adminVolH.DirSuggest)))

	mux.Handle("GET /admin/settings", adminAuth(http.HandlerFunc(adminH.ShowSettings)))
	mux.Handle("POST /admin/settings", adminMut(http.HandlerFunc(adminH.UpdateSettings)))

	mux.Handle("GET /admin/email", adminAuth(http.HandlerFunc(adminH.ShowEmail)))
	mux.Handle("POST /admin/email", adminMut(http.HandlerFunc(adminH.UpdateEmail)))
	mux.Handle("POST /admin/email/test-smtp", adminMut(http.HandlerFunc(adminH.TestSMTP)))
	mux.Handle("GET /admin/email/preview/{name}", adminAuth(http.HandlerFunc(adminH.PreviewEmail)))
	mux.Handle("POST /admin/email/preview/{name}", adminMut(http.HandlerFunc(adminH.PreviewEmail)))

	mux.Handle("GET /admin/plugins", adminAuth(http.HandlerFunc(adminH.ListPlugins)))
	mux.Handle("POST /admin/plugins/{id}/toggle", adminMut(http.HandlerFunc(adminH.TogglePlugin)))

	mux.Handle("GET /admin/logs", adminAuth(http.HandlerFunc(adminH.ShowLogs)))
	mux.Handle("GET /admin/logs/entries", adminAuth(http.HandlerFunc(adminH.LogEntries)))

	// Volume browser
	mux.Handle("GET /volumes/{id}/browse", auth.RequireAuthWeb(http.HandlerFunc(volBrowserH.Browse)))
	mux.Handle("GET /volumes/{id}/download", auth.RequireAuthWeb(http.HandlerFunc(volBrowserH.Download)))
	mux.Handle("POST /volumes/{id}/upload", csrfWeb(http.HandlerFunc(volBrowserH.Upload)))
	mux.Handle("POST /volumes/{id}/mkdir", csrfWeb(http.HandlerFunc(volBrowserH.MkDir)))
	mux.Handle("DELETE /volumes/{id}/file", csrfWeb(http.HandlerFunc(volBrowserH.DeleteFile)))

	// --- Status & OCS ---
	mux.HandleFunc("GET /status.php", compat.HandleStatus)
	mux.HandleFunc("GET /ocs/v1.php/cloud/capabilities", compat.HandleCapabilities)
	mux.HandleFunc("GET /ocs/v2.php/cloud/capabilities", compat.HandleCapabilities)
	ocsQuotaFn := buildOCSQuotaFunc(p, fp)
	mux.Handle("GET /ocs/v1.php/cloud/user", auth.RequireAuth(compat.HandleCurrentUser(ocsQuotaFn)))
	mux.Handle("GET /ocs/v2.php/cloud/user", auth.RequireAuth(compat.HandleCurrentUser(ocsQuotaFn)))
	mux.Handle("GET /ocs/v1.php/cloud/users/{id}", auth.RequireAuth(compat.HandleUserByID(p.Users)))
	mux.Handle("GET /ocs/v2.php/cloud/users/{id}", auth.RequireAuth(compat.HandleUserByID(p.Users)))
	// Provisioning API path variant (NC Android app uses this path for user info)
	mux.Handle("GET /ocs/v1.php/apps/provisioning_api/api/v2/users/{id}", auth.RequireAuth(compat.HandleUserByID(p.Users)))
	mux.Handle("GET /ocs/v2.php/apps/provisioning_api/api/v2/users/{id}", auth.RequireAuth(compat.HandleUserByID(p.Users)))

	// --- WebDAV file operations (NC clients) ---
	if fp != nil {
		webdav := &compat.WebDAVHandler{Log: p.Log.With("component", "webdav"), Files: fp, Clock: p.Clock}
		mux.Handle("/remote.php/dav/files/", auth.RequireAuth(webdav))

		// Chunked uploads (NC v2 protocol)
		chunked := &compat.ChunkedUploadHandler{
			Log:     p.Log.With("component", "chunked"),
			Files:   fp,
			TempDir: p.Config.DataDir,
			Clock:   p.Clock,
		}
		mux.Handle("/remote.php/dav/uploads/", auth.RequireAuth(chunked))

		// Trashbin (NC Android app uses this for deleted files)
		trashbin := &compat.TrashbinHandler{
			Log:   p.Log.With("component", "trashbin"),
			Files: fp,
		}
		mux.Handle("/remote.php/dav/trashbin/", auth.RequireAuth(trashbin))

		// Direct download API (NC Android app uses this instead of WebDAV GET)
		mux.Handle("POST /ocs/v2.php/apps/dav/api/v1/direct", auth.RequireAuth(webdav.HandleDirectDownload(p.Config.BaseURL)))
		mux.Handle("POST /ocs/v1.php/apps/dav/api/v1/direct", auth.RequireAuth(webdav.HandleDirectDownload(p.Config.BaseURL)))

		// Token-based direct file serving (no auth — token is the credential)
		mux.HandleFunc("GET /remote.php/dav/direct/", webdav.HandleDirectServe)

		// NC preview endpoint (needs auth + files plugin)
		previewH := &compat.PreviewHandler{Files: fp}
		mux.Handle("GET /index.php/core/preview", auth.RequireAuth(previewH))

		// WebDAV comments endpoint
		commentsH := &compat.CommentsHandler{Log: p.Log.With("component", "comments"), Files: fp, Users: p.Users}
		mux.Handle("/remote.php/dav/comments/files/", auth.RequireAuth(commentsH))

		// OCS sharing API (NC clients)
		sharingH := &compat.SharingHandler{
			Log:        p.Log.With("component", "sharing"),
			Files:      fp,
			Shares:     p.Shares,
			Users:      p.Users,
			Groups:     p.Groups,
			Federation: p.Federation,
			FedClient:  p.FedClient,
			Activities: p.Activities,
			BaseURL:    p.Config.BaseURL,
		}
		if p.Search != nil && p.Search.Propagator != nil {
			prop := p.Search.Propagator
			sharingH.OnShareCreate = func(rec *share.Record) {
				prop.OnShareCreate(&search.ShareRecord{
					ID: rec.ID, OwnerUserID: rec.OwnerUserID,
					ResourceType: rec.ResourceType, ResourceID: rec.ResourceID,
					SubjectType: rec.SubjectType, SubjectID: rec.SubjectID,
				})
			}
			sharingH.OnShareDelete = func(rec *share.Record) {
				prop.OnShareDelete(&search.ShareRecord{
					ID: rec.ID, OwnerUserID: rec.OwnerUserID,
					ResourceType: rec.ResourceType, ResourceID: rec.ResourceID,
					SubjectType: rec.SubjectType, SubjectID: rec.SubjectID,
				})
			}
		}
		for _, prefix := range []string{"/ocs/v1.php", "/ocs/v2.php"} {
			mux.Handle("GET "+prefix+"/apps/files_sharing/api/v1/shares", auth.RequireAuth(http.HandlerFunc(sharingH.HandleListShares)))
			mux.Handle("POST "+prefix+"/apps/files_sharing/api/v1/shares", auth.RequireAuth(http.HandlerFunc(sharingH.HandleCreateShare)))
			mux.Handle("GET "+prefix+"/apps/files_sharing/api/v1/shares/{shareId}", auth.RequireAuth(http.HandlerFunc(sharingH.HandleGetShare)))
			mux.Handle("PUT "+prefix+"/apps/files_sharing/api/v1/shares/{shareId}", auth.RequireAuth(http.HandlerFunc(sharingH.HandleUpdateShare)))
			mux.Handle("DELETE "+prefix+"/apps/files_sharing/api/v1/shares/{shareId}", auth.RequireAuth(http.HandlerFunc(sharingH.HandleDeleteShare)))
			mux.Handle("GET "+prefix+"/apps/files_sharing/api/v1/sharees", auth.RequireAuth(http.HandlerFunc(sharingH.HandleSharees)))
			mux.Handle("GET "+prefix+"/apps/files_sharing/api/v1/sharees_recommended", auth.RequireAuth(http.HandlerFunc(sharingH.HandleShareesRecommended)))
		}
	}

	// CalDAV/CardDAV: the root, principals and homes, over the plugins'
	// backends.
	mountDAV(mux, p, davShares{&shareStoreAdapter{p.Shares}, p})

	// JMAP (RFC 8620) over the same collections: contacts, and calendars
	// when configured.
	jmapH := p.JMAP.Handler()
	mux.Handle("GET "+jmap.WellKnown, jmapH)
	mux.Handle("/jmap/", jmapH)

	// WebDAV stub for non-file DAV paths (presence check, HEAD/OPTIONS/GET)
	mux.HandleFunc("/remote.php/dav", compat.HandleWebDAVStub)
	mux.HandleFunc("/remote.php/dav/", compat.HandleWebDAVStub)

	// NC-compatible OCS routes (avatar, notifications, user status, etc.)
	ncCompat := &compat.NCCompat{Users: p.Users, Files: fp, Activities: p.Activities, DataDir: p.Config.DataDir}
	ncCompat.RegisterRoutes(mux)

	// --- OCM Federation ---
	ocmH := &federation.OCMHandler{
		Log:     p.Log.With("component", "ocm"),
		Store:   p.Federation,
		Users:   p.Users,
		BaseURL: p.Config.BaseURL,
	}
	mux.HandleFunc("GET /.well-known/ocm-provider", ocmH.HandleOCMProvider)
	mux.HandleFunc("POST /ocm/shares", ocmH.HandleReceiveShare)
	mux.HandleFunc("POST /ocm/notifications", ocmH.HandleNotification)

	// OCS remote_shares API (NC-compatible federation endpoints).
	ocsRemoteH := &federation.OCSHandler{
		Log:           p.Log.With("component", "fed-ocs"),
		Store:         p.Federation,
		Users:         p.Users,
		Client:        p.FedClient,
		WriteOCS:      compat.WriteOCSReq,
		WriteOCSError: compat.WriteOCSErrorReq,
	}
	for _, prefix := range []string{"/ocs/v1.php", "/ocs/v2.php"} {
		base := prefix + "/apps/files_sharing/api/v1"
		mux.Handle("GET "+base+"/remote_shares", auth.RequireAuth(http.HandlerFunc(ocsRemoteH.HandleListRemoteShares)))
		mux.Handle("GET "+base+"/remote_shares/pending", auth.RequireAuth(http.HandlerFunc(ocsRemoteH.HandleListPendingShares)))
		mux.Handle("POST "+base+"/remote_shares/pending/{id}", auth.RequireAuth(http.HandlerFunc(ocsRemoteH.HandleAcceptShare)))
		mux.Handle("DELETE "+base+"/remote_shares/pending/{id}", auth.RequireAuth(http.HandlerFunc(ocsRemoteH.HandleDeclineShare)))
		mux.Handle("GET "+base+"/remote_shares/{id}", auth.RequireAuth(http.HandlerFunc(ocsRemoteH.HandleListRemoteShares)))
		mux.Handle("DELETE "+base+"/remote_shares/{id}", auth.RequireAuth(http.HandlerFunc(ocsRemoteH.HandleRemoveShare)))
	}

	// Federation token-based WebDAV (remote servers access our shared files).
	fedWebDAV := &federation.TokenWebDAVHandler{
		Log:    p.Log.With("component", "fed-webdav"),
		Store:  p.Federation,
		Shares: p.Shares,
		Users:  p.Users,
		Files:  fp,
	}
	mux.Handle("/public.php/webdav/", fedWebDAV)

	// --- Login Flow v2 ---
	mux.HandleFunc("POST /index.php/login/v2", loginFlow.HandleInit)
	mux.HandleFunc("POST /index.php/login/v2/poll", loginFlow.HandlePoll)
	mux.HandleFunc("GET /index.php/login/v2/grant", loginFlow.HandleGrant)
	mux.HandleFunc("POST /index.php/login/v2/grant", loginFlow.HandleGrant)
	mux.HandleFunc("GET /index.php/login/v2/done", loginFlow.HandleDone)

	// --- Admin share management (for system volumes) ---
	shareH := shareHandler{h}
	mux.Handle("GET /admin/shares/panel", adminAuth(http.HandlerFunc(shareH.SharePanelAdmin)))
	mux.Handle("POST /admin/shares/create", adminMut(http.HandlerFunc(shareH.CreateShareAdmin)))
	mux.Handle("DELETE /admin/shares/{id}", adminMut(http.HandlerFunc(shareH.DeleteShareAdmin)))

	// --- Share UI (generic, HTMX) ---
	mux.Handle("GET /api/shares/panel", auth.RequireAuth(http.HandlerFunc(shareH.SharePanel)))
	mux.Handle("POST /api/shares/create", csrfWeb(http.HandlerFunc(shareH.CreateShareForm)))
	mux.Handle("POST /api/shares/public-link-form", csrfWeb(http.HandlerFunc(shareH.CreatePublicLinkForm)))
	mux.Handle("POST /api/shares/federated", csrfWeb(http.HandlerFunc(shareH.CreateFederatedShareForm)))
	mux.Handle("DELETE /api/shares/{id}/delete", csrfWeb(http.HandlerFunc(shareH.DeleteShareHTMX)))
	mux.Handle("GET /api/users/search", auth.RequireAuth(http.HandlerFunc(shareH.UserSearch)))
	mux.Handle("GET /api/picker/search", auth.RequireAuth(http.HandlerFunc(shareH.PickerSearch)))

	// --- Search API ---
	searchH := searchHandler{h}
	mux.Handle("GET /api/search", auth.RequireAuth(http.HandlerFunc(searchH.Search)))
	mux.Handle("GET /api/search/partial", auth.RequireAuth(http.HandlerFunc(searchH.SearchPartial)))
	mux.Handle("GET /api/admin/search", adminAuth(http.HandlerFunc(searchH.SearchSystem)))

	// --- Files & Sharing API ---
	if fp != nil {
		filesAPI := filesPlugin.NewAPI(fp, p.Shares, p.Users)
		filesAPI.RegisterRoutes(mux)
	}

	return authMW.Wrap(mfaEnforcement(p)(htmlNotFound(p, mux)))
}

// htmlNotFound answers a browser asking for a page no route serves with the
// styled "page not found" page inside the normal shell, so the user can
// navigate on. API, DAV and other clients, which do not ask for HTML, keep the
// mux's plain answer.
func htmlNotFound(p *services.Providers, mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
			if _, pattern := mux.Handler(r); pattern == "" {
				pd := pageDataWithNav(r, "Page not found", p.CSRFSecret, p.Plugins)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				if err := ui.NotFoundPage(pd).Render(r.Context(), w); err != nil {
					p.Log.Error("render not found page", "path", r.URL.Path, "err", err)
				}
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// buildOCSQuotaFunc creates a QuotaFunc for the OCS API.
func buildOCSQuotaFunc(p *services.Providers, fp *filesPlugin.Plugin) compat.QuotaFunc {
	if fp == nil || p.Quotas == nil || p.Mounts == nil {
		return nil
	}
	usage := storageUsage(p, fp)
	return func(username string) (used, total int64) {
		used, err := usage(username)
		if err != nil {
			p.Log.Error("storage usage", "user", username, "err", err)
		}
		u, err := p.Users.GetByUsername(username)
		if err != nil {
			p.Log.Error("quota: look up user", "user", username, "err", err)
			return used, -1
		}
		m, err := p.Mounts.GetUserRoot(u.ID)
		if errors.Is(err, mount.ErrNotFound) {
			// No root mount (no default volume when the user was made):
			// no volume to fill, so no quota, as quota.Checker treats it.
			return used, -1
		}
		if err != nil {
			p.Log.Error("quota: root mount", "user", username, "err", err)
			return used, -1
		}
		var groupIDs []string
		for g, gErr := range p.Groups.ListGroupsForUser(u.ID) {
			if gErr != nil {
				p.Log.Error("quota: list groups", "user", username, "err", gErr)
				return used, -1
			}
			groupIDs = append(groupIDs, g.ID)
		}
		total, err = p.Quotas.EffectiveQuota(u.ID, m.VolumeID, groupIDs)
		if err != nil {
			p.Log.Error("effective quota", "user", username, "err", err)
			return used, -1
		}
		return used, total
	}
}

// storageUsage is what a user stores: their files, and what enabled plugins
// keep for them elsewhere (plugin.StorageUser).
func storageUsage(p *services.Providers, fp *filesPlugin.Plugin) quota.UsageFunc {
	return func(username string) (int64, error) {
		total, err := fp.Usage(username)
		if err != nil {
			return 0, err
		}
		for plug := range p.Plugins.Plugins() {
			su, ok := plug.(plugin.StorageUser)
			if !ok || !p.Plugins.IsEnabled(plug.ID()) {
				continue
			}
			n, err := su.StorageUsage(username)
			if err != nil {
				return 0, fmt.Errorf("%s storage usage: %w", plug.ID(), err)
			}
			total += n
		}
		return total, nil
	}
}

// buildQuotaChecker creates a QuotaChecker wired to the services layer.
func buildQuotaChecker(p *services.Providers, fp *filesPlugin.Plugin) *quota.Checker {
	if fp == nil || p.Quotas == nil || p.Mounts == nil {
		return nil
	}
	return &quota.Checker{
		Quotas: p.Quotas,
		Usage:  storageUsage(p, fp),
		RootVolume: func(username string) (string, error) {
			u, err := p.Users.GetByUsername(username)
			if err != nil {
				return "", err
			}
			m, err := p.Mounts.GetUserRoot(u.ID)
			if err != nil {
				return "", err
			}
			return m.VolumeID, nil
		},
		GroupIDs: func(userID string) ([]string, error) {
			var ids []string
			for g, err := range p.Groups.ListGroupsForUser(userID) {
				if err != nil {
					return nil, err
				}
				ids = append(ids, g.ID)
			}
			return ids, nil
		},
		UserIDFromName: func(username string) (string, error) {
			u, err := p.Users.GetByUsername(username)
			if err != nil {
				return "", err
			}
			return u.ID, nil
		},
	}
}
