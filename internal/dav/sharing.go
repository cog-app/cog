package dav

import (
	"errors"
	"iter"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav/itip"
	"git.duckfam.us/jj-software/cog/plugin"
)

// CalendarServer sharing (caldav-sharing.txt in Apple's CalendarServer):
// the owner of a collection sees who it is shared with in CS:invite and
// changes that by POSTing CS:share; a sharee sees the owner's collection
// through CS:shared-url. Both read and write the same dav:collection share
// rows as the web share panel, so a share made either way is the other's.
//
// A share takes effect at once, as one made in the web UI does: Cog has no
// pending invitations, so CS:invite reports every sharee as accepted, and a
// CS:invite-reply is accepted for clients that send one. Declining removes
// the sharee's own share. DESIGN/dav-storage.md section 7.

// Shares is the share store as the DAV core uses it.
type Shares interface {
	ListBySubject(subjectID string) iter.Seq2[plugin.ShareRecord, error]
	ListByOwner(ownerID string) iter.Seq2[plugin.ShareRecord, error]
	Create(ownerUserID, resourceType, resourceID, subjectType, subjectID string, permissions uint32) (*plugin.ShareRecord, error)
	UpdatePermissions(id string, permissions uint32) error
	Delete(id string) error
	// Decline stops a share reaching userID: deleting one with them alone,
	// and recording a group or everyone share as declined by them.
	Decline(id, userID string) error
}

// subjectUser is the subject type of a share with one user. Only these are
// listed in CS:invite: the format has no way to name a group or a link.
const subjectUser = "USER"

// csAccess maps share permissions to CalendarServer's access levels: read
// or read-write. A free/busy-only share is neither and is not listed.
func csAccess(perms uint32) (string, bool) {
	switch {
	case perms&plugin.PermWrite != 0:
		return "read-write", true
	case perms&plugin.PermRead != 0:
		return "read", true
	}
	return "", false
}

// withAccess returns perms changed to a CalendarServer access level. The
// bits that level does not speak of stay: FreeBusy always, and Schedule
// while the share can write, which scheduling for the owner needs.
func withAccess(perms uint32, readWrite bool) uint32 {
	perms &^= plugin.PermRead | plugin.PermWrite
	perms |= plugin.PermRead
	if readWrite {
		perms |= plugin.PermWrite
	} else {
		perms &^= plugin.PermSchedule
	}
	return perms
}

// sharee is one user a collection is shared with, as CS:invite lists them.
type sharee struct {
	shareID string
	user    *User
	perms   uint32
}

// ownShares returns the users each of the current user's collections is
// shared with, loaded once per request.
func (q *request) ownShares() (map[CollectionID][]sharee, error) {
	if q.owned != nil {
		return q.owned, nil
	}
	out := map[CollectionID][]sharee{}
	if q.s.Shares == nil {
		q.owned = out
		return out, nil
	}
	users := map[string]*User{}
	for rec, err := range q.s.Shares.ListByOwner(q.user.ID) {
		if err != nil {
			return nil, err
		}
		id, ok := collectionShare(rec)
		if !ok || rec.SubjectType != subjectUser {
			continue
		}
		u, ok := users[rec.SubjectID]
		if !ok {
			var err error
			u, err = q.s.Users.ByID(rec.SubjectID)
			if errors.Is(err, ErrNotFound) {
				continue // a share with a deleted user
			}
			if err != nil {
				return nil, err
			}
			users[rec.SubjectID] = u
		}
		out[id] = append(out[id], sharee{shareID: rec.ID, user: u, perms: rec.Permissions})
	}
	q.owned = out
	return out, nil
}

// calendarUserHref is how CS:invite names a user: their email as a mailto
// address, or their principal.
func calendarUserHref(u *User) string { return CalendarAddress(u) }

func displayName(u *User) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// sharingProps returns the CalendarServer sharing properties of c: for its
// owner, CS:invite and whether it is shared; for a sharee, the owner and
// CS:shared-url.
func (q *request) sharingProps(c *Collection, a access, ownerName string) (shared bool, props []prop, err error) {
	href := collectionHref(c, ownerName)
	if a.owner {
		all, err := q.ownShares()
		if err != nil {
			return false, nil, err
		}
		var users strings.Builder
		for _, sh := range all[c.ID] {
			level, ok := csAccess(sh.perms)
			if !ok {
				continue
			}
			users.WriteString(el(cs("user"), hrefEl(calendarUserHref(sh.user))+
				el(cs("common-name"), escape(displayName(sh.user)))+
				el(cs("invite-accepted"), "")+
				el(cs("access"), el(cs(level), ""))))
			shared = true
		}
		return shared, []prop{
			{cs("invite"), users.String()},
			{cs("allowed-sharing-modes"), el(cs("can-be-shared"), "")},
		}, nil
	}
	owner, err := q.s.Users.ByUsername(ownerName)
	if err != nil {
		return false, nil, err
	}
	invite := el(cs("organizer"), hrefEl(calendarUserHref(owner))+el(cs("common-name"), escape(displayName(owner))))
	if level, ok := csAccess(a.perms); ok {
		invite += el(cs("user"), hrefEl(calendarUserHref(q.user))+
			el(cs("common-name"), escape(displayName(q.user)))+
			el(cs("invite-accepted"), "")+
			el(cs("access"), el(cs(level), "")))
	}
	return true, []prop{
		{cs("invite"), invite},
		{cs("shared-url"), hrefEl(href)},
	}, nil
}

// --- CS:share ---

// shareRequest is one CS:set or CS:remove of a CS:share body.
type shareRequest struct {
	href      string
	remove    bool
	readWrite bool
}

