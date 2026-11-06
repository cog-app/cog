package web

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/httphdr"
)

// maxImportSize bounds an uploaded .ics or .vcf file.
const maxImportSize = 20 << 20

// dataHandler serves /settings/data: import and export of the user's
// calendars, task lists and address books, over the DAV core's backends.
type dataHandler struct{ handler }

// dataCollection is one row of the collections table.
type dataCollection struct {
	ID       int64
	Name     string
	Kind     string // "Events, Tasks", "Contacts"
	Filename string
	ReadOnly bool // filled by Cog: exported, never imported into
}

var componentLabels = map[dav.Component]string{
	dav.VEVENT: "Events", dav.VTODO: "Tasks", dav.VJOURNAL: "Journals", dav.VCARD: "Contacts",
}

func filename(c *dav.Collection) string {
	ext := ".ics"
	if c.Home == dav.HomeAddressBooks {
		ext = ".vcf"
	}
	return c.Name + ext
}

// homes returns the homes some enabled backend stores resources in.
func (h *dataHandler) homes() []dav.Home {
	var homes []dav.Home
	if h.p.DAV.Stores(dav.VEVENT) || h.p.DAV.Stores(dav.VTODO) || h.p.DAV.Stores(dav.VJOURNAL) {
		homes = append(homes, dav.HomeCalendars)
	}
	if h.p.DAV.Stores(dav.VCARD) {
		homes = append(homes, dav.HomeAddressBooks)
	}
	return homes
}

// collections returns the user's own collections in every home with a
// backend.
func (h *dataHandler) collections(userID string) ([]dav.Collection, error) {
	var all []dav.Collection
	for _, home := range h.homes() {
		cols, err := h.p.DAV.Collections.EnsureDefault(userID, home)
		if err != nil {
			return nil, err
		}
		all = append(all, cols...)
	}
	return all, nil
}

