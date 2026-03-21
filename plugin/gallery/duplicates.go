package gallery

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strconv"

	"github.com/ajdnik/imghash"
	"github.com/ajdnik/imghash/hashtype"
	"github.com/ajdnik/imghash/similarity"

	_ "golang.org/x/image/webp"
)

// binaryHasher is the interface satisfied by all imghash binary hash types.
type binaryHasher interface {
	Calculate(img image.Image) hashtype.Binary
}

// getHasher returns a binary hasher for the given algorithm name.
func getHasher(algorithm string) (binaryHasher, error) {
	switch algorithm {
	case "average":
		h := imghash.NewAverage()
		return &h, nil
	case "difference", "":
		h := imghash.NewDifference()
		return &h, nil
	case "phash":
		h := imghash.NewPHash()
		return &h, nil
	case "median":
		h := imghash.NewMedian()
		return &h, nil
	case "blockmean":
		h := imghash.NewBlockMean()
		return &h, nil
	default:
		return nil, fmt.Errorf("unknown hash algorithm: %q", algorithm)
	}
}

// DuplicateGroup represents a set of duplicate media items.
type DuplicateGroup struct {
	ID           int64
	MemberCount  int
	CoverMediaID int64
	CreatedAt    int64
}

// DuplicateMember represents a media item within a duplicate group.
type DuplicateMember struct {
	Media    Media
	IsKeep   bool
	Distance float64
}

// --- Store functions ---

// UpdateFingerprint sets the fingerprint for a media item.
func UpdateFingerprint(db *sql.DB, mediaID int64, fingerprint string) error {
	_, err := db.Exec(`UPDATE media SET fingerprint = ? WHERE id = ?`, fingerprint, mediaID)
	return err
}

