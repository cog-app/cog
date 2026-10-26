package contacts

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
)

// Cards are stored as rows (DESIGN/dav-storage.md section 4.4): one in
// cards for the card and its single-valued properties, and one per value in
// the child tables. The vCard text is generated from them on read.
//
// The rows live in this plugin rather than in a shared package beside
// internal/dav/icalsql: icalsql exists because two plugins store calendar
// components, and only this one stores cards. Other plugins (the birthday
// calendar) read contacts through the plugin.

// valueTables are the child tables holding a list of vcard.Value, by the
// card field each holds.
var valueTables = []struct {
	name  string
	field func(*vcard.Card) *[]vcard.Value
}{
	{"card_emails", func(c *vcard.Card) *[]vcard.Value { return &c.Emails }},
	{"card_phones", func(c *vcard.Card) *[]vcard.Value { return &c.Phones }},
	{"card_urls", func(c *vcard.Card) *[]vcard.Value { return &c.URLs }},
	{"card_impp", func(c *vcard.Card) *[]vcard.Value { return &c.IMPPs }},
	{"card_related", func(c *vcard.Card) *[]vcard.Value { return &c.Related }},
}

// childTables are every table keyed by card_id.
var childTables = []string{"card_emails", "card_phones", "card_urls", "card_impp", "card_related", "card_addresses", "card_members"}

