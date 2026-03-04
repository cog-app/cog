package volume

import (
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/share"
)

// ShareResourceType is the resource type used in the shares table for volumes.
const ShareResourceType = "system:volume"

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
	if vol.OwnerID == nil {
		return true, nil
	}
	// Owner has access.
	if *vol.OwnerID == userID {
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
func (a *Access) ListAccessible(userID string) ([]Volume, error) {
	// Find volume IDs shared with this user.
	shares, err := a.Shares.ListBySubject(userID)
	if err != nil {
		return nil, fmt.Errorf("list volume shares: %w", err)
	}
	var sharedIDs []string
	for _, s := range shares {
		if s.ResourceType == ShareResourceType {
			sharedIDs = append(sharedIDs, s.ResourceID)
		}
	}
	return a.Volumes.ListAccessible(userID, sharedIDs)
}

// ShareVolume creates a share record for a volume. Only the owner can share.
func (a *Access) ShareVolume(ownerUserID, volumeID, subjectType, subjectID string, perms uint32) (*share.Record, error) {
	vol, err := a.Volumes.GetByID(volumeID)
	if err != nil {
		return nil, err
	}
	if vol.OwnerID == nil || *vol.OwnerID != ownerUserID {
		return nil, fmt.Errorf("only the volume owner can share")
	}
	return a.Shares.Create(ownerUserID, ShareResourceType, volumeID, subjectType, subjectID, perms)
}
