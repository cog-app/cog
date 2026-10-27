package calendar

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// AttachmentView is an event's attachment as the modal lists it.
type AttachmentView struct {
	Name string
	Size string
	// URL downloads it: Cog's own for managed and inline attachments, the
	// link itself for an http(s) URI, "" for anything else.
	URL string
	// ManagedID is set for the user's own managed attachments, which the
	// modal can remove.
	ManagedID string
}

// attachmentsOf lists the attachments of the event's component at
// position comp.
func (h *Pages) attachmentsOf(ev *Event, comp int, username string) []AttachmentView {
	if comp < 0 || comp >= len(ev.Object.Components) {
		return nil
	}
	served := ""
	own := ""
	if h.plugin.dav != nil {
		served = h.plugin.dav.AttachmentsURL()
		own = h.plugin.dav.AttachmentURL(username, "")
	}
	var out []AttachmentView
	for n, a := range ev.Object.Components[comp].Attachments {
		v := AttachmentView{Name: a.Filename}
		if a.Size != nil {
			v.Size = formatSize(*a.Size)
		}
		switch {
		case a.Inline:
			v.URL = fmt.Sprintf("/apps/calendar/events/%d/attachments/%d/%d", ev.ID, comp, n)
			if v.Size == "" {
				v.Size = formatSize(int64(base64.StdEncoding.DecodedLen(len(a.Value))))
			}
		case served != "" && strings.HasPrefix(a.Value, served):
			// Served by Cog, the user's own or an organizer's: link to it on
			// whatever origin the browser uses.
			if u, err := url.Parse(a.Value); err == nil {
				v.URL = u.RequestURI()
			}
			if a.ManagedID != "" && strings.HasPrefix(a.Value, own) {
				v.ManagedID = a.ManagedID
			}
		default:
			if u, err := url.Parse(a.Value); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
				v.URL = a.Value
			}
		}
		if v.Name == "" {
			v.Name = attachmentName(a)
		}
		out = append(out, v)
	}
	return out
}

// attachmentName names an attachment that has no FILENAME: from its URL's
// last segment, or its type.
func attachmentName(a ical.Attachment) string {
	if !a.Inline {
		if u, err := url.Parse(a.Value); err == nil {
			if seg := u.Path[strings.LastIndex(u.Path, "/")+1:]; seg != "" {
				return seg
			}
			return a.Value
		}
	}
	if a.FmtType != "" {
		return "Attachment (" + a.FmtType + ")"
	}
	return "Attachment"
}

func formatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// eventPage is where a POST about an event goes back to: the calendar, with
// the event's modal open again.
func eventPage(ev *Event) string { return fmt.Sprintf("/apps/calendar/?event=%d", ev.ID) }

// attachmentError says what a failed attachment change means to the user.
func attachmentError(err error) string {
	var pe *dav.PreconditionError
	if errors.As(err, &pe) {
		switch pe.Name.Local {
		case "max-attachment-size":
			return fmt.Sprintf("The file is larger than %s.", formatSize(dav.MaxAttachmentSize))
		case "max-attachments-per-resource":
			return fmt.Sprintf("An event can have at most %d attachments.", dav.MaxAttachmentsPerResource)
		case "quota-not-exceeded":
			return "The file does not fit in your storage quota."
		case "need-privileges":
			return "Only the organizer can change this event's attachments."
		case "valid-managed-id":
			return "That attachment is no longer on the event."
		}
	}
	return "The attachment could not be saved: " + err.Error()
}

// changeAttachment applies an attachment change to the request's event and
// redirects back to it with a flash message (PRG).
func (h *Pages) changeAttachment(w http.ResponseWriter, r *http.Request, ev *Event, cal *Calendar, userID string, ch dav.AttachmentChange, done string) {
	fail := func(msg string) {
		plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: msg})
		http.Redirect(w, r, eventPage(ev), http.StatusSeeOther)
	}
	if h.plugin.dav == nil {
		fail("Attachments need the DAV server.")
		return
	}
	me, err := h.plugin.me(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c, err := h.plugin.dav.Collections.ByID(dav.CollectionID(cal.ID))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ch.MaySend = true
	ch.Conditions.IfMatch = []int64{ev.Version} // what the user was shown
	if _, err := h.plugin.dav.ChangeAttachment(me, c, ev.URI, ch); err != nil {
		if errors.Is(err, dav.ErrPrecondition) {
			fail("The event changed meanwhile; try again.")
			return
		}
		var pe *dav.PreconditionError
		if !errors.As(err, &pe) {
			h.plugin.log.Error("change attachment", "event", ev.ID, "err", err)
		}
		fail(attachmentError(err))
		return
	}
	plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: done})
	http.Redirect(w, r, eventPage(ev), http.StatusSeeOther)
}

// UploadAttachment handles POST of a file to attach to an event, as a
// managed attachment, so DAV clients see it too.
func (h *Pages) UploadAttachment(w http.ResponseWriter, r *http.Request) {
	_, userID, ev, cal, status, err := h.ownWritable(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: "Choose a file to attach."})
		http.Redirect(w, r, eventPage(ev), http.StatusSeeOther)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, dav.MaxAttachmentSize+1))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctype := hdr.Header.Get("Content-Type")
	if _, _, err := mime.ParseMediaType(ctype); err != nil || ctype == "" {
		ctype = http.DetectContentType(data)
	}
	h.changeAttachment(w, r, ev, cal, userID, dav.AttachmentChange{
		Action: dav.AttachmentAdd, Data: data, FmtType: ctype, Filename: hdr.Filename,
	}, "Attached "+hdr.Filename+".")
}

// RemoveAttachment handles POST to remove one of the user's managed
// attachments from every component of an event.
func (h *Pages) RemoveAttachment(w http.ResponseWriter, r *http.Request) {
	_, userID, ev, cal, status, err := h.ownWritable(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	h.changeAttachment(w, r, ev, cal, userID, dav.AttachmentChange{
		Action: dav.AttachmentRemove, ManagedID: r.FormValue("managed_id"),
	}, "Attachment removed.")
}

// InlineAttachment serves an attachment a client sent inline (base64 in the
// event) as a download.
func (h *Pages) InlineAttachment(w http.ResponseWriter, r *http.Request) {
	_, _, ev, _, status, err := h.ownEvent(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	comp, err1 := strconv.Atoi(r.PathValue("comp"))
	n, err2 := strconv.Atoi(r.PathValue("n"))
	if err1 != nil || err2 != nil || comp < 0 || comp >= len(ev.Object.Components) ||
		n < 0 || n >= len(ev.Object.Components[comp].Attachments) || !ev.Object.Components[comp].Attachments[n].Inline {
		http.NotFound(w, r)
		return
	}
	a := ev.Object.Components[comp].Attachments[n]
	data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(a.Value), ""))
	if err != nil {
		http.Error(w, "the attachment's data is not valid base64", http.StatusUnprocessableEntity)
		return
	}
	ctype := a.FmtType
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	name := a.Filename
	if name == "" {
		name = "attachment"
	}
	// Never let a browser run what a client stored on Cog's origin.
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if _, err := w.Write(data); err != nil {
		h.plugin.log.Warn("send attachment", "event", ev.ID, "err", err)
	}
}

// Local reports whether Cog serves the attachment, as a download.
func (v AttachmentView) Local() bool { return strings.HasPrefix(v.URL, "/") }