// ShowData renders the import and export page.
func (h *dataHandler) ShowData(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Import and export", h.p.CSRFSecret, h.p.Plugins)
	u := auth.UserFromContext(r.Context())
	cols, err := h.collections(u.ID)
	if err != nil {
		h.p.Log.Error("list collections", "user", u.Username, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	rows := make([]dataCollection, len(cols))
	for i := range cols {
		var kinds []string
		for _, comp := range cols[i].Components {
			kinds = append(kinds, componentLabels[comp])
		}
		rows[i] = dataCollection{ID: int64(cols[i].ID), Name: cols[i].Title(), Kind: strings.Join(kinds, ", "), Filename: filename(&cols[i]), ReadOnly: cols[i].ReadOnly()}
	}
	var flashMsg, flashKind string
	if f := getFlash(w, r); f != nil {
		flashMsg, flashKind = f.Message, f.Kind
	}
	if err := settingsDataPage(pd, flashMsg, flashKind, rows, h.homes()).Render(r.Context(), w); err != nil {
		h.p.Log.Error("render import and export page", "err", err)
	}
}

// ownCollection returns the request user's collection named by the id value.
func (h *dataHandler) ownCollection(r *http.Request, value string) (*dav.Collection, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, dav.ErrNotFound
	}
	c, err := h.p.DAV.Collections.ByID(dav.CollectionID(id))
	if err != nil {
		return nil, err
	}
	if c.OwnerID != auth.UserFromContext(r.Context()).ID {
		return nil, dav.ErrNotFound
	}
	return c, nil
}

// Export downloads one collection as a single .ics or .vcf file.
func (h *dataHandler) Export(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	c, err := h.ownCollection(r, r.PathValue("id"))
	if errors.Is(err, dav.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.p.Log.Error("export collection", "user", u.Username, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	// Generate first, so a failure is an error page rather than a truncated
	// download.
	var buf bytes.Buffer
	if err := h.p.DAV.Export(&buf, u.Username, c); err != nil {
		h.p.Log.Error("export collection", "user", u.Username, "collection", c.ID, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	ct := "text/calendar; charset=utf-8"
	if c.Home == dav.HomeAddressBooks {
		ct = "text/vcard; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", httphdr.Attachment(filename(c)))
	if _, err := w.Write(buf.Bytes()); err != nil {
		h.p.Log.Error("send export", "user", u.Username, "err", err)
	}
}

// ExportAll downloads every collection as a zip, one file per collection.
func (h *dataHandler) ExportAll(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	cols, err := h.collections(u.ID)
	if err != nil {
		h.p.Log.Error("export all", "user", u.Username, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := range cols {
		f, err := zw.Create(string(cols[i].Home) + "/" + filename(&cols[i]))
		if err == nil {
			err = h.p.DAV.Export(f, u.Username, &cols[i])
		}
		if err != nil {
			h.p.Log.Error("export all", "user", u.Username, "collection", cols[i].ID, "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}
	if err := zw.Close(); err != nil {
		h.p.Log.Error("export all", "user", u.Username, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="cog-export.zip"`)
	if _, err := w.Write(buf.Bytes()); err != nil {
		h.p.Log.Error("send export", "user", u.Username, "err", err)
	}
}

// Import stores an uploaded file's resources in an existing collection or a
// new one, then redirects with the result (PRG).
func (h *dataHandler) Import(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	back := func(kind, msg string, errs ...error) {
		setFlash(w, kind, msg, errs...)
		http.Redirect(w, r, "/settings/data", http.StatusSeeOther)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportSize+1<<20)
	if err := r.ParseMultipartForm(maxImportSize); err != nil {
		back("error", "The file is too large or the upload failed.")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		back("error", "Choose a file to import.")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxImportSize+1))
	if err != nil {
		back("error", "Could not read the file.", err)
		return
	}
	if len(data) > maxImportSize {
		back("error", "The file is too large.")
		return
	}
	comps, err := dav.ComponentsIn(bytes.NewReader(data))
	if err != nil {
		back("error", "This is not an iCalendar or vCard file: "+err.Error())
		return
	}

	var c *dav.Collection
	switch target := r.FormValue("target"); target {
	case "new:" + string(dav.HomeCalendars), "new:" + string(dav.HomeAddressBooks):
		home := dav.Home(strings.TrimPrefix(target, "new:"))
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			back("error", "Give the new collection a name.")
			return
		}
		c = &dav.Collection{OwnerID: u.ID, Home: home, Name: dav.Slug(name, "imported"), DisplayName: name, Color: "#0082c9"}
		for _, comp := range comps {
			if comp.Home() == home && h.p.DAV.Stores(comp) {
				c.Components = append(c.Components, comp)
			}
		}
		if len(c.Components) == 0 {
			back("error", "The file has nothing a "+strings.TrimSuffix(string(home), "s")+" can hold.")
			return
		}
		if err := h.p.DAV.Collections.CreateNamed(c); err != nil {
			back("error", "Could not create the collection.", err)
			return
		}
	default:
		c, err = h.ownCollection(r, target)
		if errors.Is(err, dav.ErrNotFound) {
			back("error", "Choose where to import the file.")
			return
		}
		if err != nil {
			back("error", "Could not open the collection.", err)
			return
		}
	}

	res, err := h.p.DAV.Import(u.Username, c, bytes.NewReader(data))
	if err != nil {
		back("error", "Import failed: "+err.Error())
		return
	}
	msg := fmt.Sprintf("Imported into %s: %d new, %d replaced, %d skipped.", c.Title(), res.Imported, res.Replaced, res.Skipped)
	kind := "success"
	if res.Failed > 0 {
		kind = "error"
		msg += fmt.Sprintf(" %d failed: %s", res.Failed, strings.Join(res.Reasons, "; "))
		if res.Failed > len(res.Reasons) {
			msg += "; …"
		}
	}
	back(kind, msg)
}
