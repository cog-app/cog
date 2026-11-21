package gallery

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin"
)

// A user can share their people list. Whoever it is shared with gets a
// mirror of each person in it (linked_from, linked_id), named as the owner
// names them, and the faces on photos copied from the owner's gallery follow
// the owner's assignments. Their own photos' faces are clustered against
// those as against their own people, so a face of someone in the shared list
// joins that person. Changes to a mirror are made to the owner's list, when
// the share allows changes, and come back with the next sync.

const peopleResourceType = "people"

// peopleListID is the one row of people_lists, which a share names.
const peopleListID = "1"

var errPeopleReadOnly = errors.New("this person is in a people list shared with you that you may not change")

// peopleSource is the people list shared with a user, if any.
type peopleSource struct {
	owner    string
	writable bool
}

// peopleSource returns the people list shared with username. With more than
// one, the owner first by name wins: a person can be in only one list.
func (p *Plugin) peopleSource(username string) (peopleSource, error) {
	shares, users := p.ctx.ShareStore(), p.ctx.UserLookup()
	if shares == nil || users == nil {
		// Not "nothing shared": that would unlink every mirrored person.
		return peopleSource{}, errors.New("sharing is unavailable")
	}
	u, err := users.GetByUsername(username)
	if err != nil {
		return peopleSource{}, fmt.Errorf("look up %s: %w", username, err)
	}
	var found []peopleSource
	for rec, err := range shares.ListBySubject(u.ID) {
		if err != nil {
			return peopleSource{}, fmt.Errorf("shares with %s: %w", username, err)
		}
		if rec.ResourceType != PluginID+":"+peopleResourceType || rec.OwnerUserID == u.ID {
			continue
		}
		owner, err := users.GetByID(rec.OwnerUserID)
		if err != nil {
			return peopleSource{}, fmt.Errorf("owner of share %s: %w", rec.ID, err)
		}
		found = append(found, peopleSource{owner: owner.Username, writable: rec.Permissions&plugin.PermWrite != 0})
	}
	if len(found) == 0 {
		return peopleSource{}, nil
	}
	slices.SortFunc(found, func(a, b peopleSource) int { return strings.Compare(a.owner, b.owner) })
	best := found[0]
	for _, s := range found[1:] {
		if s.owner == best.owner {
			best.writable = best.writable || s.writable
		}
	}
	return best, nil
}

