package dav

import (
	"iter"
	"slices"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin"
)

// shareMap holds, per collection, the permissions each owner granted.
type shareMap map[CollectionID]map[string]uint32

// perms returns what the collection's owner shared; shares by anyone else
// name a collection they do not own and grant nothing.
func (m shareMap) perms(c *Collection) uint32 { return m[c.ID][c.OwnerID] }

// subjectShares lists the shares that reach a user.
type subjectShares interface {
	ListBySubject(subjectID string) iter.Seq2[plugin.ShareRecord, error]
}

// collectionShare returns the collection a share names, if it names one.
func collectionShare(rec plugin.ShareRecord) (CollectionID, bool) {
	if rec.ResourceType != ShareType {
		return 0, false
	}
	n, err := strconv.ParseInt(rec.ResourceID, 10, 64)
	if err != nil {
		return 0, false // not a collection ID; the share names nothing
	}
	return CollectionID(n), true
}

// sharesFor returns the collection shares that reach userID. A share names a
// resource ID, not a path, so it only counts together with its owner.
func sharesFor(shares subjectShares, userID string) (shareMap, error) {
	out := shareMap{}
	if shares == nil {
		return out, nil
	}
	for rec, err := range shares.ListBySubject(userID) {
		if err != nil {
			return nil, err
		}
		id, ok := collectionShare(rec)
		if !ok {
			continue
		}
		if out[id] == nil {
			out[id] = map[string]uint32{}
		}
		out[id][rec.OwnerUserID] |= rec.Permissions
	}
	return out, nil
}

// SharedCollection is a collection shared with the current user.
type SharedCollection struct {
	Collection
	OwnerUsername string
	Permissions   uint32
}

// Shared returns the collections shared with userID by their owners that
// hold any of comps, sorted by title. username names a user ID.
func (s *Store) Shared(shares subjectShares, username func(userID string) (string, error), userID string, comps ...Component) ([]SharedCollection, error) {
	byID, err := sharesFor(shares, userID)
	if err != nil {
		return nil, err
	}
	ids := make([]CollectionID, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	cols, err := s.ByIDs(ids)
	if err != nil {
		return nil, err
	}
	var out []SharedCollection
	names := map[string]string{}
	for _, c := range cols {
		perms := byID.perms(&c)
		if perms == 0 || c.OwnerID == userID || !slices.ContainsFunc(comps, c.Has) {
			continue
		}
		name, ok := names[c.OwnerID]
		if !ok {
			name, err = username(c.OwnerID)
			if err != nil {
				return nil, err
			}
			names[c.OwnerID] = name
		}
		out = append(out, SharedCollection{Collection: c, OwnerUsername: name, Permissions: perms})
	}
	slices.SortFunc(out, func(a, b SharedCollection) int { return strings.Compare(a.Title(), b.Title()) })
	return out, nil
}
