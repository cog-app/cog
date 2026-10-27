package dav

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/itip"
	"git.duckfam.us/jj-software/cog/plugin"
	"github.com/google/uuid"
)

// Managed attachments (RFC 8607): a client POSTs a file to an event with
// ?action=attachment-add, and Cog stores it and adds an ATTACH naming it by
// URL and MANAGED-ID. The bytes are kept by the backend that stores events
// (AttachmentStore); the core serves them under Root+"attachments/", to
// whoever may see an event that carries them. DESIGN/dav-storage.md section
// 4.2.

// Limits on managed attachments, advertised as max-attachment-size and
// max-attachments-per-resource.
const (
	MaxAttachmentSize         = 25 << 20
	MaxAttachmentsPerResource = 20
)

// AttachmentInfo describes a stored managed attachment.
type AttachmentInfo struct {
	ManagedID string
	Size      int64
	FmtType   string
	Filename  string
}

// NewAttachment is a managed attachment stored with the write that first
// names it.
type NewAttachment struct {
	AttachmentInfo
	Data []byte
}

// AttachmentUse is a component that carries a managed attachment: the
// collection of its resource and the addresses of its attendees, who may
// fetch it.
type AttachmentUse struct {
	Collection CollectionID
	Attendees  []string
}

// AttachmentStore is implemented by the backend that keeps managed
// attachments: the one that stores events. A PutRequest's Attachments are
// stored in the same transaction as the resource; an attachment no ATTACH
// names any more is removed.
type AttachmentStore interface {
	// ManagedAttachments returns the owner's managed attachments among ids;
	// IDs the owner did not create are absent.
	ManagedAttachments(owner string, ids []string) (map[string]AttachmentInfo, error)
	// AttachmentUses returns the components carrying the owner's managed
	// attachment id, or ErrNotFound when the owner has none by that ID.
	AttachmentUses(owner, id string) ([]AttachmentUse, error)
	// OpenAttachment returns the attachment's bytes, or ErrNotFound.
	OpenAttachment(owner, id string) (*AttachmentInfo, io.ReadSeekCloser, error)
}

func (s *Server) attachments(bes []Backend) AttachmentStore {
	for _, b := range bes {
		if a, ok := b.(AttachmentStore); ok {
			return a
		}
	}
	return nil
}

const attachmentsPath = "attachments"

// AttachmentsURL is the URL every managed attachment's starts with.
func (s *Server) AttachmentsURL() string {
	return strings.TrimSuffix(s.BaseURL, "/") + Root + attachmentsPath + "/"
}

// AttachmentURL is where the owner's managed attachment id is served.
func (s *Server) AttachmentURL(owner, id string) string {
	return s.AttachmentsURL() + url.PathEscape(owner) + "/" + url.PathEscape(id)
}