// sharePost answers a CS:share POST on a collection its owner shares.
func (q *request) sharePost(c *Collection, owner *User, n *node) {
	var reqs []shareRequest
	for _, op := range n.children() {
		switch {
		case op.is(cs("set")):
			rw := op.child(cs("read-write")) != nil
			if !rw && op.child(cs("read")) == nil {
				http.Error(q.w, "CS:set needs CS:read or CS:read-write", http.StatusBadRequest)
				return
			}
			reqs = append(reqs, shareRequest{href: op.child(dav("href")).text(), readWrite: rw})
		case op.is(cs("remove")):
			reqs = append(reqs, shareRequest{href: op.child(dav("href")).text(), remove: true})
		}
	}
	addrs := make([]string, len(reqs))
	for i, r := range reqs {
		addrs[i] = itip.Normalize(r.href)
	}
	users, err := q.s.resolve(addrs)
	if err != nil {
		q.fail(err)
		return
	}
	// Check every sharee before changing anything, so a request either
	// applies whole or not at all.
	for i, r := range reqs {
		u := users[addrs[i]]
		if u == nil || u.ID == owner.ID {
			http.Error(q.w, "not a user this calendar can be shared with: "+r.href, http.StatusForbidden)
			return
		}
	}
	all, err := q.ownShares()
	if err != nil {
		q.fail(err)
		return
	}
	existing := all[c.ID]
	for i, r := range reqs {
		u := users[addrs[i]]
		var mine []sharee
		for _, sh := range existing {
			if sh.user.ID == u.ID {
				mine = append(mine, sh)
			}
		}
		switch {
		case r.remove:
			for _, sh := range mine {
				if err := q.s.Shares.Delete(sh.shareID); err != nil {
					q.fail(err)
					return
				}
			}
		case len(mine) == 0:
			if _, err := q.s.Shares.Create(owner.ID, ShareType, c.ID.String(), subjectUser, u.ID, withAccess(0, r.readWrite)); err != nil {
				q.fail(err)
				return
			}
		default:
			// One share per user is what the web panel makes; if there are
			// more, the first carries the access and the rest go.
			if err := q.s.Shares.UpdatePermissions(mine[0].shareID, withAccess(mine[0].perms, r.readWrite)); err != nil {
				q.fail(err)
				return
			}
			for _, sh := range mine[1:] {
				if err := q.s.Shares.Delete(sh.shareID); err != nil {
					q.fail(err)
					return
				}
			}
		}
	}
	q.owned = nil
	q.w.WriteHeader(http.StatusOK)
}

// --- CS:invite-reply ---

// inviteReply answers a CS:invite-reply POSTed to the current user's
// calendar home. Shares take effect when they are made, so accepting only
// says where the collection is; declining stops it reaching the user, and
// only them when it was made to a group or to everyone.
func (q *request) inviteReply(n *node) {
	hostHref := n.child(cs("hosturl")).child(dav("href")).text()
	c, ownerName, ok, err := q.collectionAt(hostHref)
	if err != nil {
		q.fail(err)
		return
	}
	if !ok || c.OwnerID == q.user.ID {
		http.Error(q.w, "no shared collection at "+hostHref, http.StatusForbidden)
		return
	}
	var ids []string
	for rec, err := range q.s.Shares.ListBySubject(q.user.ID) {
		if err != nil {
			q.fail(err)
			return
		}
		// ListBySubject gives the user's own shares and those of groups
		// they are in or of everyone, which they may decline for themselves.
		if id, ok := collectionShare(rec); ok && id == c.ID && rec.OwnerUserID == c.OwnerID {
			ids = append(ids, rec.ID)
		}
	}
	if len(ids) == 0 {
		http.Error(q.w, "no share of "+hostHref+" to answer", http.StatusForbidden)
		return
	}
	if n.child(cs("invite-declined")) != nil {
		for _, id := range ids {
			if err := q.s.Shares.Decline(id, q.user.ID); err != nil {
				q.fail(err)
				return
			}
		}
		q.w.WriteHeader(http.StatusNoContent)
		return
	}
	q.w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	q.w.WriteHeader(http.StatusOK)
	q.w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<CS:shared-as` + rootDecls + `>` + hrefEl(collectionHref(c, ownerName)) + `</CS:shared-as>`))
}

// collectionAt finds the collection an href names, by its owner and name.
func (q *request) collectionAt(href string) (*Collection, string, bool, error) {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return nil, "", false, nil
	}
	p, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return nil, "", false, nil
	}
	rest, ok := strings.CutPrefix(p, Root)
	if !ok {
		return nil, "", false, nil
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 3 || !slices.Contains([]Home{HomeCalendars, HomeAddressBooks}, Home(parts[0])) {
		return nil, "", false, nil
	}
	owner, err := q.s.Users.ByUsername(parts[1])
	if errors.Is(err, ErrNotFound) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	c, err := q.s.Collections.Get(owner.ID, Home(parts[0]), parts[2])
	if errors.Is(err, ErrNotFound) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	return c, owner.Username, true, nil
}

// homePost answers a POST to the current user's home: CS:invite-reply,
// or a WebDAV-Push registration.
func (q *request) homePost(home Home) {
	n, err := readXML(q.r)
	switch {
	case err == nil && n.is(pushName("push-register")):
		q.pushRegister(n, homeResource(q.user.ID, home), false)
	case err == nil && n.is(cs("invite-reply")):
		q.inviteReply(n)
	default:
		http.Error(q.w, "expected CS:invite-reply or push-register", http.StatusBadRequest)
	}
}
