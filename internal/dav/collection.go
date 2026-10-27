// Package dav is the CalDAV and CardDAV core: user-level collections, sync,
// permissions and discovery under /remote.php/dav/.
//
// Collections belong to the user and live in the system database. Plugins
// supply the resources inside them through Backend, one backend per set of
// component types, so a calendar collection may hold events from the calendar
// plugin and tasks from the tasks plugin. See DESIGN/dav-storage.md.
package dav

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/clock"
	sqlite3 "github.com/mattn/go-sqlite3"
)

// Home is a user's top-level DAV collection: one for calendars, one for
// address books.
type Home string

const (
	HomeCalendars    Home = "calendars"
	HomeAddressBooks Home = "addressbooks"
)

// Component is an iCalendar or vCard component type a resource holds.
type Component string

const (
	VEVENT   Component = "VEVENT"
	VTODO    Component = "VTODO"
	VJOURNAL Component = "VJOURNAL"
	VCARD    Component = "VCARD"
)

// Home returns the home collections of this component type live in.
func (c Component) Home() Home {
	if c == VCARD {
		return HomeAddressBooks
	}
	return HomeCalendars
}

// ParseComponent returns the component named s, ignoring case.
func ParseComponent(s string) (Component, bool) {
	switch c := Component(strings.ToUpper(s)); c {
	case VEVENT, VTODO, VJOURNAL, VCARD:
		return c, true
	}
	return "", false
}

// CollectionID identifies a collection across every user and database. IDs
// are never reused, so a share or an orphaned row naming a deleted collection
// can never come to mean another one.
type CollectionID int64

func (id CollectionID) String() string { return strconv.FormatInt(int64(id), 10) }

// ShareType is the share resource type for collections; the resource ID is
// the CollectionID.
const ShareType = "dav:collection"

// Kind says where a collection's resources come from.
type Kind string

const (
	// KindOwn collections hold what their owner and sharees write.
	KindOwn Kind = ""
	// KindSubscription calendars are filled from a remote iCalendar feed
	// (Source) and are read-only to every client.
	KindSubscription Kind = "subscription"
	// KindBirthdays is a user's birthday calendar, derived from the
	// birthdays and anniversaries of the contacts in their address books.
	KindBirthdays Kind = "birthdays"
)

// Collection is a calendar or address book.
type Collection struct {
	ID           CollectionID
	OwnerID      string // user ID
	Home         Home
	Kind         Kind
	Source       string // a subscription's feed URL, as the user gave it
	Name         string // path segment, unique per owner and home
	Components   []Component
	DisplayName  string
	Description  string
	Color        string
	SortOrder    int
	Timezone     string // IANA; "" means UTC. Floating times and new events.
	DeadProps    []DeadProp
	PropsVersion int64
	CreatedAt    int64
	UpdatedAt    int64
}

// ReadOnly reports whether Cog fills the collection itself, so no client
// may write to it, its owner included.
func (c *Collection) ReadOnly() bool { return c.Kind != KindOwn }

// Has reports whether the collection holds resources of type c.
func (c *Collection) Has(comp Component) bool { return slices.Contains(c.Components, comp) }

// Title is the display name, or the path name when none is set.
func (c *Collection) Title() string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	return c.Name
}

// DeadProp is a WebDAV property a client set with PROPPATCH or MKCOL that Cog
// does not model. It is kept and returned as sent.
type DeadProp struct {
	Space string `json:"ns"`
	Local string `json:"name"`
	Inner string `json:"xml"` // children, re-encoded with their namespaces
}

// Errors returned by Store and backends.
var (
	ErrNotFound     = errors.New("dav: not found")
	ErrExists       = errors.New("dav: already exists")
	ErrPrecondition = errors.New("dav: precondition failed")
)

// Store keeps collections in the system database.
type Store struct {
	db    *sql.DB
	clock clock.Clock
	// observe hears of collections created, changed and deleted, for
	// WebDAV-Push (Server.EnablePush).
	observe func(*Collection, collectionEvent)
}

func (s *Store) changed(c *Collection, ev collectionEvent) {
	if s.observe != nil {
		s.observe(c, ev)
	}
}

// NewStore returns a collection store over the system database.
func NewStore(db *sql.DB, c clock.Clock) *Store {
	return &Store{db: db, clock: c}
}

// selectCollections reads collections with their subscription's URL; the
// queries add their conditions on c.
const selectCollections = `SELECT c.id, c.owner_user_id, c.home, c.kind, COALESCE(s.url, ''), c.name, c.components,
	c.display_name, c.description, c.color, c.sort_order, c.timezone, c.dead_props, c.props_version,
	c.created_at, c.updated_at
	FROM dav_collections c LEFT JOIN dav_subscriptions s ON s.collection_id = c.id`

