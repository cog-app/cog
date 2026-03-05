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
)

var ErrNotFound = errors.New("volume not found")
var ErrInUse = errors.New("volume is referenced by mounts")

type Volume struct {
	ID          string
	Name        string
	Description string
	URI         string
	CreatedBy   string
	CreatedAt   time.Time
	OwnerID     *string
	Config      string
	OwnerType   string // "user" (default) or "group"
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
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Create(name, description, uri, createdBy string, ownerID *string, config string) (*Volume, error) {
	return s.CreateWithOwnerType(name, description, uri, createdBy, ownerID, "user", config)
}

// CreateWithOwnerType creates a volume with an explicit owner type ("user" or "group").
func (s *Store) CreateWithOwnerType(name, description, uri, createdBy string, ownerID *string, ownerType, config string) (*Volume, error) {
	// Validate URI.
	if _, err := ParseURI(uri); err != nil {
		return nil, err
	}
	if config == "" {
		config = "{}"
	}
	if ownerType == "" {
		ownerType = "user"
	}

	id, err := generateID()
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	_, err = s.db.Exec(
		`INSERT INTO volumes (id, name, description, uri, created_by, created_at, owner_id, config, owner_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, description, uri, createdBy, now, ownerID, config, ownerType,
	)
	if err != nil {
		return nil, fmt.Errorf("insert volume: %w", err)
	}

	return &Volume{
		ID:          id,
		Name:        name,
		Description: description,
		URI:         uri,
		CreatedBy:   createdBy,
		CreatedAt:   time.Unix(now, 0),
		OwnerID:     ownerID,
		Config:      config,
		OwnerType:   ownerType,
	}, nil
}

func (s *Store) GetByID(id string) (*Volume, error) {
	var v Volume
	var createdAt int64
	err := s.db.QueryRow(
		`SELECT id, name, description, uri, created_by, created_at, owner_id, config, owner_type FROM volumes WHERE id = ?`, id,
	).Scan(&v.ID, &v.Name, &v.Description, &v.URI, &v.CreatedBy, &createdAt, &v.OwnerID, &v.Config, &v.OwnerType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get volume: %w", err)
	}
	v.CreatedAt = time.Unix(createdAt, 0)
	return &v, nil
}

func (s *Store) GetByName(name string) (*Volume, error) {
	var v Volume
	var createdAt int64
	err := s.db.QueryRow(
		`SELECT id, name, description, uri, created_by, created_at, owner_id, config, owner_type FROM volumes WHERE name = ?`, name,
	).Scan(&v.ID, &v.Name, &v.Description, &v.URI, &v.CreatedBy, &createdAt, &v.OwnerID, &v.Config, &v.OwnerType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get volume by name: %w", err)
	}
	v.CreatedAt = time.Unix(createdAt, 0)
	return &v, nil
}

func (s *Store) List() iter.Seq2[Volume, error] {
	return func(yield func(Volume, error) bool) {
		rows, err := s.db.Query(
			`SELECT id, name, description, uri, created_by, created_at, owner_id, config, owner_type FROM volumes ORDER BY name`,
		)
		if err != nil {
			yield(Volume{}, fmt.Errorf("list volumes: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var v Volume
			var createdAt int64
			if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.URI, &v.CreatedBy, &createdAt, &v.OwnerID, &v.Config, &v.OwnerType); err != nil {
				yield(Volume{}, fmt.Errorf("scan volume: %w", err))
				return
			}
			v.CreatedAt = time.Unix(createdAt, 0)
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
		query := `SELECT id, name, description, uri, created_by, created_at, owner_id, config, owner_type
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
			var v Volume
			var createdAt int64
			if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.URI, &v.CreatedBy, &createdAt, &v.OwnerID, &v.Config, &v.OwnerType); err != nil {
				yield(Volume{}, fmt.Errorf("scan volume: %w", err))
				return
			}
			v.CreatedAt = time.Unix(createdAt, 0)
			if !yield(v, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Volume{}, err)
		}
	}
}

func (s *Store) Update(id, name, description, uri string) error {
	if _, err := ParseURI(uri); err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE volumes SET name = ?, description = ?, uri = ? WHERE id = ?`,
		name, description, uri, id,
	)
	if err != nil {
		return fmt.Errorf("update volume: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
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
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
