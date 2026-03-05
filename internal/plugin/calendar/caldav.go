package calendar

import (
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
)

// CalDAVHandler implements CalDAV for the calendar plugin.
// Mounted at /remote.php/dav/calendars/ and /remote.php/dav/principals/users/.
type CalDAVHandler struct {
	plugin *Plugin
}

// NewCalDAVHandler creates a new CalDAV handler.
func NewCalDAVHandler(p *Plugin) *CalDAVHandler {
	return &CalDAVHandler{plugin: p}
}

func (h *CalDAVHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cog"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	path := r.URL.Path

	if strings.HasPrefix(path, "/remote.php/dav/principals/") {
		h.handlePrincipal(w, r, u.Username)
		return
	}

	if strings.HasPrefix(path, "/remote.php/dav/calendars/") {
		db, err := h.plugin.DB(u.Username)
		if err != nil {
			log.Printf("caldav: db error for %s: %v", u.Username, err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if err := EnsureDefaultCalendar(db); err != nil {
			log.Printf("caldav: ensure default calendar: %v", err)
		}
		h.dispatch(w, r, db, u.Username)
		return
	}

	w.WriteHeader(http.StatusNotFound)
}

// handlePrincipal responds to PROPFIND on principal URLs.
func (h *CalDAVHandler) handlePrincipal(w http.ResponseWriter, r *http.Request, username string) {
	switch r.Method {
	case "PROPFIND":
		href := "/remote.php/dav/principals/users/" + username + "/"
		calHome := "/remote.php/dav/calendars/" + username + "/"
		bookHome := "/remote.php/dav/addressbooks/" + username + "/"
		writeMultiStatus(w, []davResponse{{
			Href: href,
			Props: []xmlProp{
				rawProp("D:resourcetype", `<D:collection/><D:principal/>`),
				textProp("D:displayname", username),
				rawProp("D:current-user-principal", fmt.Sprintf(`<D:href>%s</D:href>`, href)),
				rawProp("C:calendar-home-set", fmt.Sprintf(`<D:href>%s</D:href>`, calHome)),
				rawProp("CR:addressbook-home-set", fmt.Sprintf(`<D:href>%s</D:href>`, bookHome)),
				rawProp("D:supported-report-set",
					`<D:supported-report><D:report><C:calendar-multiget/></D:report></D:supported-report>`+
						`<D:supported-report><D:report><C:calendar-query/></D:report></D:supported-report>`+
						`<D:supported-report><D:report><D:sync-collection/></D:report></D:supported-report>`),
				rawProp("CS:email-address-set", ""),
			},
		}})
	case http.MethodOptions:
		writeOptions(w)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// dispatch routes calendar requests by path depth.
func (h *CalDAVHandler) dispatch(w http.ResponseWriter, r *http.Request, db *sql.DB, username string) {
	rest := strings.TrimPrefix(r.URL.Path, "/remote.php/dav/calendars/")
	parts := strings.SplitN(rest, "/", 3) // [user, caluid?, eventuid?]

	switch {
	case len(parts) <= 1 || parts[1] == "":
		h.calendarHome(w, r, db, username)
	case len(parts) == 2 || parts[2] == "":
		h.calendarCollection(w, r, db, username, strings.TrimSuffix(parts[1], "/"))
	default:
		eventFile := strings.TrimSuffix(parts[2], "/")
		if eventFile == "" {
			h.calendarCollection(w, r, db, username, parts[1])
			return
		}
		h.event(w, r, db, username, parts[1], strings.TrimSuffix(eventFile, ".ics"))
	}
}

// calendarHome lists all calendars for a user.
func (h *CalDAVHandler) calendarHome(w http.ResponseWriter, r *http.Request, db *sql.DB, username string) {
	switch r.Method {
	case "PROPFIND":
		homeHref := "/remote.php/dav/calendars/" + username + "/"
		depth := r.Header.Get("Depth")

		responses := []davResponse{{
			Href: homeHref,
			Props: []xmlProp{
				rawProp("D:resourcetype", `<D:collection/>`),
				textProp("D:displayname", username),
			},
		}}

		if depth != "0" {
			cals, _ := ListCalendars(db)
			for _, cal := range cals {
				responses = append(responses, calResponse(homeHref+cal.UID+"/", &cal, username))
			}
		}
		writeMultiStatus(w, responses)
	case http.MethodOptions:
		writeOptions(w)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// calendarCollection handles PROPFIND/MKCALENDAR/DELETE/REPORT/PROPPATCH on a single calendar.
func (h *CalDAVHandler) calendarCollection(w http.ResponseWriter, r *http.Request, db *sql.DB, username, calUID string) {
	cal, err := GetCalendarByUID(db, calUID)
	if err != nil {
		if r.Method == "MKCALENDAR" {
			h.mkCalendar(w, r, db, calUID)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}

	switch r.Method {
	case "PROPFIND":
		calHref := "/remote.php/dav/calendars/" + username + "/" + calUID + "/"
		depth := r.Header.Get("Depth")
		responses := []davResponse{calResponse(calHref, cal, username)}

		if depth != "0" {
			events, _ := ListEvents(db, cal.ID)
			for _, ev := range events {
				responses = append(responses, eventResponse(calHref+ev.UID+".ics", &ev))
			}
		}
		writeMultiStatus(w, responses)
	case "REPORT":
		h.handleReport(w, r, db, username, cal)
	case "PROPPATCH":
		h.handleProppatch(w, r, db, username, cal)
	case "MKCALENDAR":
		h.mkCalendar(w, r, db, calUID)
	case http.MethodDelete:
		if err := DeleteCalendar(db, cal.ID); err != nil {
			log.Printf("caldav: delete calendar %d: %v", cal.ID, err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodOptions:
		writeOptions(w)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *CalDAVHandler) mkCalendar(w http.ResponseWriter, r *http.Request, db *sql.DB, calUID string) {
	name := calUID
	color := "#0082c9"
	description := ""

	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if len(body) > 0 {
		if n := extractXMLElement(string(body), "displayname"); n != "" {
			name = n
		}
		if c := extractXMLElement(string(body), "calendar-color"); c != "" {
			color = c
		}
		if d := extractXMLElement(string(body), "calendar-description"); d != "" {
			description = d
		}
	}

	_, err := CreateCalendar(db, calUID, name, color, description)
	if err != nil {
		log.Printf("caldav: create calendar %s: %v", calUID, err)
		http.Error(w, "Conflict", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// event handles GET/PUT/DELETE/PROPFIND on a single event resource.
func (h *CalDAVHandler) event(w http.ResponseWriter, r *http.Request, db *sql.DB, username, calUID, eventUID string) {
	cal, err := GetCalendarByUID(db, calUID)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		ev, err := GetEventByUID(db, cal.ID, eventUID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("ETag", `"`+ev.ETag+`"`)
		io.WriteString(w, ev.ICSData)

	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		icsData := string(body)

		// If-None-Match: * means "create only" — fail if event exists.
		if r.Header.Get("If-None-Match") == "*" {
			if _, err := GetEventByUID(db, cal.ID, eventUID); err == nil {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
		}

		if ifMatch := r.Header.Get("If-Match"); ifMatch != "" {
			existing, err := GetEventByUID(db, cal.ID, eventUID)
			if err == nil {
				existingETag := `"` + existing.ETag + `"`
				if ifMatch != existingETag && ifMatch != existing.ETag {
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
			}
		}

		fields, err := ParseVEvent(icsData)
		if err != nil {
			http.Error(w, "invalid ICS: "+err.Error(), http.StatusBadRequest)
			return
		}

		ev, err := UpsertEvent(db, cal.ID, eventUID, icsData, fields)
		if err != nil {
			log.Printf("caldav: upsert event %s: %v", eventUID, err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("ETag", `"`+ev.ETag+`"`)
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		if err := DeleteEvent(db, cal.ID, eventUID); err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case "PROPFIND":
		ev, err := GetEventByUID(db, cal.ID, eventUID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		evHref := "/remote.php/dav/calendars/" + username + "/" + calUID + "/" + eventUID + ".ics"
		writeMultiStatus(w, []davResponse{eventResponse(evHref, ev)})

	case http.MethodOptions:
		writeOptions(w)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// --- REPORT handler ---

// reportBody is used to detect the report type from the XML request body.
type reportBody struct {
	XMLName xml.Name
	Hrefs   []string `xml:"href"`
	Filter  struct {
		CompFilter struct {
			Name       string `xml:"name,attr"`
			CompFilter struct {
				Name      string `xml:"name,attr"`
				TimeRange struct {
					Start string `xml:"start,attr"`
					End   string `xml:"end,attr"`
				} `xml:"time-range"`
			} `xml:"comp-filter"`
		} `xml:"filter>comp-filter"`
	}
	SyncToken string `xml:"sync-token"`
}

func (h *CalDAVHandler) handleReport(w http.ResponseWriter, r *http.Request, db *sql.DB, username string, cal *Calendar) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var rb reportBody
	if err := xml.Unmarshal(body, &rb); err != nil {
		http.Error(w, "invalid XML", http.StatusBadRequest)
		return
	}

	localName := rb.XMLName.Local
	switch localName {
	case "calendar-multiget":
		h.reportMultiget(w, db, username, cal, rb.Hrefs)
	case "calendar-query":
		h.reportQuery(w, db, username, cal, &rb)
	case "sync-collection":
		h.reportSyncCollection(w, db, username, cal, rb.SyncToken)
	default:
		http.Error(w, "unsupported report: "+localName, http.StatusBadRequest)
	}
}

func (h *CalDAVHandler) reportMultiget(w http.ResponseWriter, db *sql.DB, username string, cal *Calendar, hrefs []string) {
	calHref := "/remote.php/dav/calendars/" + username + "/" + cal.UID + "/"
	var responses []davResponse

	for _, href := range hrefs {
		href = strings.TrimSpace(href)
		// Extract event UID from href.
		uid := strings.TrimPrefix(href, calHref)
		uid = strings.TrimSuffix(uid, ".ics")
		if uid == "" {
			continue
		}

		ev, err := GetEventByUID(db, cal.ID, uid)
		if err != nil {
			// Return 404 for missing events.
			responses = append(responses, davResponse{
				Href:   href,
				Status: "HTTP/1.1 404 Not Found",
			})
			continue
		}

		responses = append(responses, eventResponseWithData(href, ev))
	}

	writeMultiStatus(w, responses)
}

func (h *CalDAVHandler) reportQuery(w http.ResponseWriter, db *sql.DB, username string, cal *Calendar, rb *reportBody) {
	calHref := "/remote.php/dav/calendars/" + username + "/" + cal.UID + "/"

	startStr := rb.Filter.CompFilter.CompFilter.TimeRange.Start
	endStr := rb.Filter.CompFilter.CompFilter.TimeRange.End

	var events []Event
	if startStr != "" && endStr != "" {
		start := parseCalDAVDate(startStr)
		end := parseCalDAVDate(endStr)
		events, _ = EventsInRangeForCalendar(db, cal.ID, start.Unix(), end.Unix())
	} else {
		events, _ = ListEvents(db, cal.ID)
	}

	var responses []davResponse
	for _, ev := range events {
		responses = append(responses, eventResponseWithData(calHref+ev.UID+".ics", &ev))
	}
	writeMultiStatus(w, responses)
}

func (h *CalDAVHandler) reportSyncCollection(w http.ResponseWriter, db *sql.DB, username string, cal *Calendar, syncToken string) {
	calHref := "/remote.php/dav/calendars/" + username + "/" + cal.UID + "/"

	var sinceToken int64
	if syncToken != "" {
		// Extract numeric token from URI like "http://cog/ns/sync/42".
		if idx := strings.LastIndex(syncToken, "/"); idx >= 0 {
			sinceToken, _ = strconv.ParseInt(syncToken[idx+1:], 10, 64)
		}
	}

	var responses []davResponse

	if sinceToken > 0 {
		// Return only changes since the given token.
		changed, _ := EventsChangedSince(db, cal.ID, sinceToken)
		for _, ev := range changed {
			responses = append(responses, eventResponseWithData(calHref+ev.UID+".ics", &ev))
		}

		deleted, _ := DeletedEventsSince(db, cal.ID, sinceToken)
		for _, uid := range deleted {
			responses = append(responses, davResponse{
				Href:   calHref + uid + ".ics",
				Status: "HTTP/1.1 404 Not Found",
			})
		}
	} else {
		// Full sync: return all events.
		events, _ := ListEvents(db, cal.ID)
		for _, ev := range events {
			responses = append(responses, eventResponseWithData(calHref+ev.UID+".ics", &ev))
		}
	}

	writeSyncMultiStatus(w, responses, "http://cog/ns/sync/"+strconv.FormatInt(cal.SyncToken, 10))
}

func parseCalDAVDate(s string) time.Time {
	if t, err := time.Parse("20060102T150405Z", s); err == nil {
		return t
	}
	if t, err := time.Parse("20060102T150405", s); err == nil {
		return t
	}
	if t, err := time.Parse("20060102", s); err == nil {
		return t
	}
	return time.Time{}
}

// --- PROPPATCH handler ---

func (h *CalDAVHandler) handleProppatch(w http.ResponseWriter, r *http.Request, db *sql.DB, username string, cal *Calendar) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	bodyStr := string(body)

	name := cal.Name
	color := cal.Color
	description := cal.Description

	if n := extractXMLElement(bodyStr, "displayname"); n != "" {
		name = n
	}
	if c := extractXMLElement(bodyStr, "calendar-color"); c != "" {
		color = c
	}
	if d := extractXMLElement(bodyStr, "calendar-description"); d != "" {
		description = d
	}

	if err := UpdateCalendar(db, cal.ID, name, color, description); err != nil {
		log.Printf("caldav: update calendar %d: %v", cal.ID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Return 207 with success for each property.
	calHref := "/remote.php/dav/calendars/" + username + "/" + cal.UID + "/"
	var propNames []string
	if extractXMLElement(bodyStr, "displayname") != "" {
		propNames = append(propNames, "D:displayname")
	}
	if extractXMLElement(bodyStr, "calendar-color") != "" {
		propNames = append(propNames, "ICAL:calendar-color")
	}
	if extractXMLElement(bodyStr, "calendar-description") != "" {
		propNames = append(propNames, "C:calendar-description")
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)
	io.WriteString(w, xmlHeader)
	io.WriteString(w, `<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/" xmlns:ICAL="http://apple.com/ns/ical/">`)
	io.WriteString(w, `<D:response>`)
	fmt.Fprintf(w, `<D:href>%s</D:href>`, escapeXML(calHref))
	io.WriteString(w, `<D:propstat><D:prop>`)
	for _, pn := range propNames {
		fmt.Fprintf(w, `<%s/>`, pn)
	}
	io.WriteString(w, `</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`)
	io.WriteString(w, `</D:response></D:multistatus>`)
}

// --- XML response helpers ---

type davResponse struct {
	Href   string
	Props  []xmlProp
	Status string // if non-empty, used instead of propstat (for 404 responses)
}

type xmlProp struct {
	Name  string
	Value string
	IsRaw bool
}

func rawProp(name, rawXML string) xmlProp {
	return xmlProp{Name: name, Value: rawXML, IsRaw: true}
}

func textProp(name, text string) xmlProp {
	return xmlProp{Name: name, Value: escapeXML(text)}
}

func calResponse(href string, cal *Calendar, username string) davResponse {
	ownerHref := "/remote.php/dav/principals/users/" + username + "/"
	return davResponse{
		Href: href,
		Props: []xmlProp{
			rawProp("D:resourcetype", `<D:collection/><C:calendar/>`),
			textProp("D:displayname", cal.Name),
			textProp("ICAL:calendar-color", cal.Color),
			textProp("CS:getctag", strconv.FormatInt(cal.CTag, 10)),
			textProp("D:getetag", `"`+strconv.FormatInt(cal.CTag, 10)+`"`),
			rawProp("C:supported-calendar-component-set", `<C:comp name="VEVENT"/>`),
			textProp("C:calendar-description", cal.Description),
			textProp("D:sync-token", "http://cog/ns/sync/"+strconv.FormatInt(cal.SyncToken, 10)),
			rawProp("D:current-user-privilege-set",
				`<D:privilege><D:all/></D:privilege>`+
					`<D:privilege><D:read/></D:privilege>`+
					`<D:privilege><D:write/></D:privilege>`+
					`<D:privilege><D:write-content/></D:privilege>`),
			rawProp("D:owner", fmt.Sprintf(`<D:href>%s</D:href>`, ownerHref)),
			rawProp("D:supported-report-set",
				`<D:supported-report><D:report><C:calendar-multiget/></D:report></D:supported-report>`+
					`<D:supported-report><D:report><C:calendar-query/></D:report></D:supported-report>`+
					`<D:supported-report><D:report><D:sync-collection/></D:report></D:supported-report>`),
		},
	}
}

func eventResponse(href string, ev *Event) davResponse {
	return davResponse{
		Href: href,
		Props: []xmlProp{
			textProp("D:getetag", `"`+ev.ETag+`"`),
			textProp("D:getcontenttype", "text/calendar; charset=utf-8; component=VEVENT"),
		},
	}
}

func eventResponseWithData(href string, ev *Event) davResponse {
	return davResponse{
		Href: href,
		Props: []xmlProp{
			textProp("D:getetag", `"`+ev.ETag+`"`),
			textProp("D:getcontenttype", "text/calendar; charset=utf-8; component=VEVENT"),
			rawProp("C:calendar-data", escapeXML(ev.ICSData)),
		},
	}
}

func writeMultiStatus(w http.ResponseWriter, responses []davResponse) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("DAV", "1, 2, 3, calendar-access")
	w.WriteHeader(207)

	io.WriteString(w, xmlHeader)
	io.WriteString(w, `<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/" xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:ICAL="http://apple.com/ns/ical/">`)

	for _, resp := range responses {
		writeResponseElement(w, resp)
	}

	io.WriteString(w, `</D:multistatus>`)
}

func writeSyncMultiStatus(w http.ResponseWriter, responses []davResponse, syncToken string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("DAV", "1, 2, 3, calendar-access")
	w.WriteHeader(207)

	io.WriteString(w, xmlHeader)
	io.WriteString(w, `<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/" xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:ICAL="http://apple.com/ns/ical/">`)

	for _, resp := range responses {
		writeResponseElement(w, resp)
	}

	fmt.Fprintf(w, `<D:sync-token>%s</D:sync-token>`, escapeXML(syncToken))
	io.WriteString(w, `</D:multistatus>`)
}

func writeResponseElement(w io.Writer, resp davResponse) {
	io.WriteString(w, `<D:response>`)
	fmt.Fprintf(w, `<D:href>%s</D:href>`, escapeXML(resp.Href))
	if resp.Status != "" && len(resp.Props) == 0 {
		fmt.Fprintf(w, `<D:status>%s</D:status>`, resp.Status)
	} else {
		io.WriteString(w, `<D:propstat><D:prop>`)
		for _, p := range resp.Props {
			fmt.Fprintf(w, `<%s>%s</%s>`, p.Name, p.Value, p.Name)
		}
		io.WriteString(w, `</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`)
	}
	io.WriteString(w, `</D:response>`)
}

func writeOptions(w http.ResponseWriter) {
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, MKCALENDAR, REPORT")
	w.Header().Set("DAV", "1, 2, 3, calendar-access")
	w.WriteHeader(http.StatusOK)
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

// escapeXML escapes special characters for XML text content.
func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// extractXMLElement extracts text content of a simple XML element by local name.
func extractXMLElement(xmlData, localName string) string {
	lower := strings.ToLower(xmlData)
	target := strings.ToLower(localName)

	idx := -1
	for _, prefix := range []string{":" + target + ">", "<" + target + ">"} {
		if i := strings.Index(lower, prefix); i >= 0 {
			idx = i + len(prefix)
			break
		}
	}
	if idx < 0 {
		return ""
	}

	endIdx := strings.Index(lower[idx:], "</")
	if endIdx < 0 {
		return ""
	}
	return strings.TrimSpace(xmlData[idx : idx+endIdx])
}

// HandleDAVRoot responds to PROPFIND on /remote.php/dav/ with current-user-principal.
func HandleDAVRoot(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cog"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	principal := "/remote.php/dav/principals/users/" + u.Username + "/"
	writeMultiStatus(w, []davResponse{{
		Href: "/remote.php/dav/",
		Props: []xmlProp{
			rawProp("D:resourcetype", `<D:collection/>`),
			rawProp("D:current-user-principal", fmt.Sprintf(`<D:href>%s</D:href>`, principal)),
		},
	}})
}