func scanCollection(row interface{ Scan(...any) error }) (*Collection, error) {
	var c Collection
	var comps, dead string
	if err := row.Scan(&c.ID, &c.OwnerID, &c.Home, &c.Kind, &c.Source, &c.Name, &comps, &c.DisplayName, &c.Description,
		&c.Color, &c.SortOrder, &c.Timezone, &dead, &c.PropsVersion, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	for s := range strings.SplitSeq(comps, ",") {
		if comp, ok := ParseComponent(s); ok {
			c.Components = append(c.Components, comp)
		}
	}
	if err := json.Unmarshal([]byte(dead), &c.DeadProps); err != nil {
		return nil, fmt.Errorf("collection %d dead properties: %w", c.ID, err)
	}
	return &c, nil
}

func (s *Store) query(q string, args ...any) ([]Collection, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan collection: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// List returns a user's collections in a home, in display order.
func (s *Store) List(ownerID string, home Home) ([]Collection, error) {
	return s.query(selectCollections+` WHERE c.owner_user_id = ? AND c.home = ?
		ORDER BY c.sort_order, c.display_name, c.name`, ownerID, home)
}

// ListWith returns a user's collections that hold any of comps.
func (s *Store) ListWith(ownerID string, comps ...Component) ([]Collection, error) {
	if len(comps) == 0 {
		return nil, nil
	}
	all, err := s.List(ownerID, comps[0].Home())
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(c Collection) bool {
		return !slices.ContainsFunc(comps, c.Has)
	}), nil
}

// Get returns a user's collection by home and name.
func (s *Store) Get(ownerID string, home Home, name string) (*Collection, error) {
	c, err := scanCollection(s.db.QueryRow(selectCollections+`
		WHERE c.owner_user_id = ? AND c.home = ? AND c.name = ?`, ownerID, home, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get collection %s/%s: %w", home, name, err)
	}
	return c, nil
}

// ByID returns a collection by ID.
func (s *Store) ByID(id CollectionID) (*Collection, error) {
	c, err := scanCollection(s.db.QueryRow(selectCollections+` WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get collection %d: %w", id, err)
	}
	return c, nil
}

// ByIDs returns the collections that exist among ids, in no particular order.
func (s *Store) ByIDs(ids []CollectionID) ([]Collection, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return s.query(selectCollections+` WHERE c.id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
}

// OfKind returns a user's collections of a kind.
func (s *Store) OfKind(ownerID string, kind Kind) ([]Collection, error) {
	return s.query(selectCollections+` WHERE c.owner_user_id = ? AND c.kind = ? ORDER BY c.id`, ownerID, kind)
}

func encodeComponents(comps []Component) string {
	s := make([]string, len(comps))
	for i, c := range comps {
		s[i] = string(c)
	}
	return strings.Join(s, ",")
}

func encodeDead(props []DeadProp) (string, error) {
	if props == nil {
		props = []DeadProp{}
	}
	b, err := json.Marshal(props)
	return string(b), err
}

// Create inserts c and sets its ID and timestamps. It returns ErrExists when
// the owner already has a collection of that name in that home.
func (s *Store) Create(c *Collection) error {
	if c.Name == "" || strings.Contains(c.Name, "/") {
		return fmt.Errorf("invalid collection name %q", c.Name)
	}
	if len(c.Components) == 0 {
		return fmt.Errorf("collection %q has no components", c.Name)
	}
	if reserved(c.Home, c.Name) {
		return ErrExists // the inbox and outbox live there
	}
	for _, comp := range c.Components {
		if comp.Home() != c.Home {
			return fmt.Errorf("%s collection %q cannot hold %s", c.Home, c.Name, comp)
		}
	}
	dead, err := encodeDead(c.DeadProps)
	if err != nil {
		return fmt.Errorf("encode dead properties: %w", err)
	}
	now := s.clock().Unix()
	res, err := s.db.Exec(`INSERT INTO dav_collections (owner_user_id, home, kind, name, components, display_name,
		description, color, sort_order, timezone, dead_props, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.OwnerID, c.Home, c.Kind, c.Name, encodeComponents(c.Components), c.DisplayName,
		c.Description, c.Color, c.SortOrder, c.Timezone, dead, now, now)
	if err != nil {
		var se sqlite3.Error
		if errors.As(err, &se) && se.ExtendedCode == sqlite3.ErrConstraintUnique {
			return ErrExists
		}
		return fmt.Errorf("create collection %q: %w", c.Name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("create collection %q: %w", c.Name, err)
	}
	c.ID, c.CreatedAt, c.UpdatedAt = CollectionID(id), now, now
	s.changed(c, collectionCreated)
	return nil
}

// Update saves c's properties and bumps its props version. Name, owner,
// home, kind and components cannot change.
func (s *Store) Update(c *Collection) error {
	dead, err := encodeDead(c.DeadProps)
	if err != nil {
		return fmt.Errorf("encode dead properties: %w", err)
	}
	now := s.clock().Unix()
	err = s.db.QueryRow(`UPDATE dav_collections SET display_name = ?, description = ?, color = ?,
		sort_order = ?, timezone = ?, dead_props = ?, props_version = props_version + 1, updated_at = ?
		WHERE id = ? RETURNING props_version`,
		c.DisplayName, c.Description, c.Color, c.SortOrder, c.Timezone, dead, now, c.ID).Scan(&c.PropsVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("update collection %d: %w", c.ID, err)
	}
	c.UpdatedAt = now
	s.changed(c, collectionUpdated)
	return nil
}

// Delete removes a collection. Its resources live in plugin databases;
// Server.DeleteCollection purges them.
func (s *Store) Delete(id CollectionID) error {
	c := Collection{ID: id}
	err := s.db.QueryRow(`DELETE FROM dav_collections WHERE id = ? RETURNING owner_user_id, home`, id).Scan(&c.OwnerID, &c.Home)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete collection %d: %w", id, err)
	}
	s.changed(&c, collectionDeleted)
	return nil
}

// dropOrphanPushes removes WebDAV-Push subscriptions to collections that no
// longer exist, once they owe nothing: they were told of the deletion.
func (s *Store) dropOrphanPushes() error {
	if _, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE protocol = 'webdav' AND pending = 0
		AND resource LIKE 'collection/%'
		AND CAST(substr(resource, 12) AS INTEGER) NOT IN (SELECT id FROM dav_collections)`); err != nil {
		return fmt.Errorf("drop push subscriptions of deleted collections: %w", err)
	}
	return nil
}

// Slug returns a path name for a collection made from its display name:
// lowercase letters and digits joined by dashes, or fallback when nothing is
// left.
func Slug(display, fallback string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(display) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	if b.Len() == 0 {
		return fallback
	}
	return b.String()
}

// CreateNamed is Create with c.Name as a base: when the owner already has a
// collection of that name in the home, it tries name-2, name-3 and so on.
func (s *Store) CreateNamed(c *Collection) error {
	base := c.Name
	for i := 2; ; i++ {
		err := s.Create(c)
		if !errors.Is(err, ErrExists) || i > 100 {
			return err
		}
		c.Name = fmt.Sprintf("%s-%d", base, i)
	}
}

// Default collections, created for a user the first time a home is empty.
var defaults = []Collection{
	{Home: HomeCalendars, Name: "personal", DisplayName: "Personal", Color: "#0082c9", Components: []Component{VEVENT, VTODO}},
	{Home: HomeAddressBooks, Name: "contacts", DisplayName: "Contacts", Color: "#0082c9", Components: []Component{VCARD}},
}

// GetOrDefault is Get, except that asking for the default collection of an
// owner who has no collections in the home creates it, as listing the home
// would. Clients that guess the default path work on a fresh account.
func (s *Store) GetOrDefault(ownerID string, home Home, name string) (*Collection, error) {
	c, err := s.Get(ownerID, home, name)
	if !errors.Is(err, ErrNotFound) || !slices.ContainsFunc(defaults, func(d Collection) bool {
		return d.Home == home && d.Name == name
	}) {
		return c, err
	}
	if _, err := s.EnsureDefault(ownerID, home); err != nil {
		return nil, err
	}
	return s.Get(ownerID, home, name)
}

// EnsureDefault creates the default collection of home for a user who has
// none, and returns the user's collections in that home.
func (s *Store) EnsureDefault(ownerID string, home Home) ([]Collection, error) {
	cols, err := s.List(ownerID, home)
	if err != nil || len(cols) > 0 {
		return cols, err
	}
	for _, d := range defaults {
		if d.Home != home {
			continue
		}
		d.OwnerID = ownerID
		d.Components = slices.Clone(d.Components)
		if err := s.Create(&d); err != nil && !errors.Is(err, ErrExists) {
			return nil, err
		}
	}
	return s.List(ownerID, home)
}