// attachmentToken lets an external attendee, who has no Cog account, fetch
// an attachment of an event they are invited to: it is only accepted while
// that address is an attendee of a component carrying the attachment.
func (s *Server) attachmentToken(owner, id, addr string) string {
	mac := hmac.New(sha256.New, s.Secret)
	fmt.Fprintf(mac, "attachment\x00%s\x00%s\x00%s", owner, id, itip.Normalize(addr))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// forExternal rewrites a message for a recipient outside Cog: the owner's
// managed attachments carry a token that lets the recipient fetch them, and
// no MANAGED-ID, which means nothing to another server (RFC 8607 section
// 3.12.7).
func (s *Server) forExternal(owner string, o *ical.Object, addr string) *ical.Object {
	prefix := s.AttachmentURL(owner, "")
	out := o.Clone()
	for _, e := range out.Components {
		for i := range e.Attachments {
			a := &e.Attachments[i]
			if a.ManagedID == "" {
				continue
			}
			if strings.HasPrefix(a.Value, prefix) {
				a.Value = s.AttachmentURL(owner, a.ManagedID) + "?token=" + s.attachmentToken(owner, a.ManagedID, addr)
			}
			a.ManagedID = ""
		}
	}
	return out
}

// --- Serving ---

// serveAttachment answers a GET for /attachments/<owner>/<id>. The owner,
// anyone the event's collection is shared with for reading, and the
// attendees of a component carrying it may fetch it; an external attendee
// shows a token instead of logging in.
func (s *Server) serveAttachment(w http.ResponseWriter, r *http.Request, user *User, rest string) {
	ownerName, id, ok := strings.Cut(rest, "/")
	if !ok || ownerName == "" || id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// Attachments change only through their event (RFC 8607 sections
		// 3.8 and 3.9).
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	fail := func(err error) {
		s.Log.Error("dav: attachment", "owner", ownerName, "id", id, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
	token, feed := r.URL.Query().Get("token"), r.URL.Query().Get("feed")
	if user == nil && token == "" && feed == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cog"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	store := s.attachments(s.Backends())
	if store == nil {
		http.NotFound(w, r)
		return
	}
	owner, err := s.Users.ByUsername(ownerName)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		fail(err)
		return
	}
	allowed := user != nil && user.ID == owner.ID
	if !allowed {
		uses, err := store.AttachmentUses(owner.Username, id)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "Gone", http.StatusGone)
			return
		}
		if err != nil {
			fail(err)
			return
		}
		if allowed, err = s.mayFetch(owner, id, user, token, uses); err != nil {
			fail(err)
			return
		}
		if !allowed {
			if allowed, err = s.feedMayFetch(owner, id, feed, uses); err != nil {
				fail(err)
				return
			}
		}
	}
	if !allowed {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	info, f, err := store.OpenAttachment(owner.Username, id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "Gone", http.StatusGone)
		return
	}
	if err != nil {
		fail(err)
		return
	}
	defer f.Close()
	ctype := info.FmtType
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	// The bytes are whatever a client uploaded: never let a browser run
	// them on Cog's origin.
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": safeFilename(info.Filename, id)}))
	w.Header().Set("ETag", `"`+id+`"`) // an update makes a new ID
	w.Header().Set("Cache-Control", "private")
	http.ServeContent(w, r, "", time.Time{}, f)
}

// mayFetch reports whether user (nil with a token) may fetch the owner's
// attachment id, used by uses.
func (s *Server) mayFetch(owner *User, id string, user *User, token string, uses []AttachmentUse) (bool, error) {
	if user == nil {
		for _, u := range uses {
			for _, a := range u.Attendees {
				if hmac.Equal([]byte(token), []byte(s.attachmentToken(owner.Username, id, a))) {
					return true, nil
				}
			}
		}
		return false, nil
	}
	self := selfOf(user)
	var ids []CollectionID
	for _, u := range uses {
		if slices.ContainsFunc(u.Attendees, func(a string) bool { return self(itip.Normalize(a)) }) {
			return true, nil
		}
		if !slices.Contains(ids, u.Collection) {
			ids = append(ids, u.Collection)
		}
	}
	shared, err := sharesFor(s.Shares, user.ID)
	if err != nil {
		return false, err
	}
	cols, err := s.Collections.ByIDs(ids)
	if err != nil {
		return false, err
	}
	for i := range cols {
		if cols[i].OwnerID == owner.ID && shared.perms(&cols[i])&plugin.PermRead != 0 {
			return true, nil
		}
	}
	return false, nil
}

// safeFilename makes a client's file name safe to store and send (RFC 6266
// section 4.3): no directories, no control characters, never empty.
func safeFilename(name, fallback string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" || name == ".." {
		return fallback
	}
	return name
}

// --- Changing ---

// AttachmentAction is the action of an attachment POST.
type AttachmentAction string

const (
	AttachmentAdd    AttachmentAction = "attachment-add"
	AttachmentUpdate AttachmentAction = "attachment-update"
	AttachmentRemove AttachmentAction = "attachment-remove"
)

// AttachmentChange is one managed attachment operation on an event.
type AttachmentChange struct {
	Action    AttachmentAction
	ManagedID string   // update and remove
	RIDs      []string // add and remove: "M" or RECURRENCE-IDs; none means every component
	Data      []byte   // add and update
	FmtType   string
	Filename  string
	// MaySend is false for a sharee without the Schedule permission.
	MaySend    bool
	Conditions davsql.Conditions
}