// listColumn writes a list of values as one column: comma-separated, with
// commas and backslashes inside a value escaped. It reads like the vCard
// text, without the escaping of semicolons and line breaks.
func listColumn(vals []string) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = strings.NewReplacer(`\`, `\\`, `,`, `\,`).Replace(v)
	}
	return strings.Join(out, ",")
}

// listOf reads a listColumn; "" is the empty list.
func listOf(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		case c == ',':
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	return append(out, b.String())
}

// jsonColumn writes v as JSON, or NULL for a nil slice.
func jsonColumn[T any](v []T) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// paramsColumn writes parameters as JSON, '[]' for none.
func paramsColumn(ps []contentline.Param) (string, error) {
	if len(ps) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(ps)
	return string(b), err
}

func typesColumn(ts []string) (string, error) {
	if len(ts) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(ts)
	return string(b), err
}

// unmarshalList reads a JSON array, keeping an empty one as nil, which is
// how the codec writes no values.
func unmarshalList[T any](s string, v *[]T) error {
	if s == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(s), v); err != nil {
		return err
	}
	if len(*v) == 0 {
		*v = nil
	}
	return nil
}

// renderCard writes a card as the text Cog serves.
func renderCard(c *vcard.Card) ([]byte, error) {
	var b bytes.Buffer
	if err := contentline.Write(&b, []*contentline.Component{c.Encode()}); err != nil {
		return nil, fmt.Errorf("write card %s: %w", c.UID, err)
	}
	return b.Bytes(), nil
}

// --- Loading ---

const loadChunk = 500

// loadCards reads cards by row ID, in a fixed number of queries per chunk of
// IDs, with their photos; with ph nil, an inline photo has no Data.
func loadCards(q davsql.Querier, ph *photoStore, ids []int64) (map[int64]*vcard.Card, error) {
	out := make(map[int64]*vcard.Card, len(ids))
	for chunk := range slices.Chunk(ids, loadChunk) {
		if err := loadChunkOf(q, ph, chunk, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func placeholders(n int) string { return "?" + strings.Repeat(",?", n-1) }

func anys(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func loadChunkOf(q davsql.Querier, ph *photoStore, ids []int64, out map[int64]*vcard.Card) error {
	in := placeholders(len(ids))
	args := anys(ids)
	rows, err := q.Query(`SELECT id, uid, vcard_version, kind, fn, n_family, n_given, n_additional, n_prefix, n_suffix,
			nickname, org, org_units, title, role, note, bday, anniversary, gender, rev, categories,
			photo_hash, photo_uri, photo_type, photo_encoding, photo_params, leftovers
		FROM cards WHERE id IN (`+in+`)`, args...)
	if err != nil {
		return fmt.Errorf("load cards: %w", err)
	}
	type photoRef struct {
		card *vcard.Card
		hash string
	}
	var photos []photoRef
	for rows.Next() {
		var id int64
		var c vcard.Card
		var n [5]sql.NullString
		var nickname, orgUnits, categories sql.NullString
		var org, hash, photoURI, photoType, photoEnc, photoParams, left string
		if err := rows.Scan(&id, &c.UID, &c.Version, &c.Kind, &c.FN, &n[0], &n[1], &n[2], &n[3], &n[4],
			&nickname, &org, &orgUnits, &c.Title, &c.Role, &c.Note, &c.Birthday, &c.Anniversary, &c.Gender, &c.Rev, &categories,
			&hash, &photoURI, &photoType, &photoEnc, &photoParams, &left); err != nil {
			rows.Close()
			return fmt.Errorf("scan card: %w", err)
		}
		if n[0].Valid {
			c.N = &vcard.Name{
				Family: listOf(n[0].String), Given: listOf(n[1].String), Additional: listOf(n[2].String),
				Prefix: listOf(n[3].String), Suffix: listOf(n[4].String),
			}
		}
		if orgUnits.Valid {
			var units []string
			if err := json.Unmarshal([]byte(orgUnits.String), &units); err != nil {
				rows.Close()
				return fmt.Errorf("card %d org: %w", id, err)
			}
			c.Org = append([]string{org}, units...)
		}
		for _, f := range []struct {
			col sql.NullString
			dst *[]string
		}{{nickname, &c.Nicknames}, {categories, &c.Categories}} {
			if !f.col.Valid {
				continue
			}
			if err := json.Unmarshal([]byte(f.col.String), f.dst); err != nil {
				rows.Close()
				return fmt.Errorf("card %d: %w", id, err)
			}
		}
		if hash != "" {
			c.Photo = &vcard.Photo{URI: photoURI, Type: photoType, Encoding: photoEnc}
			if err := unmarshalList(photoParams, &c.Photo.Params); err != nil {
				rows.Close()
				return fmt.Errorf("card %d photo: %w", id, err)
			}
			if photoURI == "" {
				photos = append(photos, photoRef{&c, hash})
			}
		}
		if err := json.Unmarshal([]byte(left), &c.Leftovers); err != nil {
			rows.Close()
			return fmt.Errorf("card %d leftovers: %w", id, err)
		}
		out[id] = &c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load cards: %w", err)
	}
	for _, p := range photos {
		if ph == nil {
			break
		}
		data, err := ph.read(p.hash)
		if err != nil {
			return err
		}
		if data == nil {
			p.card.Photo = nil
			continue
		}
		p.card.Photo.Data = data
	}

	scanMeta := func(m *vcard.Meta, types, params string) error {
		if err := unmarshalList(types, &m.Types); err != nil {
			return err
		}
		return unmarshalList(params, &m.Params)
	}
	for _, t := range valueTables {
		rows, err := q.Query(`SELECT card_id, value, types, pref, group_name, label, params FROM `+t.name+`
			WHERE card_id IN (`+in+`) ORDER BY card_id, position`, args...)
		if err != nil {
			return fmt.Errorf("load %s: %w", t.name, err)
		}
		for rows.Next() {
			var id int64
			var v vcard.Value
			var types, params string
			if err := rows.Scan(&id, &v.Value, &types, &v.Pref, &v.Group, &v.Label, &params); err != nil {
				rows.Close()
				return fmt.Errorf("scan %s: %w", t.name, err)
			}
			if err := scanMeta(&v.Meta, types, params); err != nil {
				rows.Close()
				return fmt.Errorf("%s of card %d: %w", t.name, id, err)
			}
			if c := out[id]; c != nil {
				list := t.field(c)
				*list = append(*list, v)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("load %s: %w", t.name, err)
		}
	}

	rows, err = q.Query(`SELECT card_id, pobox, ext, street, locality, region, code, country, types, pref, group_name, label, params
		FROM card_addresses WHERE card_id IN (`+in+`) ORDER BY card_id, position`, args...)
	if err != nil {
		return fmt.Errorf("load addresses: %w", err)
	}
	for rows.Next() {
		var id int64
		var a vcard.Address
		var comps [7]string
		var types, params string
		if err := rows.Scan(&id, &comps[0], &comps[1], &comps[2], &comps[3], &comps[4], &comps[5], &comps[6],
			&types, &a.Pref, &a.Group, &a.Label, &params); err != nil {
			rows.Close()
			return fmt.Errorf("scan address: %w", err)
		}
		a.POBox, a.Ext, a.Street, a.Locality = listOf(comps[0]), listOf(comps[1]), listOf(comps[2]), listOf(comps[3])
		a.Region, a.Code, a.Country = listOf(comps[4]), listOf(comps[5]), listOf(comps[6])
		if err := scanMeta(&a.Meta, types, params); err != nil {
			rows.Close()
			return fmt.Errorf("address of card %d: %w", id, err)
		}
		if c := out[id]; c != nil {
			c.Addresses = append(c.Addresses, a)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load addresses: %w", err)
	}

	rows, err = q.Query(`SELECT card_id, uri, params FROM card_members WHERE card_id IN (`+in+`) ORDER BY card_id, position`, args...)
	if err != nil {
		return fmt.Errorf("load members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var m vcard.Member
		var params string
		if err := rows.Scan(&id, &m.URI, &params); err != nil {
			return fmt.Errorf("scan member: %w", err)
		}
		if err := unmarshalList(params, &m.Params); err != nil {
			return fmt.Errorf("member of card %d: %w", id, err)
		}
		if c := out[id]; c != nil {
			c.Members = append(c.Members, m)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load members: %w", err)
	}
	return nil
}

// cardIDs returns the row IDs of a collection's cards, by URI.
func cardIDs(q davsql.Querier, collID dav.CollectionID) ([]int64, error) {
	rows, err := q.Query(`SELECT id FROM cards WHERE collection_id = ? ORDER BY uri`, collID)
	if err != nil {
		return nil, fmt.Errorf("list cards: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list cards: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// renderIDs generates the text of stored cards, for dav.Table.Render.
func renderIDs(q davsql.Querier, ph *photoStore, ids []int64) (map[int64][]byte, error) {
	cards, err := loadCards(q, ph, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]byte, len(cards))
	for id, c := range cards {
		if out[id], err = renderCard(c); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// --- Writes ---
//
// Every write bumps the collection's version in the same transaction
// (davsql), whether it comes from DAV or the web UI, so sync tokens and ETags
// always reflect it.

// storedCard reports a written card.
type storedCard struct {
	ID      int64
	Version int64
	Created bool
	Data    []byte // the text served for this version
	// Garbage names the photo the card used before, when it no longer
	// does; its file goes once nothing else uses it.
	Garbage string
}

// storeCard writes c at uri in a collection inside tx, after checking cond,
// with its photo in ph. It returns dav.ErrPrecondition when cond fails and a
// *dav.UIDConflict when another card in the collection has c's UID. Cog's
// own columns (is_starred) are kept.
func storeCard(tx *sql.Tx, ph *photoStore, collID int64, uri string, c *vcard.Card, cond davsql.Conditions, now int64) (*storedCard, error) {
	var cur *int64
	var curVersion int64
	var oldPhoto string
	switch err := tx.QueryRow(`SELECT version, photo_hash FROM cards WHERE collection_id = ? AND uri = ?`, collID, uri).Scan(&curVersion, &oldPhoto); {
	case err == nil:
		cur = &curVersion
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("read card %s: %w", uri, err)
	}
	if err := cond.Check(cur); err != nil {
		return nil, dav.ErrPrecondition
	}
	var other string
	switch err := tx.QueryRow(`SELECT uri FROM cards WHERE collection_id = ? AND uid = ? AND uri != ?`, collID, c.UID, uri).Scan(&other); {
	case err == nil:
		return nil, &dav.UIDConflict{URI: other}
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("check UID %s: %w", c.UID, err)
	}

	// The stored card must read back as it is served.
	c.AssignGroups()
	data, err := renderCard(c)
	if err != nil {
		return nil, err
	}
	var hash, photoURI, photoType, photoEnc string
	photoParams := "[]"
	if c.Photo != nil {
		hash = hashOf(c.Photo.Data)
		if c.Photo.URI != "" {
			hash, photoURI = urlHash(c.Photo.URI), c.Photo.URI
		}
		photoType, photoEnc = c.Photo.Type, c.Photo.Encoding
		if photoParams, err = paramsColumn(c.Photo.Params); err != nil {
			return nil, err
		}
	}
	var n [5]any
	if c.N != nil {
		for i, comp := range [][]string{c.N.Family, c.N.Given, c.N.Additional, c.N.Prefix, c.N.Suffix} {
			n[i] = listColumn(comp)
		}
	}
	var org string
	var orgUnits any
	if c.Org != nil {
		org = c.Org[0]
		if orgUnits, err = jsonColumn(append([]string{}, c.Org[1:]...)); err != nil {
			return nil, err
		}
	}
	nickname, err := jsonColumn(c.Nicknames)
	if err != nil {
		return nil, err
	}
	categories, err := jsonColumn(c.Categories)
	if err != nil {
		return nil, err
	}
	left, err := json.Marshal(c.Leftovers)
	if err != nil {
		return nil, err
	}

	version, err := davsql.Bump(tx, collID)
	if err != nil {
		return nil, err
	}
	// The photo file is stored only now that the transaction holds the
	// write lock, which collect takes to remove files (Store.collect).
	if c.Photo != nil && c.Photo.URI == "" {
		if err := ph.put(hash, c.Photo.Data); err != nil {
			return nil, err
		}
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO cards (collection_id, uid, uri, version, size, vcard_version, kind, fn,
			n_family, n_given, n_additional, n_prefix, n_suffix, nickname, org, org_units, title, role, note,
			bday, anniversary, gender, rev, categories, photo_hash, photo_uri, photo_type, photo_encoding, photo_params,
			leftovers, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(collection_id, uri) DO UPDATE SET
			uid = excluded.uid, version = excluded.version, size = excluded.size,
			vcard_version = excluded.vcard_version, kind = excluded.kind, fn = excluded.fn,
			n_family = excluded.n_family, n_given = excluded.n_given, n_additional = excluded.n_additional,
			n_prefix = excluded.n_prefix, n_suffix = excluded.n_suffix, nickname = excluded.nickname,
			org = excluded.org, org_units = excluded.org_units, title = excluded.title, role = excluded.role,
			note = excluded.note, bday = excluded.bday, anniversary = excluded.anniversary, gender = excluded.gender,
			rev = excluded.rev, categories = excluded.categories, photo_hash = excluded.photo_hash, photo_uri = excluded.photo_uri,
			photo_type = excluded.photo_type, photo_encoding = excluded.photo_encoding,
			photo_params = excluded.photo_params, leftovers = excluded.leftovers, updated_at = excluded.updated_at
		RETURNING id`,
		collID, c.UID, uri, version, len(data), c.Version, c.Kind, c.FN,
		n[0], n[1], n[2], n[3], n[4], nickname, org, orgUnits, c.Title, c.Role, c.Note,
		c.Birthday, c.Anniversary, c.Gender, c.Rev, categories, hash, photoURI, photoType, photoEnc, photoParams,
		string(left), now, now).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("store card %s: %w", uri, err)
	}
	if cur != nil {
		for _, t := range childTables {
			if _, err := tx.Exec(`DELETE FROM `+t+` WHERE card_id = ?`, id); err != nil {
				return nil, fmt.Errorf("replace card %s: %w", uri, err)
			}
		}
	}
	if err := insertChildren(tx, id, c); err != nil {
		return nil, fmt.Errorf("store card %s: %w", uri, err)
	}
	if err := davsql.Revive(tx, collID, uri); err != nil {
		return nil, err
	}
	st := &storedCard{ID: id, Version: version, Created: cur == nil, Data: data}
	if oldPhoto != hash {
		st.Garbage = oldPhoto
	}
	return st, nil
}

