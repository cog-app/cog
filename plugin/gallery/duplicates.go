package gallery

import (
	"database/sql"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"path"
	"regexp"
	"strings"

	"github.com/ajdnik/imghash"
	"github.com/ajdnik/imghash/hashtype"

	_ "golang.org/x/image/webp"
)

// binaryHasher is the interface satisfied by all imghash binary hash types.
type binaryHasher interface {
	Calculate(img image.Image) hashtype.Binary
}

// getHasher returns a binary hasher for the given algorithm name.
func getHasher(algorithm DuplicateAlgorithm) (binaryHasher, error) {
	switch algorithm {
	case AlgoAverage:
		h := imghash.NewAverage()
		return &h, nil
	case AlgoDifference, "":
		h := imghash.NewDifference()
		return &h, nil
	case AlgoPHash:
		h := imghash.NewPHash()
		return &h, nil
	case AlgoMedian:
		h := imghash.NewMedian()
		return &h, nil
	case AlgoBlockMean:
		h := imghash.NewBlockMean()
		return &h, nil
	default:
		return nil, fmt.Errorf("unknown hash algorithm: %q", algorithm)
	}
}

// DuplicateGroup represents a set of duplicate media items.
type DuplicateGroup struct {
	ID           int64
	Kind         DuplicateKind
	MemberCount  int
	CoverMediaID int64
	CreatedAt    int64
}

// DuplicateMember represents a media item within a duplicate group.
type DuplicateMember struct {
	Media    Media
	IsKeep   bool
	Distance float64 // fingerprint bits that differ from the kept one's
	Score    float64 // PSNR against the kept one in dB, or -1 if not measured
	Size     int64   // file size in bytes, when known
}

// IsEdit reports whether the file's name says an editor saved it:
// Google Photos' "-edited" and the "~2" of Android's gallery.
func (m DuplicateMember) IsEdit() bool {
	name := m.Media.FileName()
	stem := name[:len(name)-len(path.Ext(name))]
	return strings.HasSuffix(stem, "-edited") || editSuffix.MatchString(stem)
}

// IsMotion reports whether the photo is a Pixel motion photo, which
// carries a short video inside it.
func (m DuplicateMember) IsMotion() bool {
	return strings.Contains(m.Media.FileName(), ".MP")
}

var editSuffix = regexp.MustCompile(`~\d+$`)

// UpdateFingerprint sets the fingerprint for a media item.
func UpdateFingerprint(db *sql.DB, mediaID int64, fingerprint string) error {
	_, err := db.Exec(`UPDATE media SET fingerprint = ? WHERE id = ?`, fingerprint, mediaID)
	return err
}

// ClearAllFingerprints resets all fingerprints (e.g. when algorithm changes).
// Those copied from another user's gallery follow when the owner's change.
func ClearAllFingerprints(db *sql.DB) error {
	_, err := db.Exec(`UPDATE media SET fingerprint = '' WHERE shared_from = ''`)
	return err
}