// AttachmentResult is the event after an attachment operation.
type AttachmentResult struct {
	ManagedID string // the new MANAGED-ID; "" after a remove
	Version   int64
	Data      []byte
}

// PreconditionError is a request that failed a DAV precondition.
type PreconditionError struct {
	Status int
	Name   xml.Name
}

func (e *PreconditionError) Error() string { return "dav: precondition " + e.Name.Local }

func precondition(status int, name xml.Name) error { return &PreconditionError{status, name} }

// ChangeAttachment adds, updates or removes a managed attachment of the
// event at uri in owner's collection c, and sends what the change means to
// its attendees. Only the event's organizer may (RFC 8607 section 3.12.2);
// an event without one is the owner's own.
func (s *Server) ChangeAttachment(owner *User, c *Collection, uri string, ch AttachmentChange) (*AttachmentResult, error) {
	if c.ReadOnly() {
		return nil, ErrReadOnly
	}
	bes := s.backendsOf(c)
	store := s.attachments(bes)
	var events Backend
	for _, b := range bes {
		if slices.Contains(b.Components(), VEVENT) {
			events = b
		}
	}
	if store == nil || events == nil {
		return nil, precondition(http.StatusForbidden, caldav("valid-action"))
	}
	for attempt := 0; ; attempt++ {
		list, err := events.Get(owner.Username, c, []string{uri}, true)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, ErrNotFound
		}
		r := list[0]
		cond := ch.Conditions
		if err := cond.Check(&r.Version); err != nil {
			return nil, ErrPrecondition
		}
		if unconditional(cond) {
			cond = davsql.Conditions{IfMatch: []int64{r.Version}}
		}
		old, err := decodeEvent(r.Data)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", uri, err)
		}
		if org := itip.Organizer(old); org != nil && !selfOf(owner)(itip.Normalize(org.Address)) {
			return nil, precondition(http.StatusForbidden, dav("need-privileges"))
		}
		cur := old.Clone()
		res, add, err := s.applyAttachment(owner, c, cur, ch)
		if err != nil {
			return nil, err
		}
		d, err := s.Plan(ScheduleChange{Owner: owner, Old: old, New: cur, MaySend: ch.MaySend})
		if err != nil {
			return nil, err
		}
		req, err := objectRequest(uri, cur, cond)
		if err != nil {
			return nil, err
		}
		if len(req.Data) > maxResourceSize {
			return nil, precondition(http.StatusForbidden, caldav("max-resource-size"))
		}
		if add != nil {
			req.Attachments = []NewAttachment{*add}
		}
		put, err := s.put(bes, owner.Username, c, req)
		if errors.Is(err, ErrPrecondition) && unconditional(ch.Conditions) && attempt < 2 {
			continue
		}
		if errors.Is(err, plugin.ErrQuotaExceeded) {
			return nil, precondition(http.StatusForbidden, dav("quota-not-exceeded"))
		}
		if err != nil {
			return nil, err
		}
		d.Send()
		res.Version, res.Data = put.Version, put.Data
		return res, nil
	}
}

