// Package volume provides CRUD operations for storage volumes.
package volume

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

var ErrNotFound = errors.New("volume not found")
var ErrInUse = errors.New("volume is referenced by mounts")

// DefaultVolumeName is the well-known name of the system default volume.
const DefaultVolumeName = "default"

type Volume struct {
	ID          string
	Name        string
	Description string
	URI         string
	CreatedAt   time.Time
	OwnerUserID *string
	Config      string
	Kind        Kind
}

// Kind says what a share of a volume lets its audience do.
type Kind string

const (
	// Raw: mount the volume, or a part of it, as the share allows.
	Raw Kind = "raw"
	// Storage: make areas of their own in it, each at
	// <username>/<name>, and see nothing else of it.
	Storage Kind = "storage"
)

// ParseKind returns the kind named s.
func ParseKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case Raw, Storage:
		return k, nil
	}
	return "", fmt.Errorf("unknown volume kind %q", s)
}

// volumeColumns are the columns scanVolume reads, in order.
const volumeColumns = `id, name, description, uri, created_at, owner_id, config, kind`

type scanner interface{ Scan(dest ...any) error }

func scanVolume(row scanner) (Volume, error) {
	var v Volume
	var createdAt int64
	if err := row.Scan(&v.ID, &v.Name, &v.Description, &v.URI, &createdAt, &v.OwnerUserID, &v.Config, &v.Kind); err != nil {
		return Volume{}, err
	}
	v.CreatedAt = time.Unix(createdAt, 0)
	return v, nil
}

// IsDefault returns true if this is the system default volume.
func (v *Volume) IsDefault() bool { return v.Name == DefaultVolumeName }

// Scheme returns the URI scheme ("file", "webdav", "mem") or "" on parse error.
func (v *Volume) Scheme() string {
	p, err := ParseURI(v.URI)
	if err != nil {
		return ""
	}
	return p.Scheme
}

// URIPath returns the path portion of the URI (host+path) or "" on parse error.
func (v *Volume) URIPath() string {
	p, err := ParseURI(v.URI)
	if err != nil {
		return ""
	}
	return p.Path
}

// ParsedURI represents a parsed volume URI.
type ParsedURI struct {
	Scheme string // "file", "mem", "webdav"
	Path   string
}

// ParseURI parses a volume URI string (e.g. "file:///data", "mem://", "webdav://host/path").
func ParseURI(raw string) (*ParsedURI, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse volume URI: %w", err)
	}
	switch u.Scheme {
	case "file", "mem", "webdav":
	default:
		return nil, fmt.Errorf("unsupported volume URI scheme: %q", u.Scheme)
	}
	return &ParsedURI{Scheme: u.Scheme, Path: u.Host + u.Path}, nil
}

// ResolvePath returns the concrete filesystem path for a subpath within this volume.
func (p *ParsedURI) ResolvePath(subpath string) (string, error) {
	if p.Scheme == "mem" {
		return "", fmt.Errorf("mem:// volumes do not have filesystem paths")
	}
	return filepath.Join(p.Path, subpath), nil
}

// EnsureDir creates the directory for the given subpath within this volume.
func (p *ParsedURI) EnsureDir(subpath string) error {
	if p.Scheme == "mem" {
		return nil
	}
	dir, err := p.ResolvePath(subpath)
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0700)
}

type Store struct {
	db    *sql.DB
	clock clock.Clock
	// Changed runs after a volume is changed or removed, for whoever keeps
	// something built from the volumes.
	Changed []func()
}

func (s *Store) changed() {
	for _, f := range s.Changed {
		f()
	}
}

func NewStore(db *sql.DB, c clock.Clock) *Store {
	return &Store{db: db, clock: c}
}

func (s *Store) Create(name, description, uri string, ownerUserID *string, config string) (*Volume, error) {
	// Validate URI.
	if _, err := ParseURI(uri); err != nil {
		return nil, err
	}
	if config == "" {
		config = "{}"
	}

	id, err := generateID()
	if err != nil {
		return nil, err
	}

	now := s.clock().Unix()
	_, err = s.db.Exec(
		`INSERT INTO volumes (id, name, description, uri, created_at, owner_id, config) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, name, description, uri, now, ownerUserID, config,
	)
	if err != nil {
		return nil, fmt.Errorf("insert volume: %w", err)
	}

	return &Volume{
		ID:          id,
		Name:        name,
		Description: description,
		URI:         uri,
		CreatedAt:   time.Unix(now, 0),
		OwnerUserID: ownerUserID,
		Config:      config,
		Kind:        Raw,
	}, nil
}

func (s *Store) GetByID(id string) (*Volume, error) {
	v, err := scanVolume(s.db.QueryRow(`SELECT `+volumeColumns+` FROM volumes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get volume: %w", err)
	}
	return &v, nil
}

