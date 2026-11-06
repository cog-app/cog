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
	if !SettingDuplicateDetectionEnabled.MustGet(sysDB) {
		return nil
	}

	algorithm := SettingDuplicateAlgorithm.MustGet(sysDB)

	// Check if algorithm changed — invalidate old fingerprints.
	lastAlgo := SettingDuplicateAlgorithmUsed.MustGet(db)
	if lastAlgo != "" && lastAlgo != string(algorithm) {
		p.log.Info("duplicate algorithm changed, clearing fingerprints", "old", lastAlgo, "new", algorithm)
		if err := ClearAllFingerprints(db); err != nil {
			return fmt.Errorf("clear fingerprints: %w", err)
		}
	}
	if err := SettingDuplicateAlgorithmUsed.Set(db, string(algorithm)); err != nil {
		return fmt.Errorf("record duplicate algorithm: %w", err)
	}

	hasher, err := getHasher(algorithm)
	if err != nil {
		return err
	}

	rows, err := db.Query(`SELECT ` + mediaCols + ` FROM media m
		WHERE m.fingerprint = '' AND m.mime_type LIKE 'image/%' AND m.width > 0 LIMIT 100`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items, err := scanMediaRows(rows)
	if err != nil {
		return err
	}

	var computed int
	for i := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m := &items[i]
		id := m.ID

		f, err := p.openMedia(username, m.FilePath)
		if err != nil {
			p.log.Warn("fingerprint: open failed", "id", id, "err", err)
			continue
		}

		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			p.log.Debug("fingerprint: decode failed", "id", id, "err", err)
			// Mark with a sentinel so we don't retry.
			if err := UpdateFingerprint(db, id, "-"); err != nil {
				return fmt.Errorf("mark fingerprint %d undecodable: %w", id, err)
			}
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

	maxDist := SettingDuplicateDistance.MustGet(sysDB)

	// Load all valid fingerprints.
	rows, err := db.Query(`SELECT id, fingerprint, width, height, date_taken, date_indexed, quality_score
		FROM media WHERE fingerprint != '' AND fingerprint != '-'`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var entries []fpEntry
	for rows.Next() {
		var e fpEntry
		if err := rows.Scan(&e.ID, &e.Fingerprint, &e.Width, &e.Height, &e.DateTaken, &e.DateIndexed, &e.QualityScore); err != nil {
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
			p.log.Warn("duplicates: bad fingerprint, skipping", "id", e.ID, "err", err)
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

		// Select keep: highest quality, then resolution, then oldest date.
		keepIdx := members[0]
		for _, idx := range members[1:] {
			if betterKeep(entries[idx], entries[keepIdx]) {
				keepIdx = idx
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

// betterKeep returns true if candidate is a better keep choice than current.
// Prefers: highest quality score → highest resolution → oldest date.
func betterKeep(candidate, current fpEntry) bool {
	// Quality score: higher is better, -1 means unscored (worst).
	cq, kq := candidate.QualityScore, current.QualityScore
	if cq != kq {
		return cq > kq
	}
	// Resolution tiebreaker.
	cPx := candidate.Width * candidate.Height
	kPx := current.Width * current.Height
	if cPx != kPx {
		return cPx > kPx
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

// laplacianVariance computes the variance of the Laplacian-filtered image,
// a measure of sharpness. Higher values indicate sharper images.
func laplacianVariance(img image.Image) float64 {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w < 3 || h < 3 {
		return 0
	}

	// Apply Laplacian kernel [[0,1,0],[1,-4,1],[0,1,0]] on grayscale.
	var sum, sumSq float64
	n := float64((w - 2) * (h - 2))
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			c := gray(img, bounds.Min.X+x, bounds.Min.Y+y)
			u := gray(img, bounds.Min.X+x, bounds.Min.Y+y-1)
			d := gray(img, bounds.Min.X+x, bounds.Min.Y+y+1)
			l := gray(img, bounds.Min.X+x-1, bounds.Min.Y+y)
			r := gray(img, bounds.Min.X+x+1, bounds.Min.Y+y)
			lap := u + d + l + r - 4*c
			sum += lap
			sumSq += lap * lap
		}
	}
	mean := sum / n
	return sumSq/n - mean*mean
}

func gray(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535.0
}

// UpdateQualityScore sets the quality score for a media item.
func UpdateQualityScore(db *sql.DB, mediaID int64, score float64) error {
	_, err := db.Exec(`UPDATE media SET quality_score = ? WHERE id = ?`, score, mediaID)
	return err
}