func insertChildren(tx *sql.Tx, id int64, c *vcard.Card) error {
	for _, t := range valueTables {
		for i, v := range *t.field(c) {
			types, err := typesColumn(v.Types)
			if err != nil {
				return err
			}
			params, err := paramsColumn(v.Params)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO `+t.name+` (card_id, position, value, types, pref, group_name, label, params)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, i, v.Value, types, v.Pref, v.Group, v.Label, params); err != nil {
				return fmt.Errorf("insert %s: %w", t.name, err)
			}
		}
	}
	for i, a := range c.Addresses {
		types, err := typesColumn(a.Types)
		if err != nil {
			return err
		}
		params, err := paramsColumn(a.Params)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO card_addresses (card_id, position, pobox, ext, street, locality, region, code, country,
				types, pref, group_name, label, params) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, i, listColumn(a.POBox), listColumn(a.Ext), listColumn(a.Street), listColumn(a.Locality),
			listColumn(a.Region), listColumn(a.Code), listColumn(a.Country), types, a.Pref, a.Group, a.Label, params); err != nil {
			return fmt.Errorf("insert address: %w", err)
		}
	}
	for i, m := range c.Members {
		params, err := paramsColumn(m.Params)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO card_members (card_id, position, uri, params) VALUES (?, ?, ?, ?)`, id, i, m.URI, params); err != nil {
			return fmt.Errorf("insert member: %w", err)
		}
	}
	return nil
}

// deleteCard removes the card at uri inside tx and leaves a tombstone. It
// returns the photo the card used, whose file may now be garbage.
func deleteCard(tx *sql.Tx, collID int64, uri string, cond davsql.Conditions, now int64) (string, error) {
	var version int64
	var photo string
	err := tx.QueryRow(`SELECT version, photo_hash FROM cards WHERE collection_id = ? AND uri = ?`, collID, uri).Scan(&version, &photo)
	if errors.Is(err, sql.ErrNoRows) {
		return "", dav.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read card %s: %w", uri, err)
	}
	if err := cond.Check(&version); err != nil {
		return "", dav.ErrPrecondition
	}
	if _, err := tx.Exec(`DELETE FROM cards WHERE collection_id = ? AND uri = ?`, collID, uri); err != nil {
		return "", fmt.Errorf("delete card %s: %w", uri, err)
	}
	if _, err := davsql.Delete(tx, collID, uri, now); err != nil {
		return "", err
	}
	return photo, nil
}