// syncSharedPeople brings a user's mirrors of the people list shared with
// them, and the people of the faces they follow, in step with the owner's.
// ownerDB is the owner's gallery database, or nil to open it read-only.
func (p *Plugin) syncSharedPeople(db *sql.DB, username string, ownerDB *sql.DB) error {
	src, err := p.peopleSource(username)
	if err != nil {
		return err
	}
	// Mirrors of a list no longer shared become the user's own people.
	if _, err := db.Exec(`UPDATE people SET linked_from = '', linked_id = NULL WHERE linked_from != '' AND linked_from != ?`, src.owner); err != nil {
		return fmt.Errorf("unlink people: %w", err)
	}
	if src.owner == "" {
		return nil
	}
	if ownerDB == nil {
		ownerDB, err = p.ctx.PluginDB(src.owner, plugin.ReadOnly, plugin.NoHooks)
		if err != nil {
			return fmt.Errorf("gallery db of %s: %w", src.owner, err)
		}
		defer ownerDB.Close()
	}

	type person struct {
		name    string
		visible bool
	}
	theirs := map[int64]person{}
	rows, err := ownerDB.Query(`SELECT id, name, visible FROM people WHERE linked_from = ''`)
	if err != nil {
		return fmt.Errorf("people of %s: %w", src.owner, err)
	}
	for rows.Next() {
		var id int64
		var pr person
		if err := rows.Scan(&id, &pr.name, &pr.visible); err != nil {
			rows.Close()
			return fmt.Errorf("people of %s: %w", src.owner, err)
		}
		theirs[id] = pr
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("people of %s: %w", src.owner, err)
	}

	followed, err := followedFaces(db, src.owner)
	if err != nil {
		return err
	}
	theirFaces, err := personOfFaces(ownerDB, followed)
	if err != nil {
		return fmt.Errorf("faces of %s: %w", src.owner, err)
	}

	return inTx(db, func(tx *sql.Tx) error {
		mirrors := map[int64]int64{} // their person -> mine
		rows, err := tx.Query(`SELECT id, linked_id, name, visible FROM people WHERE linked_from = ?`, src.owner)
		if err != nil {
			return fmt.Errorf("mirrored people: %w", err)
		}
		var stale []int64
		var changed []int64
		for rows.Next() {
			var id, linked int64
			var mine person
			if err := rows.Scan(&id, &linked, &mine.name, &mine.visible); err != nil {
				rows.Close()
				return fmt.Errorf("mirrored people: %w", err)
			}
			t, ok := theirs[linked]
			switch {
			case !ok:
				stale = append(stale, id)
			case t != mine:
				changed = append(changed, linked)
				mirrors[linked] = id
			default:
				mirrors[linked] = id
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return fmt.Errorf("mirrored people: %w", err)
		}
		for _, id := range stale {
			if _, err := tx.Exec(`DELETE FROM people WHERE id = ?`, id); err != nil {
				return fmt.Errorf("forget mirrored person %d: %w", id, err)
			}
		}
		for _, linked := range changed {
			t := theirs[linked]
			if _, err := tx.Exec(`UPDATE people SET name = ?, visible = ? WHERE id = ?`, t.name, t.visible, mirrors[linked]); err != nil {
				return fmt.Errorf("update mirrored person: %w", err)
			}
		}
		for linked, t := range theirs {
			if _, ok := mirrors[linked]; ok {
				continue
			}
			res, err := tx.Exec(`INSERT INTO people (name, visible, linked_from, linked_id) VALUES (?, ?, ?, ?)`,
				t.name, t.visible, src.owner, linked)
			if err != nil {
				return fmt.Errorf("mirror person: %w", err)
			}
			if mirrors[linked], err = res.LastInsertId(); err != nil {
				return fmt.Errorf("mirror person: %w", err)
			}
		}

		for _, f := range followed {
			var want sql.NullInt64
			if their, ok := theirFaces[f.ownerFaceID]; ok && their.Valid {
				if mine, ok := mirrors[their.Int64]; ok {
					want = sql.NullInt64{Int64: mine, Valid: true}
				}
			}
			if want == f.personID {
				continue
			}
			if _, err := tx.Exec(`UPDATE faces SET person_id = ? WHERE id = ?`, want, f.id); err != nil {
				return fmt.Errorf("follow face %d: %w", f.id, err)
			}
		}
		return nil
	})
}

type followedFace struct {
	id, ownerFaceID int64
	personID        sql.NullInt64
}

// followedFaces returns the faces on photos copied from owner's gallery.
func followedFaces(db *sql.DB, owner string) ([]followedFace, error) {
	rows, err := db.Query(`SELECT f.id, f.owner_face_id, f.person_id FROM faces f JOIN media m ON m.id = f.media_id
		WHERE m.shared_from = ? AND f.owner_face_id IS NOT NULL`, owner)
	if err != nil {
		return nil, fmt.Errorf("followed faces: %w", err)
	}
	defer rows.Close()
	var out []followedFace
	for rows.Next() {
		var f followedFace
		if err := rows.Scan(&f.id, &f.ownerFaceID, &f.personID); err != nil {
			return nil, fmt.Errorf("followed faces: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// personOfFaces returns the person of each of the given faces in db.
func personOfFaces(db *sql.DB, faces []followedFace) (map[int64]sql.NullInt64, error) {
	out := make(map[int64]sql.NullInt64, len(faces))
	const batch = 500
	for start := 0; start < len(faces); start += batch {
		chunk := faces[start:min(start+batch, len(faces))]
		ids := make([]any, len(chunk))
		for i, f := range chunk {
			ids[i] = f.ownerFaceID
		}
		rows, err := db.Query(`SELECT id, person_id FROM faces WHERE id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, ids...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var person sql.NullInt64
			if err := rows.Scan(&id, &person); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = person
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// sharedPerson is where a change to one of a user's people is made.
type sharedPerson struct {
	owner    string // "" for the user's own person
	ownerID  int64  // the person in the owner's list
	ownerDB  *sql.DB
	writable bool
}

// sharedPersonOf reports whether person id of username mirrors a person in a
// list shared with them, and if so where a change to it goes.
func (p *Plugin) sharedPersonOf(db *sql.DB, username string, id int64) (sharedPerson, error) {
	var from string
	var linked sql.NullInt64
	err := db.QueryRow(`SELECT linked_from, linked_id FROM people WHERE id = ?`, id).Scan(&from, &linked)
	if err != nil {
		return sharedPerson{}, fmt.Errorf("person %d: %w", id, err)
	}
	if from == "" {
		return sharedPerson{}, nil
	}
	return p.sharedTarget(username, from, linked.Int64)
}

// sharedFaceOf reports whether face id of username follows a face in the
// gallery whose people list is shared with them.
func (p *Plugin) sharedFaceOf(db *sql.DB, username string, id int64) (sharedPerson, error) {
	var from string
	var ownerFace sql.NullInt64
	err := db.QueryRow(`SELECT m.shared_from, f.owner_face_id FROM faces f JOIN media m ON m.id = f.media_id WHERE f.id = ?`, id).
		Scan(&from, &ownerFace)
	if err != nil {
		return sharedPerson{}, fmt.Errorf("face %d: %w", id, err)
	}
	if from == "" || !ownerFace.Valid {
		return sharedPerson{}, nil
	}
	return p.sharedTarget(username, from, ownerFace.Int64)
}

func (p *Plugin) sharedTarget(username, from string, ownerID int64) (sharedPerson, error) {
	src, err := p.peopleSource(username)
	if err != nil {
		return sharedPerson{}, err
	}
	if src.owner != from {
		return sharedPerson{}, nil
	}
	t := sharedPerson{owner: from, ownerID: ownerID, writable: src.writable}
	if t.writable {
		if t.ownerDB, err = p.DB(from); err != nil {
			return sharedPerson{}, fmt.Errorf("gallery db of %s: %w", from, err)
		}
	}
	return t, nil
}

// changeShared makes a change to the owner's list and brings the user's
// mirrors in step with it at once.
func (p *Plugin) changeShared(db *sql.DB, username string, t sharedPerson, change func(ownerDB *sql.DB) error) error {
	if !t.writable {
		return errPeopleReadOnly
	}
	if err := change(t.ownerDB); err != nil {
		return err
	}
	return p.syncSharedPeople(db, username, t.ownerDB)
}

var errMergeShared = errors.New("a person from a shared people list can only be merged into another from the same list")

// renamePerson names one of username's people, in the owner's list for a
// mirror.
func (p *Plugin) renamePerson(db *sql.DB, username string, id int64, name string) error {
	t, err := p.sharedPersonOf(db, username, id)
	if err != nil {
		return err
	}
	if t.owner == "" {
		return NamePersonRow(db, id, name)
	}
	return p.changeShared(db, username, t, func(ownerDB *sql.DB) error { return NamePersonRow(ownerDB, t.ownerID, name) })
}

// hidePerson toggles whether one of username's people is shown, in the
// owner's list for a mirror.
func (p *Plugin) hidePerson(db *sql.DB, username string, id int64) error {
	t, err := p.sharedPersonOf(db, username, id)
	if err != nil {
		return err
	}
	if t.owner == "" {
		return HidePersonRow(db, id)
	}
	return p.changeShared(db, username, t, func(ownerDB *sql.DB) error { return HidePersonRow(ownerDB, t.ownerID) })
}

// mergePeople merges source into target. The user's own person can go into
// anyone, which is how their faces join a shared person; a mirror only into
// another mirror of the same list, as a merge in that list.
func (p *Plugin) mergePeople(db *sql.DB, username string, targetID, sourceID int64) error {
	src, err := p.sharedPersonOf(db, username, sourceID)
	if err != nil {
		return err
	}
	if src.owner == "" {
		return MergePeopleRows(db, targetID, sourceID)
	}
	tgt, err := p.sharedPersonOf(db, username, targetID)
	if err != nil {
		return err
	}
	if tgt.owner != src.owner {
		return errMergeShared
	}
	return p.changeShared(db, username, src, func(ownerDB *sql.DB) error {
		if err := MergePeopleRows(ownerDB, tgt.ownerID, src.ownerID); err != nil {
			return err
		}
		// The user's own faces of source go with it rather than being left
		// for clustering when the mirror goes.
		return MergePeopleRows(db, targetID, sourceID)
	})
}

// newPersonFromFace makes a person of a face of username's, in the owner's
// list for a face that follows theirs, and returns the user's person.
func (p *Plugin) newPersonFromFace(db *sql.DB, username string, faceID int64) (int64, error) {
	t, err := p.sharedFaceOf(db, username, faceID)
	if err != nil {
		return 0, err
	}
	if t.owner == "" {
		return CreatePersonFromFace(db, faceID)
	}
	err = p.changeShared(db, username, t, func(ownerDB *sql.DB) error {
		_, err := CreatePersonFromFace(ownerDB, t.ownerID)
		return err
	})
	if err != nil {
		return 0, err
	}
	var person sql.NullInt64
	if err := db.QueryRow(`SELECT person_id FROM faces WHERE id = ?`, faceID).Scan(&person); err != nil {
		return 0, fmt.Errorf("face %d: %w", faceID, err)
	}
	if !person.Valid {
		return 0, fmt.Errorf("face %d: no person after making one", faceID)
	}
	return person.Int64, nil
}

// deleteFace removes a face of username's, and the owner's face it follows.
func (p *Plugin) deleteFace(db *sql.DB, username string, faceID int64) error {
	t, err := p.sharedFaceOf(db, username, faceID)
	if err != nil {
		return err
	}
	if t.owner != "" {
		if err := p.changeShared(db, username, t, func(ownerDB *sql.DB) error { return DeleteFace(ownerDB, t.ownerID) }); err != nil {
			return err
		}
	}
	return DeleteFace(db, faceID)
}
