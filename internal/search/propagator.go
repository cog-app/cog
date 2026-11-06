package search

import (
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// ShareRecord is a minimal representation of a share for propagation purposes,
// avoiding import cycles with the share package.
type ShareRecord struct {
	ID           string
	OwnerUserID  string
	ResourceType string
	ResourceID   string
	SubjectType  string
	SubjectID    string
}

// Propagator indexes shared resources into recipients' search indices.
// It runs asynchronously, processing events from a bounded channel.
//
// Shutdown uses quit + stopped flag rather than close(ch) so concurrent
// producers (OnShareCreate/Delete) cannot panic by sending to a closed channel.
type Propagator struct {
	log     *slog.Logger
	mgr     *Manager
	ch      chan propagateEvent
	quit    chan struct{}
	done    chan struct{}
	started atomic.Bool
	stopped atomic.Bool

	// Function callbacks to avoid import cycles.
	// ResolveRecipientUsernames expands a share into target usernames.
	ResolveRecipientUsernames func(rec *ShareRecord) []string
	// LookupUsername maps a user ID to a username.
	LookupUsername func(userID string) (string, error)
	// ResolveSourceName maps a share's resource type ("notes:note") to the
	// name the indexer files its documents under ("notes:notes"). The two are
	// not the same string: a share names a resource type, while a document is
	// named after the table the rows live in. Without this the propagator
	// looks up a document ID nothing ever wrote.
	ResolveSourceName func(resourceType string) (string, bool)
	// LookupShare returns a share as it is now, or nil once it is gone.
	LookupShare func(id string) (*ShareRecord, error)
	// ResolveResourceTypes is ResolveSourceName reversed: the resource
	// types whose documents a source ("notes:notes") indexes. More than one
	// type can live in one table, as files and directories do.
	ResolveResourceTypes func(source string) []string
	// SharesOf returns the shares the user named owner made of the given
	// resources of one type.
	SharesOf func(owner, resourceType string, resourceIDs []string) ([]*ShareRecord, error)
	// SharesFor returns the shares that reach a user now.
	SharesFor func(username string) ([]*ShareRecord, error)
}

type propagateEvent struct {
	Kind    string // "create", "delete", "owner", "recipient"
	Record  *ShareRecord
	attempt int

	// For "owner", the user whose index changed and how; for
	// "recipient", the user whose index opened.
	Username string
	Docs     []Document
	Deleted  []string
}

// RetryDelay is how long propagation waits before looking again for a
// document that was not in the owner's index yet. Exported for tests.
var RetryDelay = 250 * time.Millisecond

// maxPropagateAttempts bounds those retries.
//
// This is defensive rather than demonstrated: indexing is asynchronous, so a
// share created moments after its resource can in principle be processed
// before the document exists, and nothing revisits a share afterwards — the
// recipient would simply never get it. Attempts to reproduce that ordering in
// tests did not manage it, so treat this as insurance against a miss under
// load rather than a fix for an observed failure.
const maxPropagateAttempts = 8

// NewPropagator creates a propagator wired to the search manager.
func NewPropagator(log *slog.Logger, mgr *Manager) *Propagator {
	return &Propagator{
		log:  log.With("component", "search-propagator"),
		mgr:  mgr,
		ch:   make(chan propagateEvent, 1024),
		quit: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// Start begins the background worker goroutine.
func (p *Propagator) Start() {
	p.started.Store(true)
	go p.run()
}

// Stop signals the worker to drain and exit.
// It is safe to call even if Start was never called.
func (p *Propagator) Stop() {
	if !p.started.Load() {
		return
	}
	if p.stopped.Swap(true) {
		return
	}
	close(p.quit)
	<-p.done
}

// OnShareChange follows a share that was created, changed, declined or
// deleted: while it exists its resource is indexed for its recipients, and
// once it is gone it is removed from theirs. rec is the share as it was, for
// finding the recipients of one that no longer exists.
func (p *Propagator) OnShareChange(rec *ShareRecord) {
	if rec == nil || p.LookupShare == nil {
		return
	}
	now, err := p.LookupShare(rec.ID)
	if err != nil {
		p.log.Error("propagate: look up share", "share", rec.ID, "err", err)
		return
	}
	if now == nil {
		p.OnShareDelete(rec)
		return
	}
	p.OnShareCreate(now)
}

// OnOwnerIndexed follows a change to a user's own index into the copies of
// what they shared: an edited document replaces its copies, a deleted one
// takes them away.
func (p *Propagator) OnOwnerIndexed(owner string, docs []Document, deleted []string) {
	p.send(propagateEvent{Kind: "owner", Username: owner, Docs: docs, Deleted: deleted})
}

// OnRecipientChanged adds to a user's index the copies it lacks of what is
// shared with them: when their index opens, for shares that reached them
// while it was closed, and when they join a group, for what was shared with
// it before.
func (p *Propagator) OnRecipientChanged(username string) {
	p.send(propagateEvent{Kind: "recipient", Username: username})
}

func (p *Propagator) send(ev propagateEvent) {
	if p.stopped.Load() {
		return
	}
	select {
	case p.ch <- ev:
	case <-p.quit:
	default:
		p.log.Warn("propagator channel full, dropping event", "kind", ev.Kind, "user", ev.Username)
	}
}

// OnShareCreate queues a share-create event for propagation.
func (p *Propagator) OnShareCreate(rec *ShareRecord) {
	if rec == nil || p.stopped.Load() {
		return
	}
	select {
	case p.ch <- propagateEvent{Kind: "create", Record: rec}:
	case <-p.quit:
	default:
		p.log.Warn("propagator channel full, dropping create event")
	}
}

// OnShareDelete queues a share-delete event for propagation.
func (p *Propagator) OnShareDelete(rec *ShareRecord) {
	if rec == nil || p.stopped.Load() {
		return
	}
	select {
	case p.ch <- propagateEvent{Kind: "delete", Record: rec}:
	case <-p.quit:
	default:
		p.log.Warn("propagator channel full, dropping delete event")
	}
}

func (p *Propagator) run() {
	defer close(p.done)
	for {
		select {
		case ev := <-p.ch:
			p.handle(ev)
		case <-p.quit:
			// Drain remaining events then exit.
			for {
				select {
				case ev := <-p.ch:
					p.handle(ev)
				default:
					return
				}
			}
		}
	}
}

func (p *Propagator) handle(ev propagateEvent) {
	switch ev.Kind {
	case "create":
		p.handleCreate(ev)
	case "delete":
		p.handleDelete(ev.Record)
	case "owner":
		p.handleOwner(ev.Username, ev.Docs, ev.Deleted)
	case "recipient":
		p.handleRecipient(ev.Username)
	}
}

// ownedDoc is one of an owner's documents a share might name.
type ownedDoc struct {
	id     string // the resource's ID: the document's row
	fields map[string]any
}

// handleOwner refreshes, or removes, the copies of an owner's documents
// that are shared: one share query per source in the batch.
func (p *Propagator) handleOwner(owner string, docs []Document, deleted []string) {
	if p.ResolveResourceTypes == nil || p.SharesOf == nil || p.ResolveRecipientUsernames == nil {
		return
	}
	bySource := map[string][]ownedDoc{}
	split := func(docID string) (string, string, bool) {
		i := strings.LastIndexByte(docID, ':')
		if i <= 0 {
			return "", "", false
		}
		return docID[:i], docID[i+1:], true
	}
	for _, d := range docs {
		if source, id, ok := split(d.ID); ok {
			bySource[source] = append(bySource[source], ownedDoc{id: id, fields: d.Fields})
		}
	}
	for _, docID := range deleted {
		if source, id, ok := split(docID); ok {
			bySource[source] = append(bySource[source], ownedDoc{id: id})
		}
	}
	for source, owned := range bySource {
		byID := make(map[string]ownedDoc, len(owned))
		ids := make([]string, 0, len(owned))
		for _, o := range owned {
			byID[o.id] = o
			ids = append(ids, o.id)
		}
		for _, resourceType := range p.ResolveResourceTypes(source) {
			shares, err := p.SharesOf(owner, resourceType, ids)
			if err != nil {
				p.log.Error("propagate owner change: list shares", "owner", owner, "type", resourceType, "err", err)
				continue
			}
			for _, rec := range shares {
				o := byID[rec.ResourceID]
				docID := sharedDocID(owner, rec.ResourceType, rec.ResourceID)
				for _, username := range p.ResolveRecipientUsernames(rec) {
					if username == owner {
						continue
					}
					var err error
					if o.fields == nil {
						err = p.mgr.DeleteSharedDocument(username, docID)
					} else {
						err = p.mgr.IndexSharedDocument(username, Document{ID: docID, Fields: o.fields})
					}
					if err != nil {
						p.log.Error("propagate owner change", "user", username, "doc", docID, "err", err)
					}
				}
			}
		}
	}
}

// handleRecipient copies into a user's index what is shared with them and
// missing from it. Copies already there are kept fresh by handleOwner.
func (p *Propagator) handleRecipient(username string) {
	if p.SharesFor == nil || p.LookupUsername == nil {
		return
	}
	shares, err := p.SharesFor(username)
	if err != nil {
		p.log.Error("propagate to recipient: list shares", "user", username, "err", err)
		return
	}
	for _, rec := range shares {
		owner, err := p.LookupUsername(rec.OwnerUserID)
		if err != nil {
			p.log.Error("propagate to recipient: owner lookup", "share", rec.ID, "err", err)
			continue
		}
		if owner == username {
			continue
		}
		docID := sharedDocID(owner, rec.ResourceType, rec.ResourceID)
		has, err := p.mgr.hasSharedDocument(username, docID)
		if err != nil {
			p.log.Error("propagate to recipient: look up copy", "user", username, "doc", docID, "err", err)
			continue
		}
		if has {
			continue
		}
		doc, err := p.fetchOwnerDocument(owner, rec.ResourceType, rec.ResourceID)
		if err != nil {
			// Not indexed (or not indexable): nothing to copy yet. The
			// owner's index reports it when it is.
			continue
		}
		if err := p.mgr.IndexSharedDocument(username, Document{ID: docID, Fields: doc.Fields}); err != nil {
			p.log.Error("propagate to recipient: index", "user", username, "doc", docID, "err", err)
		}
	}
}

func (p *Propagator) handleCreate(ev propagateEvent) {
	rec := ev.Record
	if p.ResolveRecipientUsernames == nil || p.LookupUsername == nil {
		return
	}

	usernames := p.ResolveRecipientUsernames(rec)
	if len(usernames) == 0 {
		return
	}

	ownerUsername, err := p.LookupUsername(rec.OwnerUserID)
	if err != nil {
		p.log.Error("propagate create: owner lookup", "err", err)
		return
	}

	docID := sharedDocID(ownerUsername, rec.ResourceType, rec.ResourceID)

	// Try to fetch an existing document from the owner's index.
	ownerDoc, err := p.fetchOwnerDocument(ownerUsername, rec.ResourceType, rec.ResourceID)
	if err != nil {
		p.retryCreate(ev, err)
		return
	}

	for _, username := range usernames {
		if username == ownerUsername {
			continue
		}
		doc := Document{
			ID:     docID,
			Fields: ownerDoc.Fields,
		}
		if err := p.mgr.IndexSharedDocument(username, doc); err != nil {
			p.log.Error("propagate create: index", "user", username, "err", err)
		}
	}
}

// retryCreate looks again shortly, up to a limit. A share created in the same
// breath as the resource itself arrives before indexing has caught up.
func (p *Propagator) retryCreate(ev propagateEvent, cause error) {
	resource := ev.Record.ResourceType + ":" + ev.Record.ResourceID
	if ev.attempt+1 >= maxPropagateAttempts {
		p.log.Warn("propagate create: giving up on owner document",
			"resource", resource, "attempts", ev.attempt+1, "err", cause)
		return
	}

	ev.attempt++
	p.log.Debug("propagate create: owner document not indexed yet, retrying",
		"resource", resource, "attempt", ev.attempt)

	time.AfterFunc(RetryDelay, func() {
		if p.stopped.Load() {
			return
		}
		select {
		case p.ch <- ev:
		case <-p.quit:
		default:
			p.log.Warn("propagator channel full, dropping retry", "resource", resource)
		}
	})
}

func (p *Propagator) handleDelete(rec *ShareRecord) {
	if p.ResolveRecipientUsernames == nil || p.LookupUsername == nil {
		return
	}

	usernames := p.ResolveRecipientUsernames(rec)
	if len(usernames) == 0 {
		return
	}

	ownerUsername, err := p.LookupUsername(rec.OwnerUserID)
	if err != nil {
		p.log.Error("propagate delete: owner lookup", "err", err)
		return
	}

	docID := sharedDocID(ownerUsername, rec.ResourceType, rec.ResourceID)

	for _, username := range usernames {
		if username == ownerUsername {
			continue
		}
		if err := p.mgr.DeleteSharedDocument(username, docID); err != nil {
			p.log.Error("propagate delete: remove", "user", username, "err", err)
		}
	}
}

// fetchOwnerDocument finds a document in the owner's search index.
func (p *Propagator) fetchOwnerDocument(ownerUsername, resourceType, resourceID string) (*Document, error) {
	us, err := p.mgr.ensureUserIndex(ownerUsername)
	if err != nil {
		return nil, err
	}

	source := resourceType
	if p.ResolveSourceName != nil {
		if name, ok := p.ResolveSourceName(resourceType); ok {
			source = name
		}
	}

	docID := source + ":" + resourceID
	doc, err := us.index.GetDocument(docID)
	if err != nil {
		return nil, fmt.Errorf("document not found: %s", docID)
	}
	return doc, nil
}

// sharedDocID produces a unique document ID for shared resources.
func sharedDocID(ownerUsername, resourceType, resourceID string) string {
	return fmt.Sprintf("shared:%s:%s:%s", ownerUsername, resourceType, resourceID)
}
