package calendar

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/icalsql"
)

// Managed attachments (RFC 8607) are files under the plugin's data
// directory, one per user, named by the SHA-256 of their bytes, as contact
// photos are: the same bytes are stored once however many attachments name
// them. managed_attachments maps each MANAGED-ID to its file; a row lives
// while an ATTACH in the user's events names it.
//
// The files are outside the database, so no transaction covers them. A
// write stores its file while its transaction holds the database's write
// lock, and files are removed under that lock too (collectAttachments), so a
// file is never removed while a write that names it is in flight.

var _ dav.AttachmentStore = (*backend)(nil)

func (p *Plugin) attachmentDir(username string) string {
	return filepath.Join(p.ctx.DataDir(), PluginID, username, "attachments")
}

func attachmentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// storeAttachments stores the files of new managed attachments and their
// rows, inside tx, which must already hold the write lock (it has bumped a
// version).
func storeAttachments(tx *sql.Tx, dir string, atts []dav.NewAttachment, now int64) error {
	for _, a := range atts {
		hash := attachmentHash(a.Data)
		if err := putFile(dir, hash, a.Data); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO managed_attachments (managed_id, hash, size, fmttype, filename, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			a.ManagedID, hash, len(a.Data), a.FmtType, a.Filename, now); err != nil {
			return fmt.Errorf("store attachment %s: %w", a.ManagedID, err)
		}
	}
	return nil
}

// putFile writes data as the file named hash, unless it is there already.
func putFile(dir, hash string, data []byte) error {
	path := filepath.Join(dir, hash)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store attachment: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("store attachment: %w", err)
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("store attachment: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("store attachment: %w", errors.Join(err, f.Close(), os.Remove(f.Name())))
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store attachment: %w", errors.Join(err, os.Remove(f.Name())))
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("store attachment: %w", errors.Join(err, os.Remove(f.Name())))
	}
	return nil
}

// collectAttachments removes the managed attachments no ATTACH names any
// more, and the files no remaining one uses, holding the database's write
// lock throughout. Users without managed attachments pay one read.
func (p *Plugin) collectAttachments(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return err
	}
	var any bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM managed_attachments)`).Scan(&any); err != nil {
		return fmt.Errorf("collect attachments: %w", err)
	}
	if !any {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("collect attachments: %w", err)
	}
	defer tx.Rollback()
	hashes, err := icalsql.Strings(tx, `DELETE FROM managed_attachments WHERE NOT EXISTS (
		SELECT 1 FROM event_attachments a WHERE a.managed_id = managed_attachments.managed_id) RETURNING hash`)
	if err != nil {
		return fmt.Errorf("collect attachments: %w", err)
	}
	dir := p.attachmentDir(username)
	for _, h := range hashes {
		var used bool
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM managed_attachments WHERE hash = ?)`, h).Scan(&used); err != nil {
			return fmt.Errorf("collect attachments: %w", err)
		}
		if used {
			continue
		}
		if err := os.Remove(filepath.Join(dir, h)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove attachment %s: %w", h, err)
		}
	}
	return tx.Commit()
}

// collected runs collectAttachments after a write that may have dropped an
// attachment, logging a failure: the write itself is done, and the next one
// collects what this left.
func (p *Plugin) collected(username string) {
	if err := p.collectAttachments(username); err != nil {
		p.log.Error("remove unused attachments", "user", username, "err", err)
	}
}

// StorageUsage implements plugin.StorageUser: the bytes a user's managed
// attachments take, each file counted once.
func (p *Plugin) StorageUsage(username string) (int64, error) {
	db, err := p.DB(username)
	if err != nil {
		return 0, err
	}
	var n int64
	err = db.QueryRow(`SELECT coalesce(sum(size), 0) FROM (SELECT max(size) AS size FROM managed_attachments GROUP BY hash)`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("attachment usage: %w", err)
	}
	return n, nil
}

// ManagedAttachments implements dav.AttachmentStore.
func (b *backend) ManagedAttachments(owner string, ids []string) (map[string]dav.AttachmentInfo, error) {
	out := map[string]dav.AttachmentInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	db, err := b.p.DB(owner)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT managed_id, size, fmttype, filename FROM managed_attachments
		WHERE managed_id IN (`+icalsql.Placeholders(len(ids))+`)`, icalsql.Anys(ids)...)
	if err != nil {
		return nil, fmt.Errorf("find attachments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a dav.AttachmentInfo
		if err := rows.Scan(&a.ManagedID, &a.Size, &a.FmtType, &a.Filename); err != nil {
			return nil, fmt.Errorf("find attachments: %w", err)
		}
		out[a.ManagedID] = a
	}
	return out, rows.Err()
}

// AttachmentUses implements dav.AttachmentStore: the components naming the
// attachment, with their calendars and attendees, in two queries.
func (b *backend) AttachmentUses(owner, id string) ([]dav.AttachmentUse, error) {
	db, err := b.p.DB(owner)
	if err != nil {
		return nil, err
	}
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM managed_attachments WHERE managed_id = ?)`, id).Scan(&exists); err != nil {
		return nil, fmt.Errorf("find attachment: %w", err)
	}
	if !exists {
		return nil, dav.ErrNotFound
	}
	rows, err := db.Query(`SELECT DISTINCT v.id, e.collection_id FROM event_attachments a
		JOIN vevents v ON v.id = a.component_id JOIN events e ON e.id = v.object_id
		WHERE a.managed_id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("find attachment uses: %w", err)
	}
	var comps []int64
	byComp := map[int64]int{}
	var out []dav.AttachmentUse
	for rows.Next() {
		var cid, coll int64
		if err := rows.Scan(&cid, &coll); err != nil {
			rows.Close()
			return nil, fmt.Errorf("find attachment uses: %w", err)
		}
		byComp[cid] = len(out)
		comps = append(comps, cid)
		out = append(out, dav.AttachmentUse{Collection: dav.CollectionID(coll)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find attachment uses: %w", err)
	}
	if len(comps) == 0 {
		return out, nil
	}
	rows, err = db.Query(`SELECT component_id, address FROM event_attendees WHERE component_id IN (`+icalsql.Placeholders(len(comps))+`)`, icalsql.Anys(comps)...)
	if err != nil {
		return nil, fmt.Errorf("find attachment attendees: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int64
		var addr string
		if err := rows.Scan(&cid, &addr); err != nil {
			return nil, fmt.Errorf("find attachment attendees: %w", err)
		}
		u := &out[byComp[cid]]
		u.Attendees = append(u.Attendees, addr)
	}
	return out, rows.Err()
}

// OpenAttachment implements dav.AttachmentStore.
func (b *backend) OpenAttachment(owner, id string) (*dav.AttachmentInfo, io.ReadSeekCloser, error) {
	db, err := b.p.DB(owner)
	if err != nil {
		return nil, nil, err
	}
	info := dav.AttachmentInfo{ManagedID: id}
	var hash string
	err = db.QueryRow(`SELECT hash, size, fmttype, filename FROM managed_attachments WHERE managed_id = ?`, id).
		Scan(&hash, &info.Size, &info.FmtType, &info.Filename)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, dav.ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("find attachment: %w", err)
	}
	f, err := os.Open(filepath.Join(b.p.attachmentDir(owner), hash))
	if errors.Is(err, fs.ErrNotExist) {
		b.p.log.Error("attachment file missing", "user", owner, "id", id, "hash", hash)
		return nil, nil, dav.ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open attachment: %w", err)
	}
	return &info, f, nil
}