// InsertDuplicateGroup creates a new group and returns its ID.
func InsertDuplicateGroup(db *sql.DB) (int64, error) {
	res, err := db.Exec(`INSERT INTO duplicate_groups DEFAULT VALUES`)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AddDuplicateMember adds a media item to a duplicate group.
func AddDuplicateMember(db *sql.DB, groupID, mediaID int64, isKeep bool, distance float64) error {
	keep := 0
	if isKeep {
		keep = 1
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO duplicate_members (group_id, media_id, is_keep, distance) VALUES (?, ?, ?, ?)`,
		groupID, mediaID, keep, distance)
	return err
}

// duplicateGroupLimit caps the groups a list shows at once; the page says
// how many more there are.
const duplicateGroupLimit = 500

// ListDuplicateGroups returns up to limit groups of a kind with two or more
// members, in the order a review goes through them, oldest group first.
func ListDuplicateGroups(db *sql.DB, kind DuplicateKind, limit int) ([]DuplicateGroup, error) {
	rows, err := db.Query(`SELECT g.id, g.kind, g.created_at, COUNT(dm.media_id) as cnt,
		COALESCE((SELECT media_id FROM duplicate_members WHERE group_id = g.id AND is_keep = 1 LIMIT 1),
			(SELECT media_id FROM duplicate_members WHERE group_id = g.id LIMIT 1)) as cover
		FROM duplicate_groups g
		JOIN duplicate_members dm ON dm.group_id = g.id
		WHERE g.kind = ?
		GROUP BY g.id
		HAVING cnt >= 2
		ORDER BY g.id
		LIMIT ?`, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []DuplicateGroup
	for rows.Next() {
		var g DuplicateGroup
		if err := rows.Scan(&g.ID, &g.Kind, &g.CreatedAt, &g.MemberCount, &g.CoverMediaID); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// DuplicateKindCounts is how many groups of two or more there are of each kind.
func DuplicateKindCounts(db *sql.DB) (map[DuplicateKind]int, error) {
	rows, err := db.Query(`SELECT g.kind, COUNT(*) FROM duplicate_groups g
		WHERE (SELECT COUNT(*) FROM duplicate_members WHERE group_id = g.id) >= 2
		GROUP BY g.kind`)
	if err != nil {
		return nil, fmt.Errorf("count duplicate groups: %w", err)
	}
	defer rows.Close()
	out := map[DuplicateKind]int{}
	for rows.Next() {
		var k DuplicateKind
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, fmt.Errorf("count duplicate groups: %w", err)
		}
		out[k] = n
	}
	return out, rows.Err()
}

// DuplicateGroupKind is a group's kind, or sql.ErrNoRows when it is gone.
func DuplicateGroupKind(db *sql.DB, groupID int64) (DuplicateKind, error) {
	var k DuplicateKind
	err := db.QueryRow(`SELECT kind FROM duplicate_groups WHERE id = ?`, groupID).Scan(&k)
	return k, err
}

// NextDuplicateGroup is the group of the same kind that a review comes to
// after groupID, going round to the start; ok is false when there is none.
func NextDuplicateGroup(db *sql.DB, kind DuplicateKind, groupID int64) (next int64, ok bool, err error) {
	err = db.QueryRow(`SELECT id FROM duplicate_groups g
		WHERE kind = ? AND id != ?
			AND (SELECT COUNT(*) FROM duplicate_members WHERE group_id = g.id) >= 2
		ORDER BY id <= ?, id LIMIT 1`, kind, groupID, groupID).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("next duplicate group: %w", err)
	}
	return next, true, nil
}

// DuplicateGroupMembers returns all members of a group with full media details.
func DuplicateGroupMembers(db *sql.DB, groupID int64) ([]DuplicateMember, error) {
	// Query member metadata separately to avoid extending scanMedia.
	type memberInfo struct {
		MediaID  int64
		IsKeep   bool
		Distance float64
		Score    float64
	}
	mRows, err := db.Query(`SELECT media_id, is_keep, distance, score FROM duplicate_members
		WHERE group_id = ? ORDER BY is_keep DESC, media_id`, groupID)
	if err != nil {
		return nil, err
	}
	defer mRows.Close()

	var infos []memberInfo
	for mRows.Next() {
		var mi memberInfo
		var isKeep int
		if err := mRows.Scan(&mi.MediaID, &isKeep, &mi.Distance, &mi.Score); err != nil {
			return nil, err
		}
		mi.IsKeep = isKeep == 1
		infos = append(infos, mi)
	}
	if err := mRows.Err(); err != nil {
		return nil, err
	}

	// The members' media in one query, not one each.
	rows, err := db.Query(`SELECT `+mediaCols+` FROM media m
		WHERE m.id IN (SELECT media_id FROM duplicate_members WHERE group_id = ?)`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	media, err := scanMediaRows(rows)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*Media, len(media))
	for i := range media {
		byID[media[i].ID] = &media[i]
	}

	var members []DuplicateMember
	for _, mi := range infos {
		m, ok := byID[mi.MediaID]
		if !ok {
			continue // media deleted since the group was built
		}
		members = append(members, DuplicateMember{
			Media:    *m,
			IsKeep:   mi.IsKeep,
			Distance: mi.Distance,
			Score:    mi.Score,
		})
	}
	return members, nil
}

// PruneDuplicateGroups deletes every group left with fewer than two members.
func PruneDuplicateGroups(db *sql.DB) error {
	if _, err := db.Exec(`DELETE FROM duplicate_groups
		WHERE (SELECT COUNT(*) FROM duplicate_members WHERE group_id = duplicate_groups.id) < 2`); err != nil {
		return fmt.Errorf("prune duplicate groups: %w", err)
	}
	return nil
}

// DeleteDuplicateGroup removes a group (CASCADE removes members).
func DeleteDuplicateGroup(db *sql.DB, groupID int64) error {
	_, err := db.Exec(`DELETE FROM duplicate_groups WHERE id = ?`, groupID)
	return err
}

// RemoveDuplicateMember takes a member out of its group and remembers it is
// not a duplicate of the others, so the next scan does not put it back.
func RemoveDuplicateMember(db *sql.DB, groupID, mediaID int64) error {
	return inTx(db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO duplicate_exclusions (media_a, media_b)
			SELECT MIN(?1, media_id), MAX(?1, media_id) FROM duplicate_members
			WHERE group_id = ?2 AND media_id != ?1`, mediaID, groupID); err != nil {
			return fmt.Errorf("exclude media %d from group %d: %w", mediaID, groupID, err)
		}
		if _, err := tx.Exec(`DELETE FROM duplicate_members WHERE group_id = ? AND media_id = ?`, groupID, mediaID); err != nil {
			return fmt.Errorf("remove media %d from group %d: %w", mediaID, groupID, err)
		}
		return nil
	})
}

