package contacts

import (
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
)

// CardDAVHandler implements CardDAV for the contacts plugin.
// Mounted at /remote.php/dav/addressbooks/ and also responds on /remote.php/dav/principals/.
type CardDAVHandler struct {
	plugin *Plugin
}

// NewCardDAVHandler creates a new CardDAV handler.
func NewCardDAVHandler(p *Plugin) *CardDAVHandler {
	return &CardDAVHandler{plugin: p}
}

func (h *CardDAVHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cog"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	path := r.URL.Path

	if strings.HasPrefix(path, "/remote.php/dav/addressbooks/") {
		db, err := h.plugin.DB(u.Username)
		if err != nil {
			log.Printf("carddav: db error for %s: %v", u.Username, err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if err := EnsureDefaultAddressBook(db); err != nil {
			log.Printf("carddav: ensure default address book: %v", err)
		}
		h.dispatch(w, r, db, u.Username)
		return
	}

	w.WriteHeader(http.StatusNotFound)
}

// dispatch routes addressbook requests by path depth.
func (h *CardDAVHandler) dispatch(w http.ResponseWriter, r *http.Request, db *sql.DB, username string) {
	rest := strings.TrimPrefix(r.URL.Path, "/remote.php/dav/addressbooks/")
	parts := strings.SplitN(rest, "/", 3) // [user, bookuid?, carduri?]

	switch {
	case len(parts) <= 1 || parts[1] == "":
		h.addressBookHome(w, r, db, username)
	case len(parts) == 2 || parts[2] == "":
		h.addressBookCollection(w, r, db, username, strings.TrimSuffix(parts[1], "/"))
	default:
		cardFile := strings.TrimSuffix(parts[2], "/")
		if cardFile == "" {
			h.addressBookCollection(w, r, db, username, parts[1])
			return
		}
		h.card(w, r, db, username, parts[1], cardFile)
	}
}

// addressBookHome lists all address books for a user.
func (h *CardDAVHandler) addressBookHome(w http.ResponseWriter, r *http.Request, db *sql.DB, username string) {
	switch r.Method {
	case "PROPFIND":
		homeHref := "/remote.php/dav/addressbooks/" + username + "/"
		depth := r.Header.Get("Depth")

		responses := []davResponse{{
			Href: homeHref,
			Props: []xmlProp{
				rawProp("D:resourcetype", `<D:collection/>`),
				textProp("D:displayname", username),
			},
		}}

		if depth != "0" {
			books, _ := ListAddressBooks(db)
			for _, book := range books {
				responses = append(responses, bookResponse(homeHref+book.UID+"/", &book, username))
			}
		}
		writeMultiStatus(w, responses)
	case http.MethodOptions:
		writeOptions(w)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// addressBookCollection handles PROPFIND/MKCOL/DELETE/REPORT/PROPPATCH on a single address book.
func (h *CardDAVHandler) addressBookCollection(w http.ResponseWriter, r *http.Request, db *sql.DB, username, bookUID string) {
	book, err := GetAddressBookByUID(db, bookUID)
	if err != nil {
		if r.Method == "MKCOL" {
			h.mkAddressBook(w, r, db, bookUID)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}

	switch r.Method {
	case "PROPFIND":
		bookHref := "/remote.php/dav/addressbooks/" + username + "/" + bookUID + "/"
		depth := r.Header.Get("Depth")
		responses := []davResponse{bookResponse(bookHref, book, username)}

		if depth != "0" {
			cards, _ := ListCards(db, book.ID)
			for _, c := range cards {
				responses = append(responses, cardResponse(bookHref+c.URI, &c))
			}
		}
		writeMultiStatus(w, responses)
	case "REPORT":
		h.handleReport(w, r, db, username, book)
	case "PROPPATCH":
		h.handleProppatch(w, r, db, username, book)
	case "MKCOL":
		h.mkAddressBook(w, r, db, bookUID)
	case http.MethodDelete:
		if err := DeleteAddressBook(db, book.ID); err != nil {
			log.Printf("carddav: delete address book %d: %v", book.ID, err)
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

func (h *CardDAVHandler) mkAddressBook(w http.ResponseWriter, r *http.Request, db *sql.DB, bookUID string) {
	name := bookUID
	color := "#0082c9"
	description := ""

	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if len(body) > 0 {
		if n := extractXMLElement(string(body), "displayname"); n != "" {
			name = n
		}
		if c := extractXMLElement(string(body), "addressbook-color"); c != "" {
			color = c
		}
		if d := extractXMLElement(string(body), "addressbook-description"); d != "" {
			description = d
		}
	}

	_, err := CreateAddressBook(db, bookUID, name, description, color)
	if err != nil {
		log.Printf("carddav: create address book %s: %v", bookUID, err)
		http.Error(w, "Conflict", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// card handles GET/PUT/DELETE/PROPFIND on a single card resource.
func (h *CardDAVHandler) card(w http.ResponseWriter, r *http.Request, db *sql.DB, username, bookUID, cardURI string) {
	book, err := GetAddressBookByUID(db, bookUID)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Derive UID from URI: strip .vcf suffix.
	cardUID := strings.TrimSuffix(cardURI, ".vcf")

	switch r.Method {
	case http.MethodGet:
		c, err := GetCardByUID(db, book.ID, cardUID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
		w.Header().Set("ETag", `"`+c.ETag+`"`)
		io.WriteString(w, c.VCardData)

	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		vcardData := string(body)

		// If-None-Match: * means "create only".
		if r.Header.Get("If-None-Match") == "*" {
			if _, err := GetCardByUID(db, book.ID, cardUID); err == nil {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
		}

		if ifMatch := r.Header.Get("If-Match"); ifMatch != "" {
			existing, err := GetCardByUID(db, book.ID, cardUID)
			if err == nil {
				existingETag := `"` + existing.ETag + `"`
				if ifMatch != existingETag && ifMatch != existing.ETag {
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
			}
		}

		fields, err := ParseVCard(vcardData)
		if err != nil {
			http.Error(w, "invalid vCard: "+err.Error(), http.StatusBadRequest)
			return
		}

		c, err := UpsertCard(db, book.ID, cardUID, cardURI, vcardData, fields)
		if err != nil {
			log.Printf("carddav: upsert card %s: %v", cardUID, err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("ETag", `"`+c.ETag+`"`)
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		if err := DeleteCard(db, book.ID, cardUID); err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case "PROPFIND":
		c, err := GetCardByUID(db, book.ID, cardUID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		href := "/remote.php/dav/addressbooks/" + username + "/" + bookUID + "/" + c.URI
		writeMultiStatus(w, []davResponse{cardResponse(href, c)})

	case http.MethodOptions:
		writeOptions(w)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// --- REPORT handler ---

type reportBody struct {
	XMLName   xml.Name
	Hrefs     []string `xml:"href"`
	SyncToken string   `xml:"sync-token"`
}

func (h *CardDAVHandler) handleReport(w http.ResponseWriter, r *http.Request, db *sql.DB, username string, book *AddressBook) {
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
	case "addressbook-multiget":
		h.reportMultiget(w, db, username, book, rb.Hrefs)
	case "addressbook-query":
		h.reportQuery(w, db, username, book)
	case "sync-collection":
		h.reportSyncCollection(w, db, username, book, rb.SyncToken)
	default:
		http.Error(w, "unsupported report: "+localName, http.StatusBadRequest)
	}
}

func (h *CardDAVHandler) reportMultiget(w http.ResponseWriter, db *sql.DB, username string, book *AddressBook, hrefs []string) {
	bookHref := "/remote.php/dav/addressbooks/" + username + "/" + book.UID + "/"
	var responses []davResponse

	for _, href := range hrefs {
		href = strings.TrimSpace(href)
		uri := strings.TrimPrefix(href, bookHref)
		if uri == "" {
			continue
		}
		uid := strings.TrimSuffix(uri, ".vcf")

		c, err := GetCardByUID(db, book.ID, uid)
		if err != nil {
			responses = append(responses, davResponse{
				Href:   href,
				Status: "HTTP/1.1 404 Not Found",
			})
			continue
		}
		responses = append(responses, cardResponseWithData(href, c))
	}

	writeMultiStatus(w, responses)
}

func (h *CardDAVHandler) reportQuery(w http.ResponseWriter, db *sql.DB, username string, book *AddressBook) {
	bookHref := "/remote.php/dav/addressbooks/" + username + "/" + book.UID + "/"
	cards, _ := ListCards(db, book.ID)

	var responses []davResponse
	for _, c := range cards {
		responses = append(responses, cardResponseWithData(bookHref+c.URI, &c))
	}
	writeMultiStatus(w, responses)
}

func (h *CardDAVHandler) reportSyncCollection(w http.ResponseWriter, db *sql.DB, username string, book *AddressBook, syncToken string) {
	bookHref := "/remote.php/dav/addressbooks/" + username + "/" + book.UID + "/"

	var sinceToken int64
	if syncToken != "" {
		if idx := strings.LastIndex(syncToken, "/"); idx >= 0 {
			sinceToken, _ = strconv.ParseInt(syncToken[idx+1:], 10, 64)
		}
	}

	var responses []davResponse

	if sinceToken > 0 {
		// Return all cards (clients use etags to diff). Plus deleted cards.
		cards, _ := ListCards(db, book.ID)
		for _, c := range cards {
			responses = append(responses, cardResponseWithData(bookHref+c.URI, &c))
		}

		deleted, _ := DeletedCardsSince(db, book.ID, sinceToken)
		for _, uid := range deleted {
			responses = append(responses, davResponse{
				Href:   bookHref + uid + ".vcf",
				Status: "HTTP/1.1 404 Not Found",
			})
		}
	} else {
		cards, _ := ListCards(db, book.ID)
		for _, c := range cards {
			responses = append(responses, cardResponseWithData(bookHref+c.URI, &c))
		}
	}

	writeSyncMultiStatus(w, responses, "http://cog/ns/sync/"+strconv.FormatInt(book.SyncToken, 10))
}

// --- PROPPATCH handler ---

func (h *CardDAVHandler) handleProppatch(w http.ResponseWriter, r *http.Request, db *sql.DB, username string, book *AddressBook) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	bodyStr := string(body)

	name := book.Name
	color := book.Color
	description := book.Description

	if n := extractXMLElement(bodyStr, "displayname"); n != "" {
		name = n
	}
	if c := extractXMLElement(bodyStr, "addressbook-color"); c != "" {
		color = c
	}
	if d := extractXMLElement(bodyStr, "addressbook-description"); d != "" {
		description = d
	}

	if err := UpdateAddressBook(db, book.ID, name, description, color); err != nil {
		log.Printf("carddav: update address book %d: %v", book.ID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	bookHref := "/remote.php/dav/addressbooks/" + username + "/" + book.UID + "/"
	var propNames []string
	if extractXMLElement(bodyStr, "displayname") != "" {
		propNames = append(propNames, "D:displayname")
	}
	if extractXMLElement(bodyStr, "addressbook-color") != "" {
		propNames = append(propNames, "ICAL:addressbook-color")
	}
	if extractXMLElement(bodyStr, "addressbook-description") != "" {
		propNames = append(propNames, "CR:addressbook-description")
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)
	io.WriteString(w, xmlHeader)
	io.WriteString(w, `<D:multistatus xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:ICAL="http://apple.com/ns/ical/">`)
	io.WriteString(w, `<D:response>`)
	fmt.Fprintf(w, `<D:href>%s</D:href>`, escapeXML(bookHref))
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
	Status string
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

func bookResponse(href string, book *AddressBook, username string) davResponse {
	ownerHref := "/remote.php/dav/principals/users/" + username + "/"
	return davResponse{
		Href: href,
		Props: []xmlProp{
			rawProp("D:resourcetype", `<D:collection/><CR:addressbook xmlns:CR="urn:ietf:params:xml:ns:carddav"/>`),
			textProp("D:displayname", book.Name),
			textProp("CS:getctag", strconv.FormatInt(book.CTag, 10)),
			textProp("D:getetag", `"`+strconv.FormatInt(book.CTag, 10)+`"`),
			textProp("D:sync-token", "http://cog/ns/sync/"+strconv.FormatInt(book.SyncToken, 10)),
			textProp("CR:addressbook-description", book.Description),
			rawProp("D:current-user-privilege-set",
				`<D:privilege><D:all/></D:privilege>`+
					`<D:privilege><D:read/></D:privilege>`+
					`<D:privilege><D:write/></D:privilege>`+
					`<D:privilege><D:write-content/></D:privilege>`),
			rawProp("D:owner", fmt.Sprintf(`<D:href>%s</D:href>`, ownerHref)),
			rawProp("D:supported-report-set",
				`<D:supported-report><D:report><CR:addressbook-multiget xmlns:CR="urn:ietf:params:xml:ns:carddav"/></D:report></D:supported-report>`+
					`<D:supported-report><D:report><CR:addressbook-query xmlns:CR="urn:ietf:params:xml:ns:carddav"/></D:report></D:supported-report>`+
					`<D:supported-report><D:report><D:sync-collection/></D:report></D:supported-report>`),
		},
	}
}

func cardResponse(href string, c *Card) davResponse {
	return davResponse{
		Href: href,
		Props: []xmlProp{
			textProp("D:getetag", `"`+c.ETag+`"`),
			textProp("D:getcontenttype", "text/vcard; charset=utf-8"),
		},
	}
}

func cardResponseWithData(href string, c *Card) davResponse {
	return davResponse{
		Href: href,
		Props: []xmlProp{
			textProp("D:getetag", `"`+c.ETag+`"`),
			textProp("D:getcontenttype", "text/vcard; charset=utf-8"),
			rawProp("CR:address-data", escapeXML(c.VCardData)),
		},
	}
}

func writeMultiStatus(w http.ResponseWriter, responses []davResponse) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("DAV", "1, 2, 3, addressbook")
	w.WriteHeader(207)

	io.WriteString(w, xmlHeader)
	io.WriteString(w, `<D:multistatus xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:CS="http://calendarserver.org/ns/" xmlns:ICAL="http://apple.com/ns/ical/">`)

	for _, resp := range responses {
		writeResponseElement(w, resp)
	}

	io.WriteString(w, `</D:multistatus>`)
}

func writeSyncMultiStatus(w http.ResponseWriter, responses []davResponse, syncToken string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("DAV", "1, 2, 3, addressbook")
	w.WriteHeader(207)

	io.WriteString(w, xmlHeader)
	io.WriteString(w, `<D:multistatus xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:CS="http://calendarserver.org/ns/">`)

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
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, MKCOL, REPORT")
	w.Header().Set("DAV", "1, 2, 3, addressbook")
	w.WriteHeader(http.StatusOK)
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

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

// HandlePrincipalCardDAV adds addressbook-home-set to a principal PROPFIND response.
// This is called from the calendar's principal handler to add CardDAV discovery.
func HandlePrincipalCardDAV(username string) []xmlProp {
	bookHome := "/remote.php/dav/addressbooks/" + username + "/"
	return []xmlProp{
		rawProp("CR:addressbook-home-set", fmt.Sprintf(`<D:href>%s</D:href>`, bookHome)),
	}
}