// applyAttachment makes the change to o and returns the attachment to store
// with it, if any.
func (s *Server) applyAttachment(owner *User, c *Collection, o *ical.Object, ch AttachmentChange) (*AttachmentResult, *NewAttachment, error) {
	z := ical.ZonesFor(o, c.zone())
	now := s.Clock()
	var changed []*ical.Component
	res := &AttachmentResult{}
	var add *NewAttachment
	if ch.Action == AttachmentAdd || ch.Action == AttachmentUpdate {
		if len(ch.Data) > MaxAttachmentSize {
			return nil, nil, precondition(http.StatusForbidden, caldav("max-attachment-size"))
		}
		size := int64(len(ch.Data))
		res.ManagedID = uuid.NewString()
		add = &NewAttachment{
			AttachmentInfo: AttachmentInfo{ManagedID: res.ManagedID, Size: size, FmtType: ch.FmtType, Filename: safeFilename(ch.Filename, "attachment")},
			Data:           ch.Data,
		}
	}
	switch ch.Action {
	case AttachmentAdd:
		if ch.ManagedID != "" {
			return nil, nil, precondition(http.StatusConflict, caldav("valid-managed-id"))
		}
		if len(o.ManagedIDs()) >= MaxAttachmentsPerResource {
			return nil, nil, precondition(http.StatusForbidden, caldav("max-attachments-per-resource"))
		}
		targets, err := o.Targets(ch.RIDs, z)
		if err != nil {
			return nil, nil, precondition(http.StatusConflict, caldav("valid-rid"))
		}
		size := add.Size
		for _, e := range targets {
			e.Attachments = append(e.Attachments, ical.Attachment{
				Value: s.AttachmentURL(owner.Username, add.ManagedID), ManagedID: add.ManagedID,
				FmtType: add.FmtType, Size: &size, Filename: add.Filename,
			})
		}
		changed = targets
	case AttachmentUpdate:
		if len(ch.RIDs) > 0 {
			return nil, nil, precondition(http.StatusConflict, caldav("valid-rid"))
		}
		changed = o.Holding(ch.ManagedID)
		if ch.ManagedID == "" || len(changed) == 0 {
			return nil, nil, precondition(http.StatusConflict, caldav("valid-managed-id"))
		}
		for _, e := range changed {
			for i := range e.Attachments {
				a := &e.Attachments[i]
				if a.ManagedID != ch.ManagedID {
					continue
				}
				size := add.Size
				a.Value, a.ManagedID, a.Size, a.FmtType, a.Filename = s.AttachmentURL(owner.Username, add.ManagedID), add.ManagedID, &size, add.FmtType, add.Filename
			}
		}
	case AttachmentRemove:
		if ch.ManagedID == "" || len(o.Holding(ch.ManagedID)) == 0 {
			return nil, nil, precondition(http.StatusConflict, caldav("valid-managed-id"))
		}
		targets, err := o.Targets(ch.RIDs, z)
		if err != nil {
			return nil, nil, precondition(http.StatusConflict, caldav("valid-rid"))
		}
		for _, e := range targets {
			if e.RemoveAttachment(ch.ManagedID) {
				changed = append(changed, e)
			}
		}
		if len(changed) == 0 {
			return nil, nil, precondition(http.StatusConflict, caldav("valid-managed-id"))
		}
	default:
		return nil, nil, precondition(http.StatusForbidden, caldav("valid-action"))
	}
	// Attendees learn of the change: it is news, as a new SEQUENCE says.
	for _, e := range changed {
		e.Stamp(now, true)
	}
	return res, add, nil
}

// attachmentPost answers a POST on an event (RFC 8607 section 3.3).
func (q *request) attachmentPost(c *Collection, owner *User, a access, uri string) {
	query := q.r.URL.Query()
	actions := query["action"]
	if len(actions) != 1 {
		writeError(q.w, http.StatusForbidden, caldav("valid-action"), "")
		return
	}
	ch := AttachmentChange{
		Action:     AttachmentAction(actions[0]),
		ManagedID:  query.Get("managed-id"),
		MaySend:    a.schedule(),
		Conditions: conditions(q.r, c.ID),
	}
	if rid := query.Get("rid"); query.Has("rid") {
		ch.RIDs = strings.Split(rid, ",")
		if slices.Contains(ch.RIDs, "") {
			writeError(q.w, http.StatusConflict, caldav("valid-rid"), "")
			return
		}
	}
	if ch.Action == AttachmentAdd || ch.Action == AttachmentUpdate {
		data, err := io.ReadAll(io.LimitReader(q.r.Body, MaxAttachmentSize+1))
		if err != nil {
			http.Error(q.w, "read body", http.StatusBadRequest)
			return
		}
		ch.Data = data
		// FMTTYPE is the media type, without parameters, as in RFC 8607's
		// examples.
		if mt, _, err := mime.ParseMediaType(q.r.Header.Get("Content-Type")); err == nil {
			ch.FmtType = mt
		}
		if _, params, err := mime.ParseMediaType(q.r.Header.Get("Content-Disposition")); err == nil {
			ch.Filename = params["filename"]
		}
	}
	res, err := q.s.ChangeAttachment(owner, c, uri, ch)
	var pe *PreconditionError
	switch {
	case errors.As(err, &pe):
		writeError(q.w, pe.Status, pe.Name, "")
		return
	case errors.Is(err, ErrNotFound):
		http.NotFound(q.w, q.r)
		return
	case errors.Is(err, ErrPrecondition):
		http.Error(q.w, "Precondition Failed", http.StatusPreconditionFailed)
		return
	case err != nil:
		q.fail(err)
		return
	}
	q.w.Header().Set("ETag", ETag(c.ID, res.Version))
	if res.ManagedID != "" {
		q.w.Header().Set("Cal-Managed-ID", res.ManagedID)
	}
	status := http.StatusOK
	switch ch.Action {
	case AttachmentAdd:
		status = http.StatusCreated
	case AttachmentRemove:
		status = http.StatusNoContent
	}
	if !wantsRepresentation(q.r) {
		q.w.WriteHeader(status)
		return
	}
	if status == http.StatusNoContent {
		status = http.StatusOK
	}
	q.w.Header().Set("Content-Type", contentType(VEVENT))
	q.w.Header().Set("Content-Location", q.r.URL.Path)
	q.w.Header().Set("Content-Length", strconv.Itoa(len(res.Data)))
	q.w.WriteHeader(status)
	q.w.Write(res.Data)
}

