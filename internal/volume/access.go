package volume

import (
	"errors"
	"fmt"
	"iter"

	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin"
)

// ShareResourceType is the resource type used in the shares table for volumes.
const ShareResourceType = "files:volume"

// OwnerPerms are the permissions a volume's owner has on it.
const OwnerPerms = plugin.PermRead | plugin.PermWrite | plugin.PermReshare

// Access provides access-controlled volume operations using the shares table.
type Access struct {
	Volumes *Store
	Shares  *share.Store
	// Users tells whether a share of a system volume was made by an admin,
	// the only users who may share one.
	Users *user.Store
}

// Permissions returns what the user may do with the volume: everything when
// they own it, what its shares grant them otherwise, and 0 when they have no
// access.
//
// A system volume (no owner) is the server's: the default volume is the data
// directory itself, every user's files and databases. A user reaches one only
// through a share an admin made. A user volume is reached through a share its
// owner made. Shares anyone else made of it grant nothing — a share names its
// resource by ID, and creating one does not prove the creator owns it.
func (a *Access) Permissions(userID, volumeID string) (uint32, error) {
	vol, err := a.Volumes.GetByID(volumeID)
	if err != nil {
		return 0, err
	}
	return a.permissions(userID, vol)
}

func (a *Access) permissions(userID string, vol *Volume) (uint32, error) {
	if vol.OwnerUserID != nil && *vol.OwnerUserID == userID {
		return OwnerPerms, nil
	}
	if vol.OwnerUserID == nil {
		// Admins manage the system volumes.
		if admin, err := a.grantor(vol)(userID); err != nil || admin {
			return OwnerPerms, err
		}
	}
	decision, err := a.Shares.CheckAccessGrantedBy(userID, ShareResourceType, vol.ID, a.grantor(vol))
	if err != nil {
		return 0, err
	}
	return decision.EffectivePerms, nil
}

// grantor reports whether a share of vol made by ownerID counts.
func (a *Access) grantor(vol *Volume) func(ownerID string) (bool, error) {
	return func(ownerID string) (bool, error) {
		if vol.OwnerUserID != nil {
			return ownerID == *vol.OwnerUserID, nil
		}
		u, err := a.Users.GetByID(ownerID)
		if errors.Is(err, user.ErrNotFound) {
			return false, nil // the sharer has since been deleted
		}
		if err != nil {
			return false, fmt.Errorf("look up sharer of volume %s: %w", vol.ID, err)
		}
		return u.IsAdmin, nil
	}
}

// CanAccess reports whether the user has any access to the volume.
func (a *Access) CanAccess(userID, volumeID string) (bool, error) {
	perms, err := a.Permissions(userID, volumeID)
	return perms != 0, err
}

// ListAccessible returns the volumes the user owns or has been shared.
func (a *Access) ListAccessible(userID string) iter.Seq2[Volume, error] {
	return func(yield func(Volume, error) bool) {
		// Find volume IDs shared with this user.
		var sharedIDs []string
		for s, err := range a.Shares.ListBySubject(userID) {
			if err != nil {
				yield(Volume{}, fmt.Errorf("list volume shares: %w", err))
				return
			}
			if s.ResourceType == ShareResourceType {
				sharedIDs = append(sharedIDs, s.ResourceID)
			}
		}

		for v, err := range a.Volumes.ListAccessible(userID, sharedIDs) {
			if err != nil {
				yield(Volume{}, err)
				return
			}
			perms, err := a.permissions(userID, &v)
			if err != nil {
				yield(Volume{}, err)
				return
			}
			if perms == 0 {
				continue
			}
			if !yield(v, nil) {
				return
			}
		}
	}
}

// ShareVolume creates a share record for a volume. Only the owner can share.
func (a *Access) ShareVolume(ownerUserID, volumeID, subjectType, subjectID string, perms uint32) (*share.Record, error) {
	vol, err := a.Volumes.GetByID(volumeID)
	if err != nil {
		return nil, err
	}
	if vol.OwnerUserID == nil || *vol.OwnerUserID != ownerUserID {
		return nil, fmt.Errorf("only the volume owner can share")
	}
	return a.Shares.Create(ownerUserID, ShareResourceType, volumeID, subjectType, subjectID, perms)
}
