package web

import (
	"io/fs"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/compat"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
	filesPlugin "git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/ui"
)

// New builds the full HTTP handler with all routes and middleware.
// All plugins (including first-party) are passed in via the plugins slice.
func New(p *services.Providers) http.Handler {
	authMW := &auth.Middleware{
		Users:        p.Users,
		Sessions:     p.Sessions,
		AppPasswords: p.AppPasswords,
	}

	loginFlow := compat.NewLoginFlowHandler(p.Users, p.AppPasswords, p.Sessions, p.Config.BaseURL)

	// Use the plugin registry's mux so plugin-registered routes share the same router.
	mux := p.Plugins.Mux()

	// --- Static assets ---
	staticSub, _ := fs.Sub(ui.StaticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))

	// --- Wire optional services for plugins ---
	p.Plugins.SetServices(buildRegistryServices(p.Shares, p.EmailSender, p.Users, p.Groups))

	// --- Web UI handlers ---
	h := handler{p}
	loginH := loginHandler{h}
	settingsH := settingsHandler{h}
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

	// Avatar
	mux.Handle("GET /avatar/{id}", http.HandlerFunc(avatarH.Serve))
	mux.Handle("GET /avatar/{id}/initials", http.HandlerFunc(avatarH.ServeInitials))

	// Settings
	csrfWeb := func(h http.Handler) http.Handler {
		return auth.RequireAuthWeb(auth.RequireCSRF(p.CSRFSecret, h))
	}

	mux.Handle("GET /settings/{$}", auth.RequireAuthWeb(http.HandlerFunc(settingsH.RedirectToProfile)))
	mux.Handle("GET /settings/profile", auth.RequireAuthWeb(http.HandlerFunc(settingsH.ShowProfile)))
	mux.Handle("GET /settings/security", auth.RequireAuthWeb(http.HandlerFunc(settingsH.ShowSecurity)))
	mux.Handle("GET /settings/storage", auth.RequireAuthWeb(http.HandlerFunc(settingsH.ShowStorage)))
	mux.Handle("POST /settings/profile", csrfWeb(http.HandlerFunc(settingsH.UpdateProfile)))
	mux.Handle("POST /settings/password", csrfWeb(http.HandlerFunc(settingsH.UpdatePassword)))
	mux.Handle("POST /settings/mounts", csrfWeb(http.HandlerFunc(settingsH.CreateMount)))
	mux.Handle("DELETE /settings/mounts/{id}", csrfWeb(http.HandlerFunc(settingsH.DeleteMount)))
	mux.Handle("POST /settings/volumes", csrfWeb(http.HandlerFunc(settingsH.CreateVolume)))
	mux.Handle("DELETE /settings/volumes/{id}", csrfWeb(http.HandlerFunc(settingsH.DeleteVolume)))
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
		return auth.RequireAuthWeb(auth.RequireAdmin(auth.RequireCSRF(p.CSRFSecret, h)))
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

	mux.Handle("GET /admin/settings", adminAuth(http.HandlerFunc(adminH.ShowSettings)))
	mux.Handle("POST /admin/settings", adminMut(http.HandlerFunc(adminH.UpdateSettings)))
	mux.Handle("POST /admin/settings/test-smtp", adminMut(http.HandlerFunc(adminH.TestSMTP)))

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
	mux.Handle("GET /ocs/v1.php/cloud/user", auth.RequireAuth(http.HandlerFunc(compat.HandleCurrentUser)))
	mux.Handle("GET /ocs/v2.php/cloud/user", auth.RequireAuth(http.HandlerFunc(compat.HandleCurrentUser)))
	mux.Handle("GET /ocs/v1.php/cloud/users/{id}", auth.RequireAuth(compat.HandleUserByID(p.Users)))
	mux.Handle("GET /ocs/v2.php/cloud/users/{id}", auth.RequireAuth(compat.HandleUserByID(p.Users)))
	// Provisioning API path variant (NC Android app uses this path for user info)
	mux.Handle("GET /ocs/v1.php/apps/provisioning_api/api/v2/users/{id}", auth.RequireAuth(compat.HandleUserByID(p.Users)))
	mux.Handle("GET /ocs/v2.php/apps/provisioning_api/api/v2/users/{id}", auth.RequireAuth(compat.HandleUserByID(p.Users)))

	// --- WebDAV file operations (NC clients) ---
	// Look up the files plugin from the registry for compat handlers.
	var fp *filesPlugin.Plugin
	if plug, ok := p.Plugins.Plugin("files"); ok {
		fp, _ = plug.(*filesPlugin.Plugin)
	}

	if fp != nil {
		webdav := &compat.WebDAVHandler{Log: p.Log.With("component", "webdav"), Files: fp}
		mux.Handle("/remote.php/dav/files/", auth.RequireAuth(webdav))

		// Chunked uploads (NC v2 protocol)
		chunked := &compat.ChunkedUploadHandler{
			Log:     p.Log.With("component", "chunked"),
			Files:   fp,
			TempDir: p.Config.DataDir,
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
			Log:    p.Log.With("component", "sharing"),
			Files:  fp,
			Shares: p.Shares,
			Users:  p.Users,
			Groups: p.Groups,
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

	// CalDAV/CardDAV discovery: PROPFIND on /remote.php/dav/ returns current-user-principal.
	mux.Handle("PROPFIND /remote.php/dav/{$}", auth.RequireAuth(http.HandlerFunc(calendar.HandleDAVRoot)))
	mux.Handle("PROPFIND /remote.php/dav", auth.RequireAuth(http.HandlerFunc(calendar.HandleDAVRoot)))

	// WebDAV stub for non-file DAV paths (presence check, HEAD/OPTIONS/GET)
	mux.HandleFunc("/remote.php/dav", compat.HandleWebDAVStub)
	mux.HandleFunc("/remote.php/dav/", compat.HandleWebDAVStub)

	// NC-compatible OCS routes (avatar, notifications, user status, etc.)
	ncCompat := &compat.NCCompat{Users: p.Users, Files: fp, DataDir: p.Config.DataDir}
	ncCompat.RegisterRoutes(mux)

	// --- Login Flow v2 ---
	mux.HandleFunc("POST /index.php/login/v2", loginFlow.HandleInit)
	mux.HandleFunc("POST /index.php/login/v2/poll", loginFlow.HandlePoll)
	mux.HandleFunc("GET /index.php/login/v2/grant", loginFlow.HandleGrant)
	mux.HandleFunc("POST /index.php/login/v2/grant", loginFlow.HandleGrant)

	// --- Share UI (generic, HTMX) ---
	shareH := shareHandler{h}
	mux.Handle("GET /api/shares/panel", auth.RequireAuth(http.HandlerFunc(shareH.SharePanel)))
	mux.Handle("POST /api/shares/create", csrfWeb(http.HandlerFunc(shareH.CreateShareForm)))
	mux.Handle("POST /api/shares/public-link-form", csrfWeb(http.HandlerFunc(shareH.CreatePublicLinkForm)))
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

	return authMW.Wrap(mux)
}