// ClearAllFingerprints resets all fingerprints (e.g. when algorithm changes).
func ClearAllFingerprints(db *sql.DB) error {
	_, err := db.Exec(`UPDATE media SET fingerprint = ''`)
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

// ListDuplicateGroups returns all duplicate groups with member count and cover media ID.
func ListDuplicateGroups(db *sql.DB) ([]DuplicateGroup, error) {
	rows, err := db.Query(`SELECT g.id, g.created_at, COUNT(dm.media_id) as cnt,
		COALESCE((SELECT media_id FROM duplicate_members WHERE group_id = g.id AND is_keep = 1 LIMIT 1),
			(SELECT media_id FROM duplicate_members WHERE group_id = g.id LIMIT 1)) as cover
		FROM duplicate_groups g
		JOIN duplicate_members dm ON dm.group_id = g.id
		GROUP BY g.id
		HAVING cnt >= 2
		ORDER BY g.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []DuplicateGroup
	for rows.Next() {
		var g DuplicateGroup
		if err := rows.Scan(&g.ID, &g.CreatedAt, &g.MemberCount, &g.CoverMediaID); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// DuplicateGroupMembers returns all members of a group with full media details.
func DuplicateGroupMembers(db *sql.DB, groupID int64) ([]DuplicateMember, error) {
	// Query member metadata separately to avoid extending scanMedia.
	type memberInfo struct {
		MediaID  int64
		IsKeep   bool
		Distance float64
	}
	mRows, err := db.Query(`SELECT media_id, is_keep, distance FROM duplicate_members
		WHERE group_id = ? ORDER BY is_keep DESC`, groupID)
	if err != nil {
		return nil, err
	}
	defer mRows.Close()

	var infos []memberInfo
	for mRows.Next() {
		var mi memberInfo
		var isKeep int
		if err := mRows.Scan(&mi.MediaID, &isKeep, &mi.Distance); err != nil {
			return nil, err
		}
		mi.IsKeep = isKeep == 1
		infos = append(infos, mi)
	}
	if err := mRows.Err(); err != nil {
		return nil, err
	}

	var members []DuplicateMember
	for _, mi := range infos {
		m, err := GetMedia(db, mi.MediaID)
		if err != nil {
			continue // media may have been deleted
		}
		members = append(members, DuplicateMember{
			Media:    *m,
			IsKeep:   mi.IsKeep,
			Distance: mi.Distance,
		})
	}
	return members, nil
}

// DeleteDuplicateGroup removes a group (CASCADE removes members).
func DeleteDuplicateGroup(db *sql.DB, groupID int64) error {
	_, err := db.Exec(`DELETE FROM duplicate_groups WHERE id = ?`, groupID)
	return err
}

// RemoveDuplicateMember removes a single member from a group.
func RemoveDuplicateMember(db *sql.DB, groupID, mediaID int64) error {
	_, err := db.Exec(`DELETE FROM duplicate_members WHERE group_id = ? AND media_id = ?`, groupID, mediaID)
	return err
}

// SetKeepMember sets a member as the keep item, clearing others.
func SetKeepMember(db *sql.DB, groupID, mediaID int64) error {
	_, err := db.Exec(`UPDATE duplicate_members SET is_keep = 0 WHERE group_id = ?`, groupID)
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE duplicate_members SET is_keep = 1 WHERE group_id = ? AND media_id = ?`, groupID, mediaID)
	return err
}

// ClearDuplicateGroups removes all duplicate groups and members.
func ClearDuplicateGroups(db *sql.DB) error {
	_, err := db.Exec(`DELETE FROM duplicate_groups`)
	return err
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

// --- Fingerprint computation ---

// computeFingerprints generates perceptual hashes for unfingerprinted images.
func (p *Plugin) computeFingerprints(ctx context.Context, db *sql.DB, username string) error {
	sysDB, err := p.SystemDB()
	if err != nil {
		return fmt.Errorf("system db: %w", err)
	}
	if GetSetting(sysDB, "duplicate_detection_enabled", "true") != "true" {
		return nil
	}

	algorithm := GetSetting(sysDB, "duplicate_algorithm", "difference")

	// Check if algorithm changed — invalidate old fingerprints.
	lastAlgo := GetSetting(db, "duplicate_algorithm_used", "")
	if lastAlgo != "" && lastAlgo != algorithm {
		p.log.Info("duplicate algorithm changed, clearing fingerprints", "old", lastAlgo, "new", algorithm)
		if err := ClearAllFingerprints(db); err != nil {
			return fmt.Errorf("clear fingerprints: %w", err)
		}
	}
	SetSetting(db, "duplicate_algorithm_used", algorithm)

	hasher, err := getHasher(algorithm)
	if err != nil {
		return err
	}

	rows, err := db.Query(`SELECT id FROM media WHERE fingerprint = '' AND mime_type LIKE 'image/%' AND width > 0 LIMIT 100`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	var computed int
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		m, err := GetMedia(db, id)
		if err != nil {
			continue
		}

		srcPath := p.resolveLocalPath(username, m.FilePath)
		f, err := os.Open(srcPath)
		if err != nil {
			p.log.Debug("fingerprint: open failed", "id", id, "err", err)
			continue
		}

		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			p.log.Debug("fingerprint: decode failed", "id", id, "err", err)
			// Mark with a sentinel so we don't retry.
			UpdateFingerprint(db, id, "-")
			continue
		}

		hash := hasher.Calculate(img)
		fp := hex.EncodeToString([]byte(hash))
		if err := UpdateFingerprint(db, id, fp); err != nil {
			return fmt.Errorf("update fingerprint %d: %w", id, err)
		}
		computed++
	}

	if computed > 0 {
		p.log.Info("fingerprints computed", "user", username, "count", computed)
	}
	return nil
}

// --- Duplicate detection ---

// detectDuplicates groups media with similar fingerprints.
func (p *Plugin) detectDuplicates(db *sql.DB) error {
	sysDB, err := p.SystemDB()
	if err != nil {
		return fmt.Errorf("system db: %w", err)
	}
	if GetSetting(sysDB, "duplicate_detection_enabled", "true") != "true" {
		return nil
	}

	maxDist := 5
	if v := GetSetting(sysDB, "duplicate_distance", "5"); v != "" {
		if d, err := strconv.Atoi(v); err == nil {
			maxDist = d
		}
	}

	// Load all valid fingerprints.
	type fpEntry struct {
		ID          int64
		Fingerprint string
		Width       int
		Height      int
		DateTaken   *int64
		DateIndexed int64
	}

	rows, err := db.Query(`SELECT id, fingerprint, width, height, date_taken, date_indexed
		FROM media WHERE fingerprint != '' AND fingerprint != '-'`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var entries []fpEntry
	for rows.Next() {
		var e fpEntry
		if err := rows.Scan(&e.ID, &e.Fingerprint, &e.Width, &e.Height, &e.DateTaken, &e.DateIndexed); err != nil {
			return err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(entries) < 2 {
		return nil
	}

	// Decode fingerprints to binary hashes.
	hashes := make([]hashtype.Binary, len(entries))
	for i, e := range entries {
		b, err := hex.DecodeString(e.Fingerprint)
		if err != nil {
			continue
		}
		hashes[i] = hashtype.Binary(b)
	}

	// Union-find for grouping.
	parent := make([]int, len(entries))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	// O(n²) comparison.
	for i := 0; i < len(entries); i++ {
		if hashes[i] == nil {
			continue
		}
		for j := i + 1; j < len(entries); j++ {
			if hashes[j] == nil {
				continue
			}
			if len(hashes[i]) != len(hashes[j]) {
				continue
			}
			dist := similarity.Hamming(hashes[i], hashes[j])
			if int(dist) <= maxDist {
				union(i, j)
			}
		}
	}

	// Build groups.
	groups := map[int][]int{} // root → member indices
	for i := range entries {
		if hashes[i] == nil {
			continue
		}
		root := find(i)
		groups[root] = append(groups[root], i)
	}

	// Clear old groups and insert new ones.
	if err := ClearDuplicateGroups(db); err != nil {
		return fmt.Errorf("clear groups: %w", err)
	}

	for _, members := range groups {
		if len(members) < 2 {
			continue
		}

		// Select keep: highest resolution, oldest date as tiebreaker.
		keepIdx := members[0]
		for _, idx := range members[1:] {
			e := entries[idx]
			k := entries[keepIdx]
			ePixels := e.Width * e.Height
			kPixels := k.Width * k.Height
			if ePixels > kPixels {
				keepIdx = idx
			} else if ePixels == kPixels {
				eDate := e.DateIndexed
				if e.DateTaken != nil {
					eDate = *e.DateTaken
				}
				kDate := k.DateIndexed
				if k.DateTaken != nil {
					kDate = *k.DateTaken
				}
				if eDate < kDate {
					keepIdx = idx
				}
			}
		}

		groupID, err := InsertDuplicateGroup(db)
		if err != nil {
			return fmt.Errorf("insert group: %w", err)
		}

		for _, idx := range members {
			isKeep := idx == keepIdx
			dist := 0.0
			if hashes[idx] != nil && hashes[keepIdx] != nil && len(hashes[idx]) == len(hashes[keepIdx]) {
				dist = float64(similarity.Hamming(hashes[idx], hashes[keepIdx]))
			}
			if err := AddDuplicateMember(db, groupID, entries[idx].ID, isKeep, dist); err != nil {
				return fmt.Errorf("add member: %w", err)
			}
		}
	}

	return nil
}
