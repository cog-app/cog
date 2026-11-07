package web

import (
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/pluginsvc"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin"
)

// davUsers adapts *user.Store to dav.Users.
type davUsers struct {
	s       *user.Store
	visible func(viewer *user.User, targetID string) (bool, error)
}

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

func (a davUsers) EmailVisible(viewerID, targetID string) (bool, error) {
	viewer, err := a.s.GetByID(viewerID)
	if err != nil {
		return false, err
	}
	return a.visible(viewer, targetID)
}

func (a davUsers) ByEmails(emails []string) (map[string]*dav.User, error) {
	found, err := a.s.GetByEmails(emails)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*dav.User, len(found))
	for k, u := range found {
		out[k] = &dav.User{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email}
	}
	return out, nil
}

// davShares adapts *share.Store to dav.Shares. Shares made over DAV are
// indexed for search like those made in the share panel.
type davShares struct {
	*pluginsvc.ShareStoreAdapter
	p *services.Providers
}

func (a davShares) ListByOwner(ownerID string) iter.Seq2[plugin.ShareRecord, error] {
	return func(yield func(plugin.ShareRecord, error) bool) {
		for rec, err := range a.S.ListByOwner(ownerID) {
			if !yield(pluginsvc.Share(&rec), err) {
				return
			}
		}
	}
}

func (a davShares) Create(ownerUserID, resourceType, resourceID, subjectType, subjectID string, permissions uint32) (*plugin.ShareRecord, error) {
	rec, err := a.S.Create(ownerUserID, resourceType, resourceID, subjectType, subjectID, permissions)
	if err != nil {
		return nil, err
	}
	out := pluginsvc.Share(rec)
	return &out, nil
}

func (a davShares) UpdatePermissions(id string, permissions uint32) error {
	return a.S.Update(id, &permissions, nil)
}

func (a davShares) Delete(id string) error {
	return a.S.Delete(id)
}

// davLinks adapts *share.Store to dav.Links: a feed's secret is a public
// link share of the collection.
type davLinks struct{ s *share.Store }

func davLink(rec *share.Record) (dav.Link, bool) {
	id, err := strconv.ParseInt(rec.ResourceID, 10, 64)
	if err != nil || rec.ResourceType != dav.ShareType || rec.SubjectType != "PUBLIC" || rec.Token == nil {
		return dav.Link{}, false
	}
	return dav.Link{ID: rec.ID, OwnerID: rec.OwnerUserID, Collection: dav.CollectionID(id), Token: *rec.Token, Permissions: rec.Permissions}, true
}

func (a davLinks) CreateLink(ownerID string, id dav.CollectionID, permissions uint32) (*dav.Link, error) {
	rec, err := a.s.CreatePublicLink(ownerID, dav.ShareType, id.String(), permissions, nil)
	if err != nil {
		return nil, err
	}
	l, ok := davLink(rec)
	if !ok {
		return nil, fmt.Errorf("create link for collection %d: share %s is not a feed link", id, rec.ID)
	}
	return &l, nil
}

// LinkByToken finds a feed's link. A link with a password is not a feed:
// calendar clients subscribing to a URL have no way to give one.
func (a davLinks) LinkByToken(token string) (*dav.Link, error) {
	rec, err := a.s.GetByToken(token)
	if errors.Is(err, share.ErrNotFound) {
		return nil, dav.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	l, ok := davLink(rec)
	if !ok || (rec.PasswordHash != nil && *rec.PasswordHash != "") {
		return nil, dav.ErrNotFound
	}
	return &l, nil
}

func (a davLinks) LinksTo(id dav.CollectionID) ([]dav.Link, error) {
	var out []dav.Link
	for rec, err := range a.s.ListByResource(dav.ShareType, id.String()) {
		if err != nil {
			return nil, err
		}
		if l, ok := davLink(&rec); ok && (rec.PasswordHash == nil || *rec.PasswordHash == "") {
			out = append(out, l)
		}
	}
	return out, nil
}

func (a davLinks) LinksBy(ownerID string) ([]dav.Link, error) {
	var out []dav.Link
	for rec, err := range a.s.ListByOwner(ownerID) {
		if err != nil {
			return nil, err
		}
		if l, ok := davLink(&rec); ok && (rec.PasswordHash == nil || *rec.PasswordHash == "") {
			out = append(out, l)
		}
	}
	return out, nil
}

func (a davLinks) DeleteLink(id string) error {
	err := a.s.Delete(id)
	if errors.Is(err, share.ErrNotFound) {
		return dav.ErrNotFound
	}
	return err
}

// davPermissionLevels are the presets the share panel offers for collections.
var davPermissionLevels = []plugin.PermissionLevel{
	{Label: "Read-only", Permissions: plugin.PermRead},
	{Label: "Read/Write", Permissions: plugin.PermRead | plugin.PermWrite},
	{Label: "Free/Busy only", Permissions: plugin.PermFreeBusy},
	{Label: "Delegate (read/write, schedules as owner)", Permissions: plugin.PermRead | plugin.PermWrite | plugin.PermFreeBusy | plugin.PermSchedule},
}

// mountDAV finishes wiring the DAV core and mounts it under /remote.php/dav/.
// The host owns the root, the principals and the homes; plugins supply
// resources through dav.Provider, and a disabled plugin's backends drop out
// per request.
func mountDAV(mux *http.ServeMux, p *services.Providers, shares dav.Shares) {
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
	srv.Users = davUsers{p.Users, p.EmailVisible}
	srv.Shares = shares
	srv.BaseURL = p.Config.BaseURL
	srv.Links = davLinks{p.Shares}
	srv.Backends = func() []dav.Backend {
		var out []dav.Backend
		for _, pr := range all {
			if p.Plugins.IsEnabled(pr.pluginID) {
				out = append(out, pr.backends...)
			}
		}
		return out
	}
	srv.Mail = func() plugin.EmailSender {
		if p.EmailSender() == nil {
			return nil
		}
		return &pluginsvc.EmailSenderAdapter{SenderFunc: p.EmailSender}
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
	mux.Handle("/remote.php/dav/push/", h)
	// Attachments check access themselves: an external attendee has a token
	// rather than an account.
	mux.Handle("/remote.php/dav/attachments/", srv)
	// A feed's URL is its secret: subscribers have no account.
	mux.Handle("/remote.php/dav/feeds/", srv)
}
