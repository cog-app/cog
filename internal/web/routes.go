package web

import (
	"io/fs"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/compat"
	"git.duckfam.us/jj-software/cog/internal/plugin/dashboard"
	"git.duckfam.us/jj-software/cog/internal/plugin/files"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/ui"
)

// New builds the full HTTP handler with all routes and middleware.
// Extra plugins from custom builds are registered after the protected core plugins.
func New(p *services.Providers, extraPlugins ...plugin.Plugin) http.Handler {
	authMW := &auth.Middleware{
		Users:        p.Users,
		Sessions:     p.Sessions,
		AppPasswords: p.AppPasswords,
	}

	loginFlow := compat.NewLoginFlowHandler(p.Users, p.AppPasswords, p.Sessions, p.Config.BaseURL)

	mux := http.NewServeMux()

	// --- Static assets ---
	staticSub, _ := fs.Sub(ui.StaticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))

	// --- Register plugins ---
	// Dashboard and files plugins embed Protected and are auto-detected.
	dashPlugin := &dashboard.Plugin{Registry: p.Plugins, Secret: p.CSRFSecret}
	p.Plugins.Add(dashPlugin)
	p.Plugins.Add(p.Files)

	// Extra plugins from custom builds.
	for _, ep := range extraPlugins {
		p.Plugins.Add(ep)
	}

	// --- Web UI handlers ---
	h := handler{p}
	loginH := &loginHandler{h}
	settingsH := &settingsHandler{h}
	adminH := &adminHandler{h}
	adminVolH := &adminVolumeHandler{h}
	volBrowserH := &volumeBrowserHandler{h}
	avatarH := &avatarHandler{h}
	groupH := &groupHandler{h}

	// Login/logout/register — unauthenticated
	mux.HandleFunc("GET /login", loginH.ShowLogin)
	mux.HandleFunc("POST /login", loginH.HandleLogin)
	mux.HandleFunc("POST /logout", loginH.HandleLogout)
	mux.HandleFunc("GET /register", loginH.ShowRegister)
	mux.HandleFunc("POST /register", loginH.HandleRegister)

	// Dashboard (root route from dashboard plugin)
	mux.Handle("GET /{$}", auth.RequireAuthWeb(dashPlugin.Handler()))

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
	mux.Handle("POST /settings/groups/{id}/mounts", csrfWeb(http.HandlerFunc(groupH.CreateGroupMount)))
	mux.Handle("DELETE /settings/groups/{id}/mounts/{mount_id}", csrfWeb(http.HandlerFunc(groupH.DeleteGroupMount)))
	mux.Handle("POST /settings/groups/{id}/volumes", csrfWeb(http.HandlerFunc(groupH.CreateGroupVolume)))

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
	mux.Handle("DELETE /admin/groups/{id}/members/{member_id}", adminMut(http.HandlerFunc(adminH.RemoveMember)))

	mux.Handle("GET /admin/volumes", adminAuth(http.HandlerFunc(adminVolH.ListVolumes)))
	mux.Handle("GET /admin/volumes/new", adminAuth(http.HandlerFunc(adminVolH.NewVolumeForm)))
	mux.Handle("POST /admin/volumes", adminMut(http.HandlerFunc(adminVolH.CreateVolume)))
	mux.Handle("GET /admin/volumes/{id}", adminAuth(http.HandlerFunc(adminVolH.EditVolumeForm)))
	mux.Handle("POST /admin/volumes/{id}", adminMut(http.HandlerFunc(adminVolH.UpdateVolume)))
	mux.Handle("DELETE /admin/volumes/{id}", adminMut(http.HandlerFunc(adminVolH.DeleteVolume)))

	mux.Handle("GET /admin/settings", adminAuth(http.HandlerFunc(adminH.ShowSettings)))
	mux.Handle("POST /admin/settings", adminMut(http.HandlerFunc(adminH.UpdateSettings)))

	mux.Handle("GET /admin/plugins", adminAuth(http.HandlerFunc(adminH.ListPlugins)))
	mux.Handle("POST /admin/plugins/{id}/toggle", adminMut(http.HandlerFunc(adminH.TogglePlugin)))

	// Volume browser
	mux.Handle("GET /volumes/{id}/browse", auth.RequireAuthWeb(http.HandlerFunc(volBrowserH.Browse)))
	mux.Handle("GET /volumes/{id}/download", auth.RequireAuthWeb(http.HandlerFunc(volBrowserH.Download)))
	mux.Handle("POST /volumes/{id}/upload", csrfWeb(http.HandlerFunc(volBrowserH.Upload)))
	mux.Handle("POST /volumes/{id}/mkdir", csrfWeb(http.HandlerFunc(volBrowserH.MkDir)))
	mux.Handle("DELETE /volumes/{id}/file", csrfWeb(http.HandlerFunc(volBrowserH.DeleteFile)))

	// Files plugin page — path is in the URL after /apps/files/
	filesH := &FilesHandler{p: p}
	mux.Handle("GET /apps/files/{path...}", auth.RequireAuthWeb(http.HandlerFunc(filesH.ServeGet)))
	mux.Handle("POST /apps/files/{path...}", csrfWeb(http.HandlerFunc(filesH.ServePost)))
	mux.Handle("DELETE /apps/files/{path...}", csrfWeb(http.HandlerFunc(filesH.ServeDelete)))

	// --- Status & OCS ---
	mux.HandleFunc("GET /status.php", compat.HandleStatus)
	mux.HandleFunc("GET /ocs/v1.php/cloud/capabilities", compat.HandleCapabilities)
	mux.HandleFunc("GET /ocs/v2.php/cloud/capabilities", compat.HandleCapabilities)
	mux.Handle("GET /ocs/v1.php/cloud/user", auth.RequireAuth(http.HandlerFunc(compat.HandleCurrentUser)))
	mux.Handle("GET /ocs/v2.php/cloud/user", auth.RequireAuth(http.HandlerFunc(compat.HandleCurrentUser)))

	// --- WebDAV file operations (NC clients) ---
	webdav := &compat.WebDAVHandler{Files: p.Files, Users: p.Users}
	mux.Handle("/remote.php/dav/files/", auth.RequireAuth(webdav))

	// Direct download API (NC Android app uses this instead of WebDAV GET)
	mux.Handle("POST /ocs/v2.php/apps/dav/api/v1/direct", auth.RequireAuth(webdav.HandleDirectDownload(p.Config.BaseURL)))
	mux.Handle("POST /ocs/v1.php/apps/dav/api/v1/direct", auth.RequireAuth(webdav.HandleDirectDownload(p.Config.BaseURL)))

	// Token-based direct file serving (no auth — token is the credential)
	mux.HandleFunc("GET /remote.php/dav/direct/", webdav.HandleDirectServe)

	// WebDAV stub for non-file DAV paths (presence check, CalDAV, etc.)
	mux.HandleFunc("/remote.php/dav", compat.HandleWebDAVStub)
	mux.HandleFunc("/remote.php/dav/", compat.HandleWebDAVStub)

	// Stub routes (avatar, notifications, connectivity, etc.)
	compat.RegisterStubs(mux)

	// --- Login Flow v2 ---
	mux.HandleFunc("POST /index.php/login/v2", loginFlow.HandleInit)
	mux.HandleFunc("POST /index.php/login/v2/poll", loginFlow.HandlePoll)
	mux.HandleFunc("GET /index.php/login/v2/grant", loginFlow.HandleGrant)
	mux.HandleFunc("POST /index.php/login/v2/grant", loginFlow.HandleGrant)

	// --- Files & Sharing API ---
	filesAPI := files.NewAPI(p.Files, p.Shares, p.Users)
	filesAPI.RegisterRoutes(mux)

	return authMW.Wrap(mux)
}
