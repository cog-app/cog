package dav

import (
	"errors"
	"fmt"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
)

// Backend supplies the resources of some component types inside collections
// owned by the DAV core. Each plugin that holds DAV data implements one.
//
// Every method takes the owner's username, which names the plugin database,
// and the collection. Writes bump the backend's per-collection counter in the
// same transaction (davsql), which is what makes sync and ETags correct.
type Backend interface {
	// Key names the backend in composite sync tokens: a short, stable,
	// lowercase string ("e", "t", "c").
	Key() string
	// Components lists the component types this backend stores.
	Components() []Component

	// List returns every resource in the collection. Data is filled only
	// when withData is set.
	List(owner string, c *Collection, withData bool) ([]Resource, error)
	// Get returns the resources among uris that exist, in no particular
	// order. Missing ones are simply absent.
	Get(owner string, c *Collection, uris []string, withData bool) ([]Resource, error)
	// FindUID returns the URI of the resource with this UID, or "" when there
	// is none.
	FindUID(owner string, c *Collection, uid string) (string, error)
	// Put creates or replaces the resource at req.URI after checking
	// req.Conditions in the same transaction. It returns ErrPrecondition when
	// they fail, and a *UIDConflict when another URI holds req.UID.
	Put(owner string, c *Collection, req *PutRequest) (*PutResult, error)
	// Delete removes a resource and leaves a tombstone, after checking the
	// conditions. It returns ErrNotFound when the resource does not exist.
	Delete(owner string, c *Collection, uri string, cond davsql.Conditions) error
	// Changes returns the resources written and the URIs deleted after
	// version since, with the state they were read at, in one snapshot.
	// With since == 0 it returns every resource and no deletions.
	Changes(owner string, c *Collection, since int64, withData bool) (*Changes, error)
	// States returns the sync state of each collection, all owned by owner.
	// Collections never written to are absent.
	States(owner string, ids []CollectionID) (map[CollectionID]davsql.State, error)
	// Prune drops the collection's tombstones deleted before cutoff.
	Prune(owner string, c *Collection, cutoff time.Time) error
	// Purge removes every row of a deleted collection.
	Purge(owner string, id CollectionID) error
	// Usage returns the bytes of every resource owner holds in this
	// backend, as their text would be served: what counts toward quota.
	Usage(owner string) (int64, error)
}

// Resource is one calendar object or vCard.
type Resource struct {
	URI       string
	UID       string
	Component Component
	Version   int64
	// Created is the version that created the resource; 0 when unknown.
	Created int64
	Size    int64
	Data    []byte // only when asked for
}

// ETag returns the resource's entity tag in a collection, quotes included.
func (r *Resource) ETag(c CollectionID) string { return ETag(c, r.Version) }

// PutRequest is a validated resource the core asks a backend to store.
type PutRequest struct {
	URI        string
	UID        string
	Component  Component
	Data       []byte
	Object     *contentline.Component // the parsed VCALENDAR or VCARD
	Conditions davsql.Conditions
	// Attachments are managed attachments to store with the resource,
	// which names them (AttachmentStore).
	Attachments []NewAttachment
}

// PutResult reports a stored resource.
type PutResult struct {
	Version int64
	Created bool
	// Data is the stored representation. When it differs from the request
	// body, the PUT response carries no ETag and clients fetch it again.
	Data []byte
}

// Changes is a backend's answer to a sync-collection request.
type Changes struct {
	Changed []Resource
	Deleted []string
	// DeletedAt holds the version each of Deleted was deleted at, when the
	// backend knows it.
	DeletedAt []int64
	State     davsql.State
}

// UIDConflict reports that another resource in the collection holds the UID.
type UIDConflict struct{ URI string }

func (e *UIDConflict) Error() string { return "dav: UID already used by " + e.URI }

// IsUIDConflict reports whether err is a *UIDConflict and returns it.
func IsUIDConflict(err error) (*UIDConflict, bool) {
	var c *UIDConflict
	ok := errors.As(err, &c)
	return c, ok
}

// BatchWriter is implemented by backends that store many resources of one
// collection in one transaction, for collections Cog fills itself: a
// subscription's feed, the birthday calendar. Puts are unconditional and
// replace what is at their URI; deleting a URI that holds nothing is not an
// error. Without it the core writes them one at a time.
type BatchWriter interface {
	WriteBatch(owner string, c *Collection, puts []*PutRequest, deletes []string) error
}

// writeBatch stores puts and removes deletes in c through b.
func writeBatch(b Backend, owner string, c *Collection, puts []*PutRequest, deletes []string) error {
	if len(puts) == 0 && len(deletes) == 0 {
		return nil
	}
	if bw, ok := b.(BatchWriter); ok {
		return bw.WriteBatch(owner, c, puts, deletes)
	}
	for _, p := range puts {
		if _, err := b.Put(owner, c, p); err != nil {
			return fmt.Errorf("store %s: %w", p.URI, err)
		}
	}
	for _, uri := range deletes {
		if err := b.Delete(owner, c, uri, davsql.Conditions{}); err != nil && !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("delete %s: %w", uri, err)
		}
	}
	return nil
}

// TimeRangeQuerier is implemented by backends that can narrow a
// calendar-query time-range filter in SQL. Others return every resource of
// the component, a superset the client filters.
type TimeRangeQuerier interface {
	InRange(owner string, c *Collection, comp Component, start, end time.Time, withData bool) ([]Resource, error)
}

// CardQuerier is implemented by backends that answer an addressbook-query
// themselves: they return the cards matching f, at most f.Limit when it is
// set, and whether more matched. Others have every card listed and matched
// by the core.
type CardQuerier interface {
	QueryCards(owner string, c *Collection, f *CardFilter, withData bool) (list []Resource, truncated bool, err error)
}

// BusyRedactor is implemented by backends whose resources a free/busy-only
// share may see in reduced form. Redact replaces r.Data with a version that
// shows only when the owner is busy.
type BusyRedactor interface {
	Redact(c *Collection, r *Resource) error
}

// Provider is implemented by plugins that hold DAV resources. The host hands
// it the DAV server before the plugin registers, and mounts its backends.
type Provider interface {
	SetDAV(*Server)
	DAVBackends() []Backend
}
