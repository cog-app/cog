package web

import (
	"errors"
	"net/http"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin"
)

// davUsers adapts *user.Store to dav.Users.
type davUsers struct{ s *user.Store }

func davUser(u *user.User, err error) (*dav.User, error) {
	if errors.Is(err, user.ErrNotFound) {
		return nil, dav.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &dav.User{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email}, nil
}

func (a davUsers) ByUsername(name string) (*dav.User, error) { return davUser(a.s.GetByUsername(name)) }
func (a davUsers) ByID(id string) (*dav.User, error)         { return davUser(a.s.GetByID(id)) }

// davPermissionLevels are the presets the share panel offers for collections.
var davPermissionLevels = []plugin.PermissionLevel{
	{Label: "Read-only", Permissions: plugin.PermRead},
	{Label: "Read/Write", Permissions: plugin.PermRead | plugin.PermWrite},
	{Label: "Free/Busy only", Permissions: plugin.PermFreeBusy},
	{Label: "Appointment Scheduling", Permissions: plugin.PermRead | plugin.PermFreeBusy | plugin.PermSchedule},
}

// mountDAV finishes wiring the DAV core and mounts it under /remote.php/dav/.
// The host owns the root, the principals and the homes; plugins supply
// resources through dav.Provider, and a disabled plugin's backends drop out
// per request.
func mountDAV(mux *http.ServeMux, p *services.Providers, shares plugin.ShareStore) {
	type provided struct {
		pluginID string
		backends []dav.Backend
	}
	var all []provided
	for plug := range p.Plugins.Plugins() {
		if dp, ok := plug.(dav.Provider); ok {
			all = append(all, provided{plug.ID(), dp.DAVBackends()})
		}
	}
	srv := p.DAV
	srv.Users = davUsers{p.Users}
	srv.Shares = shares
	srv.Backends = func() []dav.Backend {
		var out []dav.Backend
		for _, pr := range all {
			if p.Plugins.IsEnabled(pr.pluginID) {
				out = append(out, pr.backends...)
			}
		}
		return out
	}
	srv.Retention = func() time.Duration {
		days := settings.DefaultDAVTombstoneDays
		if p.Settings != nil {
			days = p.Settings.GetInt(settings.KeyDAVTombstoneDays, days)
		}
		return time.Duration(days) * 24 * time.Hour
	}
	if len(all) == 0 {
		return
	}
	h := auth.RequireAuth(srv)
	mux.Handle("PROPFIND /remote.php/dav/{$}", h)
	mux.Handle("PROPFIND /remote.php/dav", h)
	mux.Handle("OPTIONS /remote.php/dav/{$}", h)
	mux.Handle("/remote.php/dav/principals/", h)
	mux.Handle("/remote.php/dav/calendars/", h)
	mux.Handle("/remote.php/dav/addressbooks/", h)
}