// DismissDuplicateGroup removes a group and remembers that none of its
// members are duplicates of each other.
func DismissDuplicateGroup(db *sql.DB, groupID int64) error {
	return inTx(db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO duplicate_exclusions (media_a, media_b)
			SELECT a.media_id, b.media_id FROM duplicate_members a
			JOIN duplicate_members b ON b.group_id = a.group_id AND a.media_id < b.media_id
			WHERE a.group_id = ?`, groupID); err != nil {
			return fmt.Errorf("exclude group %d: %w", groupID, err)
		}
		if _, err := tx.Exec(`DELETE FROM duplicate_groups WHERE id = ?`, groupID); err != nil {
			return fmt.Errorf("dismiss group %d: %w", groupID, err)
		}
		return nil
	})
}

// SetKeepMember makes a member its group's keep item, and remembers the
// choice for when the group is rebuilt.
func SetKeepMember(db *sql.DB, groupID, mediaID int64) error {
	return inTx(db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM duplicate_keeps WHERE media_id IN
			(SELECT media_id FROM duplicate_members WHERE group_id = ?)`, groupID); err != nil {
			return fmt.Errorf("clear keep of group %d: %w", groupID, err)
		}
		if _, err := tx.Exec(`INSERT INTO duplicate_keeps (media_id) VALUES (?)`, mediaID); err != nil {
			return fmt.Errorf("keep media %d: %w", mediaID, err)
		}
		if _, err := tx.Exec(`UPDATE duplicate_members SET is_keep = (media_id = ?) WHERE group_id = ?`, mediaID, groupID); err != nil {
			return fmt.Errorf("keep media %d in group %d: %w", mediaID, groupID, err)
		}
		return nil
	})
}

// inTx runs fn in a transaction, committing if it succeeds.
func inTx(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// DuplicateGroupCount returns the number of duplicate groups.
func DuplicateGroupCount(db *sql.DB) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM duplicate_groups g
		WHERE (SELECT COUNT(*) FROM duplicate_members WHERE group_id = g.id) >= 2`).Scan(&count)
	return count, err
}

// DuplicateGroupMemberCount returns the number of members in a group.
func DuplicateGroupMemberCount(db *sql.DB, groupID int64) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM duplicate_members WHERE group_id = ?`, groupID).Scan(&count)
	return count, err
}

// UpdateQualityScore sets the quality score for a media item.
func UpdateQualityScore(db *sql.DB, mediaID int64, score float64) error {
	_, err := db.Exec(`UPDATE media SET quality_score = ? WHERE id = ?`, score, mediaID)
	return err
}