// wantsRepresentation reports Prefer: return=representation (RFC 7240).
func wantsRepresentation(r *http.Request) bool {
	for _, v := range r.Header.Values("Prefer") {
		for p := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(p), "return=representation") {
				return true
			}
		}
	}
	return false
}

// --- PUT ---

// errManagedID is a PUT naming a managed attachment its owner did not
// create (RFC 8607 section 3.11, valid-managed-id-parameter).
var errManagedID = precondition(http.StatusForbidden, caldav("valid-managed-id-parameter"))

// checkManaged validates the managed attachments a PUT names: each must be
// one the stored copy already had (an invitation's, kept as received) or
// one of the owner's own, whose URL and size are then set from what is
// stored (RFC 8607 section 3.7).
func (s *Server) checkManaged(owner string, old, cur *ical.Object) error {
	ids := cur.ManagedIDs()
	if len(ids) == 0 {
		return nil
	}
	kept := map[string]string{} // managed ID → URL on the stored copy
	if old != nil {
		for _, e := range old.Components {
			for _, a := range e.Attachments {
				if a.ManagedID != "" {
					kept[a.ManagedID] = a.Value
				}
			}
		}
	}
	store := s.attachments(s.Backends())
	var own map[string]AttachmentInfo
	if store != nil {
		var err error
		if own, err = store.ManagedAttachments(owner, ids); err != nil {
			return err
		}
	}
	for _, e := range cur.Components {
		for i := range e.Attachments {
			a := &e.Attachments[i]
			if a.ManagedID == "" {
				continue
			}
			if info, ok := own[a.ManagedID]; ok {
				size := info.Size
				a.Value, a.Size = s.AttachmentURL(owner, a.ManagedID), &size
				continue
			}
			if v, ok := kept[a.ManagedID]; !ok || v != a.Value {
				return errManagedID
			}
		}
	}
	return nil
}

// relinkManaged gives each ATTACH naming one of the owner's managed
// attachments (its URL starts with prefix) the URL link returns for its
// MANAGED-ID.
func relinkManaged(c *contentline.Component, prefix string, link func(id string) string) {
	for i := range c.Props {
		p := &c.Props[i]
		if !p.Is("ATTACH") || !strings.HasPrefix(p.Value, prefix) {
			continue
		}
		if id := p.Param("MANAGED-ID"); id != "" {
			p.Value = link(id)
		}
	}
	for _, sub := range c.Components {
		relinkManaged(sub, prefix, link)
	}
}

// stripManaged removes MANAGED-ID from ATTACH properties: on export and
// import the IDs mean nothing (RFC 8607 section 3.12.7).
func stripManaged(c *contentline.Component) {
	for i := range c.Props {
		if c.Props[i].Is("ATTACH") {
			c.Props[i].Params = slices.DeleteFunc(slices.Clone(c.Props[i].Params), func(p contentline.Param) bool {
				return strings.EqualFold(p.Name, "MANAGED-ID")
			})
		}
	}
	for _, sub := range c.Components {
		stripManaged(sub)
	}
}
