package volume

import (
	"database/sql"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/share"
)

// ShareResourceType is the resource type used in the shares table for volumes.
const ShareResourceType = "system:volume"

// Access provides access-controlled volume operations using the shares table.
type Access struct {
	Volumes *Store
	Shares  *share.Store
	DB      *sql.DB // system DB for group membership queries
}

// CanAccess returns true if the user can access the given volume.
// Access is granted if: the volume has no owner (system volume), the user owns it,
// the volume is group-owned and user is in the group, or it has been shared with the user.
func (a *Access) CanAccess(userID, volumeID string) (bool, error) {
	vol, err := a.Volumes.GetByID(volumeID)
	if err != nil {
		return false, err
	}
	// System volume — accessible to all authenticated users.
	if vol.OwnerID == nil {
		return true, nil
	}
	// User-owned: owner has access.
	if vol.OwnerType == "user" && *vol.OwnerID == userID {
		return true, nil
	}
	// Group-owned: check group membership.
	if vol.OwnerType == "group" && a.DB != nil {
		var count int
		err := a.DB.QueryRow(
			`SELECT COUNT(*) FROM group_membership_closure WHERE ancestor_group_id = ? AND descendant_user_id = ?`,
			*vol.OwnerID, userID,
		).Scan(&count)
		if err == nil && count > 0 {
			return true, nil
		}
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

	// Find group-owned volume IDs via group membership.
	var groupVolumeIDs []string
	if a.DB != nil {
		rows, err := a.DB.Query(`
			SELECT v.id FROM volumes v
			JOIN group_membership_closure gmc ON v.owner_id = gmc.ancestor_group_id
			WHERE v.owner_type = 'group' AND gmc.descendant_user_id = ?`, userID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					groupVolumeIDs = append(groupVolumeIDs, id)
				}
			}
		}
	}

	// Merge shared and group volume IDs.
	allExtraIDs := append(sharedIDs, groupVolumeIDs...)
	return a.Volumes.ListAccessible(userID, allExtraIDs)
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
