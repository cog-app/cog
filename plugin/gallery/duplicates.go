package gallery

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"math/bits"
	"slices"
	"strings"

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
		})
	}
	return members, nil
}

// NonKeepDuplicates returns, in one query, the media of every member that is
// not its group's keep item, across the groups ListDuplicateGroups shows (two
// or more members). A media item in more than one group comes back once.
func NonKeepDuplicates(db *sql.DB) ([]DuplicateMember, error) {
	rows, err := db.Query(`SELECT ` + mediaCols + ` FROM media m
		WHERE m.id IN (
			SELECT media_id FROM duplicate_members
			WHERE is_keep = 0 AND group_id IN (
				SELECT group_id FROM duplicate_members GROUP BY group_id HAVING COUNT(*) >= 2))`)
	if err != nil {
		return nil, fmt.Errorf("list non-keep duplicates: %w", err)
	}
	defer rows.Close()
	media, err := scanMediaRows(rows)
	if err != nil {
		return nil, fmt.Errorf("scan non-keep duplicates: %w", err)
	}
	members := make([]DuplicateMember, len(media))
	for i, m := range media {
		members[i] = DuplicateMember{Media: m}
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

// --- Fingerprint computation ---

// --- Duplicate detection ---

// detectDuplicates groups media with similar fingerprints.
// fpEntry holds media fingerprint data for duplicate detection.
type fpEntry struct {
	ID           int64
	Fingerprint  string
	Width        int
	Height       int
	DateTaken    *int64
	DateIndexed  int64
	QualityScore float64
}

func (p *Plugin) detectDuplicates(db *sql.DB) error {
	sysDB, err := p.SystemDB()
	if err != nil {
		return fmt.Errorf("system db: %w", err)
	}
	if !SettingDuplicateDetectionEnabled.MustGet(sysDB) {
		return nil
	}

	return rebuildDuplicateGroups(db, SettingDuplicateDistance.MustGet(sysDB), p.log)
}

// rebuildDuplicateGroups groups photos whose fingerprints are within maxDist
// of each other, honouring what people decided about earlier groups.
func rebuildDuplicateGroups(db *sql.DB, maxDist int, log *slog.Logger) error {
	entries, err := loadFingerprints(db)
	if err != nil {
		return err
	}
	excluded, err := loadDuplicateExclusions(db)
	if err != nil {
		return err
	}
	kept, err := loadDuplicateKeeps(db)
	if err != nil {
		return err
	}

	hashes := make([]hashtype.Binary, len(entries))
	for i, e := range entries {
		b, err := hex.DecodeString(e.Fingerprint)
		if err != nil {
			log.Warn("duplicates: bad fingerprint, skipping", "id", e.ID, "err", err)
			continue
		}
		hashes[i] = hashtype.Binary(b)
	}

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
	words := make([][]uint64, len(hashes))
	for i, h := range hashes {
		words[i] = packWords(h)
	}
	for i := range entries {
		if hashes[i] == nil {
			continue
		}
		wi := words[i]
		for j := i + 1; j < len(entries); j++ {
			if hashes[j] == nil || len(hashes[i]) != len(hashes[j]) {
				continue
			}
			if hammingWords(wi, words[j]) > maxDist {
				continue
			}
			if excluded[pairKey(entries[i].ID, entries[j].ID)] {
				continue
			}
			if ri, rj := find(i), find(j); ri != rj {
				parent[ri] = rj
			}
		}
	}

	byRoot := map[int][]int{}
	for i := range entries {
		if hashes[i] != nil {
			byRoot[find(i)] = append(byRoot[find(i)], i)
		}
	}
	var groups []duplicateGroup
	for _, members := range byRoot {
		if len(members) < 2 {
			continue
		}
		keepIdx := members[0]
		for _, idx := range members[1:] {
			if kept[entries[idx].ID] != kept[entries[keepIdx].ID] {
				if kept[entries[idx].ID] {
					keepIdx = idx
				}
				continue
			}
			if betterKeep(entries[idx], entries[keepIdx]) {
				keepIdx = idx
			}
		}
		g := duplicateGroup{keep: entries[keepIdx].ID}
		for _, idx := range members {
			dist := 0.0
			if len(hashes[idx]) == len(hashes[keepIdx]) {
				dist = float64(similarity.Hamming(hashes[idx], hashes[keepIdx]))
			}
			g.members = append(g.members, duplicateMemberRow{mediaID: entries[idx].ID, distance: dist})
		}
		groups = append(groups, g)
	}
	return replaceDuplicateGroups(db, groups)
}

// packWords packs a fingerprint into 64-bit words. Every pair of photos is
// compared, and similarity.Hamming, a byte at a time, took 80 seconds over
// 34,000 of them.
func packWords(h hashtype.Binary) []uint64 {
	if h == nil {
		return nil
	}
	w := make([]uint64, (len(h)+7)/8)
	for i, b := range h {
		w[i/8] |= uint64(b) << (8 * (i % 8))
	}
	return w
}

func hammingWords(a, b []uint64) int {
	d := 0
	for k := range a {
		d += bits.OnesCount64(a[k] ^ b[k])
	}
	return d
}

type duplicateMemberRow struct {
	mediaID  int64
	distance float64
}

type duplicateGroup struct {
	keep    int64
	members []duplicateMemberRow
}

func pairKey(a, b int64) [2]int64 {
	if a > b {
		a, b = b, a
	}
	return [2]int64{a, b}
}

func loadFingerprints(db *sql.DB) ([]fpEntry, error) {
	rows, err := db.Query(`SELECT id, fingerprint, width, height, date_taken, date_indexed, quality_score
		FROM media WHERE fingerprint != '' AND fingerprint != '-'`)
	if err != nil {
		return nil, fmt.Errorf("load fingerprints: %w", err)
	}
	defer rows.Close()
	var entries []fpEntry
	for rows.Next() {
		var e fpEntry
		if err := rows.Scan(&e.ID, &e.Fingerprint, &e.Width, &e.Height, &e.DateTaken, &e.DateIndexed, &e.QualityScore); err != nil {
			return nil, fmt.Errorf("load fingerprints: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func loadDuplicateExclusions(db *sql.DB) (map[[2]int64]bool, error) {
	rows, err := db.Query(`SELECT media_a, media_b FROM duplicate_exclusions`)
	if err != nil {
		return nil, fmt.Errorf("load duplicate exclusions: %w", err)
	}
	defer rows.Close()
	out := map[[2]int64]bool{}
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return nil, fmt.Errorf("load duplicate exclusions: %w", err)
		}
		out[[2]int64{a, b}] = true
	}
	return out, rows.Err()
}

func loadDuplicateKeeps(db *sql.DB) (map[int64]bool, error) {
	rows, err := db.Query(`SELECT media_id FROM duplicate_keeps`)
	if err != nil {
		return nil, fmt.Errorf("load duplicate keeps: %w", err)
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("load duplicate keeps: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// replaceDuplicateGroups makes the stored groups the given ones in one
// transaction. A group whose members are unchanged keeps its row, so a page
// open on it survives a scan.
func replaceDuplicateGroups(db *sql.DB, groups []duplicateGroup) error {
	return inTx(db, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT group_id, media_id FROM duplicate_members ORDER BY group_id, media_id`)
		if err != nil {
			return fmt.Errorf("load duplicate groups: %w", err)
		}
		existing := map[string]int64{}
		var (
			cur  int64 = -1
			key  strings.Builder
			stop error
		)
		flush := func() {
			if cur >= 0 {
				existing[key.String()] = cur
			}
			key.Reset()
		}
		for rows.Next() {
			var g, m int64
			if stop = rows.Scan(&g, &m); stop != nil {
				break
			}
			if g != cur {
				flush()
				cur = g
			}
			fmt.Fprintf(&key, "%d,", m)
		}
		flush()
		if err := errors.Join(stop, rows.Err(), rows.Close()); err != nil {
			return fmt.Errorf("load duplicate groups: %w", err)
		}

		keepIDs := map[int64]bool{}
		for _, g := range groups {
			ids := make([]int64, len(g.members))
			for i, m := range g.members {
				ids[i] = m.mediaID
			}
			slices.Sort(ids)
			var k strings.Builder
			for _, id := range ids {
				fmt.Fprintf(&k, "%d,", id)
			}
			groupID, ok := existing[k.String()]
			if ok {
				keepIDs[groupID] = true
				if _, err := tx.Exec(`UPDATE duplicate_members SET is_keep = (media_id = ?) WHERE group_id = ?`, g.keep, groupID); err != nil {
					return fmt.Errorf("update group %d: %w", groupID, err)
				}
				continue
			}
			res, err := tx.Exec(`INSERT INTO duplicate_groups DEFAULT VALUES`)
			if err != nil {
				return fmt.Errorf("insert group: %w", err)
			}
			if groupID, err = res.LastInsertId(); err != nil {
				return fmt.Errorf("insert group: %w", err)
			}
			keepIDs[groupID] = true
			for _, m := range g.members {
				if _, err := tx.Exec(`INSERT INTO duplicate_members (group_id, media_id, is_keep, distance) VALUES (?, ?, ?, ?)`,
					groupID, m.mediaID, m.mediaID == g.keep, m.distance); err != nil {
					return fmt.Errorf("add member: %w", err)
				}
			}
		}
		for _, groupID := range existing {
			if keepIDs[groupID] {
				continue
			}
			if _, err := tx.Exec(`DELETE FROM duplicate_groups WHERE id = ?`, groupID); err != nil {
				return fmt.Errorf("delete group %d: %w", groupID, err)
			}
		}
		return nil
	})
}

// betterKeep returns true if candidate is a better keep choice than current.
// Prefers: highest resolution → clearly sharper → oldest date. Resolution
// comes first because sharpness is measured on a downscaled copy, where a
// small copy of a photo looks as sharp as its original.
func betterKeep(candidate, current fpEntry) bool {
	cPx := candidate.Width * candidate.Height
	kPx := current.Width * current.Height
	if cPx != kPx {
		return cPx > kPx
	}
	cq, kq := candidate.QualityScore, current.QualityScore
	if cq > kq*1.1 || kq > cq*1.1 {
		return cq > kq
	}
	// Oldest date tiebreaker.
	cDate := candidate.DateIndexed
	if candidate.DateTaken != nil {
		cDate = *candidate.DateTaken
	}
	kDate := current.DateIndexed
	if current.DateTaken != nil {
		kDate = *current.DateTaken
	}
	return cDate < kDate
}

// UpdateQualityScore sets the quality score for a media item.
func UpdateQualityScore(db *sql.DB, mediaID int64, score float64) error {
	_, err := db.Exec(`UPDATE media SET quality_score = ? WHERE id = ?`, score, mediaID)
	return err
}
