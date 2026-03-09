package volume

import (
	"fmt"
	"iter"

	"git.duckfam.us/jj-software/cog/internal/share"
)

// ShareResourceType is the resource type used in the shares table for volumes.
const ShareResourceType = "files:volume"

// Access provides access-controlled volume operations using the shares table.
type Access struct {
	Volumes *Store
	Shares  *share.Store
}

// CanAccess returns true if the user can access the given volume.
// Access is granted if: the volume has no owner (system volume), the user owns it,
// or it has been shared with the user.
func (a *Access) CanAccess(userID, volumeID string) (bool, error) {
	vol, err := a.Volumes.GetByID(volumeID)
	if err != nil {
		return false, err
	}
	// System volume — accessible to all authenticated users.
	if vol.OwnerUserID == nil {
		return true, nil
	}
	// User-owned: owner has access.
	if *vol.OwnerUserID == userID {
		return true, nil
	}
	// Check shares.
	decision, err := a.Shares.CheckAccess(userID, ShareResourceType, volumeID)
	if err != nil {
		return false, err
	}
	return decision.Allowed, nil
}

// ListAccessible returns all volumes accessible to the user.
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
			if !yield(v, err) {
				return
			}
			if err != nil {
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
