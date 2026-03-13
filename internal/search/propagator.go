package search

import (
	"fmt"
	"log/slog"
	"sync/atomic"
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
type Propagator struct {
	log     *slog.Logger
	mgr     *Manager
	ch      chan propagateEvent
	done    chan struct{}
	started atomic.Bool

	// Function callbacks to avoid import cycles.
	// ResolveRecipientUsernames expands a share into target usernames.
	ResolveRecipientUsernames func(rec *ShareRecord) []string
	// LookupUsername maps a user ID to a username.
	LookupUsername func(userID string) (string, error)
}

type propagateEvent struct {
	Kind   string // "create", "delete"
	Record *ShareRecord
}

// NewPropagator creates a propagator wired to the search manager.
func NewPropagator(log *slog.Logger, mgr *Manager) *Propagator {
	return &Propagator{
		log:  log.With("component", "search-propagator"),
		mgr:  mgr,
		ch:   make(chan propagateEvent, 1024),
		done: make(chan struct{}),
	}
}

// Start begins the background worker goroutine.
func (p *Propagator) Start() {
	p.started.Store(true)
	go p.run()
}

// Stop closes the channel and waits for the worker to drain.
// It is safe to call even if Start was never called.
func (p *Propagator) Stop() {
	if !p.started.Load() {
		return
	}
	close(p.ch)
	<-p.done
}

// OnShareCreate queues a share-create event for propagation.
func (p *Propagator) OnShareCreate(rec *ShareRecord) {
	if rec == nil {
		return
	}
	select {
	case p.ch <- propagateEvent{Kind: "create", Record: rec}:
	default:
		p.log.Warn("propagator channel full, dropping create event")
	}
}

// OnShareDelete queues a share-delete event for propagation.
func (p *Propagator) OnShareDelete(rec *ShareRecord) {
	if rec == nil {
		return
	}
	select {
	case p.ch <- propagateEvent{Kind: "delete", Record: rec}:
	default:
		p.log.Warn("propagator channel full, dropping delete event")
	}
}

func (p *Propagator) run() {
	defer close(p.done)
	for ev := range p.ch {
		switch ev.Kind {
		case "create":
			p.handleCreate(ev.Record)
		case "delete":
			p.handleDelete(ev.Record)
		}
	}
}

func (p *Propagator) handleCreate(rec *ShareRecord) {
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
		p.log.Debug("propagate create: no owner document", "resource", rec.ResourceType+":"+rec.ResourceID, "err", err)
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

	docID := resourceType + ":" + resourceID
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