func (s *Store) GetByName(name string) (*Volume, error) {
	v, err := scanVolume(s.db.QueryRow(`SELECT `+volumeColumns+` FROM volumes WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get volume by name: %w", err)
	}
	return &v, nil
}

func (s *Store) List() iter.Seq2[Volume, error] {
	return func(yield func(Volume, error) bool) {
		rows, err := s.db.Query(`SELECT ` + volumeColumns + ` FROM volumes ORDER BY name`)
		if err != nil {
			yield(Volume{}, fmt.Errorf("list volumes: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVolume(rows)
			if err != nil {
				yield(Volume{}, fmt.Errorf("scan volume: %w", err))
				return
			}
			if !yield(v, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Volume{}, err)
		}
	}
}

// ListAccessible returns volumes accessible to a user: system volumes (owner_id IS NULL),
// volumes owned by the user, and volumes whose IDs are in sharedVolumeIDs.
func (s *Store) ListAccessible(userID string, sharedVolumeIDs []string) iter.Seq2[Volume, error] {
	return func(yield func(Volume, error) bool) {
		// Build query with placeholders for shared IDs.
		query := `SELECT ` + volumeColumns + `
			FROM volumes WHERE owner_id IS NULL OR owner_id = ?`
		args := []any{userID}

		if len(sharedVolumeIDs) > 0 {
			placeholders := make([]string, len(sharedVolumeIDs))
			for i, id := range sharedVolumeIDs {
				placeholders[i] = "?"
				args = append(args, id)
			}
			query += " OR id IN (" + strings.Join(placeholders, ",") + ")"
		}
		query += " ORDER BY name"

		rows, err := s.db.Query(query, args...)
		if err != nil {
			yield(Volume{}, fmt.Errorf("list accessible volumes: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVolume(rows)
			if err != nil {
				yield(Volume{}, fmt.Errorf("scan volume: %w", err))
				return
			}
			if !yield(v, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Volume{}, err)
		}
	}
}

func (s *Store) Update(id, name, description, uri, config string) error {
	if _, err := ParseURI(uri); err != nil {
		return err
	}
	if config == "" {
		config = "{}"
	}
	res, err := s.db.Exec(
		`UPDATE volumes SET name = ?, description = ?, uri = ?, config = ? WHERE id = ?`,
		name, description, uri, config, id,
	)
	if err != nil {
		return fmt.Errorf("update volume: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	s.changed()
	return nil
}

// SetKind changes what the volume's shares let their audience do, and
// converts the mounts of it to match, in one transaction: to Storage, each
// mount of it by someone who may not see it whole (not an admin, not its
// owner) becomes their area, its files left where they are; to Raw, areas
// become plain mounts of the same place.
func (s *Store) SetKind(id string, kind Kind) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE volumes SET kind = ? WHERE id = ?`, kind, id)
	if err != nil {
		return fmt.Errorf("set volume kind: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	switch kind {
	case Storage:
		_, err = tx.Exec(`UPDATE mounts SET kind = 'area', read_only = 0
			WHERE volume_id = ?1 AND kind = 'volume' AND user_path != '/'
			  AND user_id NOT IN (SELECT id FROM users WHERE is_admin = 1)
			  AND user_id IS NOT (SELECT owner_id FROM volumes WHERE id = ?1)`, id)
	case Raw:
		_, err = tx.Exec(`UPDATE mounts SET kind = 'volume' WHERE volume_id = ? AND kind = 'area'`, id)
	}
	if err != nil {
		return fmt.Errorf("convert mounts of volume %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed()
	return nil
}

func (s *Store) Delete(id string) error {
	// Check if mounts reference this volume.
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM mounts WHERE volume_id = ?`, id).Scan(&count); err != nil {
		return fmt.Errorf("check volume mounts: %w", err)
	}
	if count > 0 {
		return ErrInUse
	}

	res, err := s.db.Exec(`DELETE FROM volumes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete volume: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	s.changed()
	return nil
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
