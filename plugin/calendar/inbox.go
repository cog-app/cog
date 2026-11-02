package calendar

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/icalsql"
	"git.duckfam.us/jj-software/cog/internal/dav/itip"
	"github.com/google/uuid"
)

// inbox keeps a user's schedule inbox (RFC 6638) in their calendar
// database, beside the events the messages are about, with the same
// versions and tombstones as a calendar (the collection is dav.InboxID).
// Messages are iTIP text Cog wrote and never edits, so they are stored as
// text rather than rows.
type inbox struct {
	dav.Table
	p *Plugin
}

var _ dav.InboxStore = (*inbox)(nil)

func newInbox(p *Plugin) *inbox {
	ib := &inbox{p: p}
	ib.Table = dav.Table{Name: "schedule_inbox", Component: dav.VEVENT, DB: p.DB, Render: renderMessages}
	return ib
}

func renderMessages(q davsql.Querier, _ *dav.Collection, ids []int64) (map[int64][]byte, error) {
	rows, err := q.Query(`SELECT id, data FROM schedule_inbox WHERE id IN (`+icalsql.Placeholders(len(ids))+`)`, icalsql.Anys(ids)...)
	if err != nil {
		return nil, fmt.Errorf("read inbox: %w", err)
	}
	defer rows.Close()
	out := make(map[int64][]byte, len(ids))
	for rows.Next() {
		var id int64
		var data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, fmt.Errorf("read inbox: %w", err)
		}
		out[id] = []byte(data)
	}
	return out, rows.Err()
}

func (ib *inbox) Key() string { return "i" }

func (ib *inbox) Put(string, *dav.Collection, *dav.PutRequest) (*dav.PutResult, error) {
	return nil, errors.New("schedule inbox: messages arrive by delivery only")
}

// Delete removes a message a client has dealt with.
func (ib *inbox) Delete(owner string, _ *dav.Collection, uri string, cond davsql.Conditions) error {
	db, err := ib.p.DB(owner)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	defer tx.Rollback()
	var version int64
	err = tx.QueryRow(`SELECT version FROM schedule_inbox WHERE collection_id = ? AND uri = ?`, dav.InboxID, uri).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return dav.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete message %s: %w", uri, err)
	}
	if cond.Check(&version) != nil {
		return dav.ErrPrecondition
	}
	if _, err := tx.Exec(`DELETE FROM schedule_inbox WHERE collection_id = ? AND uri = ?`, dav.InboxID, uri); err != nil {
		return fmt.Errorf("delete message %s: %w", uri, err)
	}
	if _, err := davsql.Delete(tx, int64(dav.InboxID), uri, ib.p.ctx.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// Deliver stores a message, and drops those delivered before cutoff that no
// client deleted (a user who only uses the web UI never does).
func (ib *inbox) Deliver(owner, uid string, data []byte, cutoff time.Time) error {
	db, err := ib.p.DB(owner)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("deliver message: %w", err)
	}
	defer tx.Rollback()
	now := ib.p.ctx.Now().Unix()
	coll := int64(dav.InboxID)
	var expired []string
	rows, err := tx.Query(`SELECT uri FROM schedule_inbox WHERE collection_id = ? AND created_at < ?`, coll, cutoff.Unix())
	if err != nil {
		return fmt.Errorf("expire messages: %w", err)
	}
	for rows.Next() {
		var uri string
		if err := rows.Scan(&uri); err != nil {
			rows.Close()
			return fmt.Errorf("expire messages: %w", err)
		}
		expired = append(expired, uri)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("expire messages: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("expire messages: %w", err)
	}
	for _, uri := range expired {
		if _, err := tx.Exec(`DELETE FROM schedule_inbox WHERE collection_id = ? AND uri = ?`, coll, uri); err != nil {
			return fmt.Errorf("expire message %s: %w", uri, err)
		}
		if _, err := davsql.Delete(tx, coll, uri, now); err != nil {
			return err
		}
	}
	version, err := davsql.Bump(tx, coll, now)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schedule_inbox (collection_id, uid, uri, version, created_version, size, method, data, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, coll, uid, newMessageURI(), version, version, len(data), messageMethod(data), string(data), now); err != nil {
		return fmt.Errorf("deliver message: %w", err)
	}
	return tx.Commit()
}

// messageMethod reads the METHOD of an iTIP message Cog wrote.
func messageMethod(data []byte) string {
	comps, err := contentline.Parse(bytes.NewReader(data))
	if err != nil || len(comps) == 0 {
		return ""
	}
	if m := comps[0].Prop("METHOD"); m != nil {
		return m.Value
	}
	return ""
}

func newMessageURI() string { return uuid.NewString() + ".ics" }

// LocateUID implements dav.UIDLocator: the user's event with uid in any of
// the calendars, in one query.
func (b *backend) LocateUID(owner string, ids []dav.CollectionID, uid string) (dav.CollectionID, string, error) {
	db, err := b.p.DB(owner)
	if err != nil {
		return 0, "", err
	}
	if len(ids) == 0 {
		return 0, "", dav.ErrNotFound
	}
	var id dav.CollectionID
	var uri string
	err = db.QueryRow(`SELECT collection_id, uri FROM events WHERE uid = ? AND collection_id IN (`+icalsql.Placeholders(len(ids))+`)
		ORDER BY id LIMIT 1`, append([]any{uid}, icalsql.Anys(ids)...)...).Scan(&id, &uri)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", dav.ErrNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("locate %s: %w", uid, err)
	}
	return id, uri, nil
}

// FreeBusy implements dav.FreeBusyQuerier from the events' instances.
func (b *backend) FreeBusy(owner string, c *dav.Collection, self func(string) bool, start, end time.Time) ([]itip.Period, error) {
	db, err := b.p.DB(owner)
	if err != nil {
		return nil, err
	}
	ids, err := icalsql.IDs(db, `SELECT id FROM events WHERE collection_id = ? AND start_utc < ? AND (range_end_utc IS NULL OR range_end_utc >= ?)`,
		c.ID, end.Unix(), start.Unix())
	if err != nil {
		return nil, err
	}
	objs, err := tables.Load(db, ids)
	if err != nil {
		return nil, err
	}
	var out []itip.Period
	for _, id := range ids {
		o := objs[id]
		ps, err := itip.BusyTime(o, ical.ZonesFor(o, zoneOf(c)), self, start, end)
		if err != nil {
			// A recurrence Cog cannot expand blocks no time, as it shows
			// none in the calendar.
			b.p.log.Warn("free/busy: expand event", "uid", o.UID, "err", err)
			continue
		}
		out = append(out, ps...)
	}
	return out, nil
}
