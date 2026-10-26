package dav

import (
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

// sharesFor returns the collection shares that reach userID. A share names a
// resource ID, not a path, so it only counts together with its owner.
func sharesFor(shares plugin.ShareStore, userID string) (shareMap, error) {
	out := shareMap{}
	if shares == nil {
		return out, nil
	}
	for rec, err := range shares.ListBySubject(userID) {
		if err != nil {
			return nil, err
		}
		if rec.ResourceType != ShareType {
			continue
		}
		n, err := strconv.ParseInt(rec.ResourceID, 10, 64)
		if err != nil {
			continue // not a collection ID; the share names nothing
		}
		id := CollectionID(n)
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
func (s *Store) Shared(shares plugin.ShareStore, username func(userID string) (string, error), userID string, comps ...Component) ([]SharedCollection, error) {
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
