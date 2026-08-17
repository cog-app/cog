package search

import (
	"fmt"
	"log/slog"
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
}

type propagateEvent struct {
	Kind    string // "create", "delete"
	Record  *ShareRecord
	attempt int
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
			switch ev.Kind {
			case "create":
				p.handleCreate(ev)
			case "delete":
				p.handleDelete(ev.Record)
			}
		case <-p.quit:
			// Drain remaining events then exit.
			for {
				select {
				case ev := <-p.ch:
					switch ev.Kind {
					case "create":
						p.handleCreate(ev)
					case "delete":
						p.handleDelete(ev.Record)
					}
				default:
					return
				}
			}
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
