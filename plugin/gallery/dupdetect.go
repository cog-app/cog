package gallery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"math"
	"math/bits"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/imaging"
)

// DuplicateKind says how alike a group's photos are, and so how closely
// someone needs to look before deleting any.
type DuplicateKind int

const (
	DuplicateSimilar     DuplicateKind = iota // look alike: bursts, edits, crops
	DuplicateIdentical                        // the same bytes
	DuplicateSamePicture                      // the same pixels, saved differently
	DuplicateSmaller                          // the same picture, scaled down
)

// DuplicateKinds is every kind, in the order they are reviewed.
var DuplicateKinds = []DuplicateKind{DuplicateIdentical, DuplicateSamePicture, DuplicateSmaller, DuplicateSimilar}

func (k DuplicateKind) Slug() string {
	switch k {
	case DuplicateIdentical:
		return "identical"
	case DuplicateSamePicture:
		return "same"
	case DuplicateSmaller:
		return "smaller"
	}
	return "similar"
}

func ParseDuplicateKind(s string) (DuplicateKind, bool) {
	for _, k := range DuplicateKinds {
		if k.Slug() == s {
			return k, true
		}
	}
	return 0, false
}

// Tab is the kind's name where there is little room.
func (k DuplicateKind) Tab() string {
	switch k {
	case DuplicateIdentical:
		return "Identical"
	case DuplicateSamePicture:
		return "Same"
	case DuplicateSmaller:
		return "Smaller"
	}
	return "Similar"
}

func (k DuplicateKind) Label() string {
	switch k {
	case DuplicateIdentical:
		return "Identical files"
	case DuplicateSamePicture:
		return "Same picture"
	case DuplicateSmaller:
		return "Smaller copies"
	}
	return "Similar"
}

const (
	// copyScore is the PSNR, in dB between two photos' thumbnails, at which
	// they are the same picture. Measured on a library's thumbnails, copies
	// scored 36–48, bursts at most 25 and edits 27–35.
	copyScore = 36
	// flatThumb is the spread of grey levels below which a thumbnail is
	// too plain to compare: a dark pocket shot matches every other one.
	flatThumb = 8
	// identicalScore stands for pixels that match exactly.
	identicalScore = 99
	// duplicateBudget bounds the hashing and measuring one scan does, so a
	// new library's first pass does not hold up the scans after it.
	duplicateBudget = 10 * time.Minute
)

// dupEntry is a photo or video as duplicate detection sees it.
type dupEntry struct {
	ID           int64
	Path         string
	Fingerprint  string
	Width        int
	Height       int
	DateTaken    *int64
	DateIndexed  int64
	QualityScore float64
	ContentKey   string
	Hash         string // whole-file SHA-256, when another file shares the content key
	HasGPS       bool
	Size         int64
	words        []uint64
}

func (p *Plugin) detectDuplicates(ctx context.Context, db, filesDB *sql.DB, username string) (remaining bool, err error) {
	sysDB, err := p.SystemDB()
	if err != nil {
		return false, fmt.Errorf("system db: %w", err)
	}
	if !SettingDuplicateDetectionEnabled.MustGet(sysDB) {
		return false, nil
	}
	deadline := time.Now().Add(duplicateBudget)
	remaining, err = p.hashSharedKeys(ctx, db, username, deadline)
	if err != nil {
		return false, err
	}
	fp, err := p.filesPlugin()
	if err != nil {
		return remaining, err
	}
	thumb := func(e *dupEntry) (image.Image, error) {
		f, err := fp.Preview(username, e.Path, 256)
		if err != nil {
			return nil, err
		}
		img, err := jpeg.Decode(f)
		return img, errors.Join(err, f.Close())
	}
	scorer, err := newPixelScorer(db, thumb, deadline, p.log)
	if err != nil {
		return remaining, err
	}
	if err := rebuildDuplicateGroups(db, filesDB, SettingDuplicateDistance.MustGet(sysDB), scorer.score, p.log); err != nil {
		return remaining, err
	}
	if err := scorer.save(db); err != nil {
		return remaining, err
	}
	return remaining || scorer.pending, nil
}

// hashSharedKeys takes the whole-file hash of every file whose content key
// another file shares, until the deadline. The key reads only a file's
// ends, so two files with one key may still differ in between.
func (p *Plugin) hashSharedKeys(ctx context.Context, db *sql.DB, username string, deadline time.Time) (remaining bool, err error) {
	rows, err := db.Query(`SELECT m.id, m.file_path, m.content_key FROM media m
		WHERE m.shared_from = '' AND m.content_key IN (
			SELECT content_key FROM media WHERE shared_from = '' AND content_key != ''
			GROUP BY content_key HAVING COUNT(*) > 1)
		AND NOT EXISTS (SELECT 1 FROM media_hashes h WHERE h.media_id = m.id AND h.content_key = m.content_key)`)
	if err != nil {
		return false, fmt.Errorf("list files to hash: %w", err)
	}
	type todo struct {
		id        int64
		path, key string
	}
	var work []todo
	for rows.Next() {
		var t todo
		if err := rows.Scan(&t.id, &t.path, &t.key); err != nil {
			return false, errors.Join(fmt.Errorf("list files to hash: %w", err), rows.Close())
		}
		work = append(work, t)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, fmt.Errorf("list files to hash: %w", err)
	}

	var hashed, failed int
	var firstErr error
	defer func() {
		if failed > 0 {
			p.log.Warn("hash duplicate candidates", "user", username, "failed", failed, "first", firstErr)
		}
		if hashed > 0 {
			p.log.Info("hashed duplicate candidates", "user", username, "hashed", hashed, "remaining", remaining)
		}
	}()
	for _, t := range work {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		if time.Now().After(deadline) {
			return true, nil
		}
		sum, err := p.hashMediaFile(username, t.path)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", t.path, err)
			}
			continue
		}
		if _, err := db.Exec(`INSERT OR REPLACE INTO media_hashes (media_id, content_key, sha256) VALUES (?, ?, ?)`,
			t.id, t.key, sum); err != nil {
			return false, fmt.Errorf("store hash of %s: %w", t.path, err)
		}
		hashed++
	}
	return false, nil
}

func (p *Plugin) hashMediaFile(username, filePath string) (string, error) {
	local, err := p.resolveLocalPath(username, filePath)
	if err != nil {
		return "", err
	}
	f, err := os.Open(local)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	if err := errors.Join(err, f.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// rebuildDuplicateGroups finds the user's duplicates afresh and stores
// them, honouring what people decided about earlier groups. score measures
// a pair of photos; nil measures none, so no photo is a copy of another.
func rebuildDuplicateGroups(db, filesDB *sql.DB, maxDist int, score pairScore, log *slog.Logger) error {
	entries, err := loadDuplicateEntries(db, filesDB, log)
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
	if score == nil {
		score = func(a, b *dupEntry) (float64, bool) { return 0, false }
	}
	return replaceDuplicateGroups(db, planDuplicateGroups(entries, maxDist, excluded, kept, score))
}

// pairScore is how alike two photos' pixels are, in dB, and whether it
// could be measured.
type pairScore func(a, b *dupEntry) (float64, bool)

// planDuplicateGroups sorts photos into groups in three passes, each
// working on what the one before left standing: identical files, then
// copies of the same picture, then photos that merely look alike. A group
// stands in the later passes by the photo it keeps, so a picture saved
// three times and scaled down once shows up once as identical and once as
// smaller, not as one muddle.
//
// Every photo in a group is close to every other one. Joining a photo to a
// group because it is close to one member let chains form: 91 photos
// across 18 years, each like the next, with nothing in common end to end.
func planDuplicateGroups(entries []dupEntry, maxDist int, excluded map[[2]int64]bool, kept map[int64]bool, score pairScore) []duplicateGroup {
	stands := make([]bool, len(entries))
	for i := range stands {
		stands[i] = true
	}
	notExcluded := func(a, b int) bool { return !excluded[pairKey(entries[a].ID, entries[b].ID)] }
	var groups []duplicateGroup
	add := func(kind DuplicateKind, members []int, better func(a, b *dupEntry) bool, pairScore func(a, b int) float64) {
		keep := pickKeep(entries, members, kept, better)
		g := duplicateGroup{kind: kind, keep: entries[keep].ID}
		for _, m := range members {
			if m != keep {
				stands[m] = false
			}
			row := duplicateMemberRow{mediaID: entries[m].ID, score: -1}
			if entries[m].words != nil && entries[keep].words != nil {
				row.distance = float64(hammingWords(entries[m].words, entries[keep].words))
			}
			if pairScore != nil && m != keep {
				row.score = pairScore(m, keep)
			}
			g.members = append(g.members, row)
		}
		groups = append(groups, g)
	}

	byHash := map[string][]int{}
	var hashes []string
	for i, e := range entries {
		if e.Hash == "" {
			continue
		}
		if _, ok := byHash[e.Hash]; !ok {
			hashes = append(hashes, e.Hash)
		}
		byHash[e.Hash] = append(byHash[e.Hash], i)
	}
	for _, h := range hashes {
		same := byHash[h]
		if len(same) < 2 {
			continue
		}
		var edges []dupEdge
		for x, a := range same {
			for _, b := range same[x+1:] {
				if notExcluded(a, b) {
					edges = append(edges, dupEdge{a, b, 0})
				}
			}
		}
		for _, c := range completeLink(edges) {
			add(DuplicateIdentical, c, nameBetter, func(int, int) float64 { return identicalScore })
		}
	}

	var near []dupEdge
	for i := range entries {
		if !stands[i] || entries[i].words == nil {
			continue
		}
		for j := i + 1; j < len(entries); j++ {
			if !stands[j] || len(entries[j].words) != len(entries[i].words) {
				continue
			}
			d := hammingWords(entries[i].words, entries[j].words)
			if d > maxDist || !notExcluded(i, j) {
				continue
			}
			near = append(near, dupEdge{i, j, float64(d)})
		}
	}

	scores := map[[2]int]float64{}
	var copies []dupEdge
	for _, e := range near {
		if !sameShape(&entries[e.a], &entries[e.b]) {
			continue
		}
		s, ok := score(&entries[e.a], &entries[e.b])
		if !ok {
			continue
		}
		scores[[2]int{e.a, e.b}] = s
		if s >= copyScore {
			copies = append(copies, dupEdge{e.a, e.b, -s})
		}
	}
	known := func(a, b int) float64 {
		if a > b {
			a, b = b, a
		}
		if s, ok := scores[[2]int{a, b}]; ok {
			return s
		}
		return -1
	}
	for _, c := range completeLink(copies) {
		kind := DuplicateSamePicture
		px := entries[c[0]].Width * entries[c[0]].Height
		for _, m := range c[1:] {
			if entries[m].Width*entries[m].Height != px {
				kind = DuplicateSmaller
			}
		}
		add(kind, c, copyBetter, known)
	}

	var similar []dupEdge
	for _, e := range near {
		if stands[e.a] && stands[e.b] {
			similar = append(similar, e)
		}
	}
	for _, c := range completeLink(similar) {
		add(DuplicateSimilar, c, similarBetter, known)
	}
	return groups
}

// dupEdge links two entries, by index, that may share a group; the lower
// its rank, the sooner it is considered.
type dupEdge struct {
	a, b int
	rank float64
}

// completeLink groups the entries the edges link so that every two in a
// group are linked, taking the edges best first. Groups of one are left out.
func completeLink(edges []dupEdge) [][]int {
	slices.SortStableFunc(edges, func(x, y dupEdge) int {
		switch {
		case x.rank < y.rank:
			return -1
		case x.rank > y.rank:
			return 1
		}
		return 0
	})
	linked := make(map[[2]int]bool, len(edges))
	for _, e := range edges {
		linked[[2]int{min(e.a, e.b), max(e.a, e.b)}] = true
	}
	cluster := map[int]int{}
	members := map[int][]int{}
	of := func(n int) int {
		if c, ok := cluster[n]; ok {
			return c
		}
		cluster[n] = n
		members[n] = []int{n}
		return n
	}
	for _, e := range edges {
		ca, cb := of(e.a), of(e.b)
		if ca == cb {
			continue
		}
		all := true
		for _, x := range members[ca] {
			for _, y := range members[cb] {
				if !linked[[2]int{min(x, y), max(x, y)}] {
					all = false
					break
				}
			}
			if !all {
				break
			}
		}
		if !all {
			continue
		}
		if len(members[ca]) < len(members[cb]) {
			ca, cb = cb, ca
		}
		for _, y := range members[cb] {
			cluster[y] = ca
		}
		members[ca] = append(members[ca], members[cb]...)
		delete(members, cb)
	}
	var out [][]int
	for _, m := range members {
		if len(m) >= 2 {
			slices.Sort(m)
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(x, y []int) int { return x[0] - y[0] })
	return out
}

// sameShape reports whether two photos have one aspect ratio, as a scaled
// copy keeps its original's.
func sameShape(a, b *dupEntry) bool {
	if a.Width <= 0 || a.Height <= 0 || b.Width <= 0 || b.Height <= 0 {
		return false
	}
	if (a.Width >= a.Height) != (b.Width >= b.Height) {
		return false
	}
	ra := float64(a.Width) / float64(a.Height)
	rb := float64(b.Width) / float64(b.Height)
	return math.Abs(ra-rb) <= 0.02*ra
}

// pickKeep chooses the member to keep: one someone chose, else the best.
func pickKeep(entries []dupEntry, members []int, kept map[int64]bool, better func(a, b *dupEntry) bool) int {
	keep := members[0]
	for _, m := range members[1:] {
		if kept[entries[m].ID] != kept[entries[keep].ID] {
			if kept[entries[m].ID] {
				keep = m
			}
			continue
		}
		if better(&entries[m], &entries[keep]) {
			keep = m
		}
	}
	return keep
}

// copySuffix is what copying a file adds to its name: "_1" from a rename
// that found the name taken, "(1)" from a browser, "-001" from Windows.
var copySuffix = regexp.MustCompile(`(?i)(_\d{1,3}|\(\d+\)|-\d{3}| - copy| copy)$`)

func isCopyName(p string) bool {
	base := path.Base(p)
	return copySuffix.MatchString(strings.TrimSuffix(base, path.Ext(base)))
}

// nameBetter prefers, of files with the same bytes, the one whose name does
// not look copied, then the shorter path, so the original name stays.
func nameBetter(a, b *dupEntry) bool {
	if ca, cb := isCopyName(a.Path), isCopyName(b.Path); ca != cb {
		return !ca
	}
	if len(a.Path) != len(b.Path) {
		return len(a.Path) < len(b.Path)
	}
	return a.Path < b.Path
}

// metadata counts what a photo says about itself that a copy may have lost.
func (e *dupEntry) metadata() int {
	n := 0
	if e.DateTaken != nil {
		n++
	}
	if e.HasGPS {
		n++
	}
	return n
}

// copyBetter prefers, of copies of one picture, the largest, then the one
// that kept its date and place, then the bigger file, which was compressed
// less.
func copyBetter(a, b *dupEntry) bool {
	if pa, pb := a.Width*a.Height, b.Width*b.Height; pa != pb {
		return pa > pb
	}
	if ma, mb := a.metadata(), b.metadata(); ma != mb {
		return ma > mb
	}
	if a.Size != b.Size {
		return a.Size > b.Size
	}
	return nameBetter(a, b)
}

// similarBetter suggests which of photos that only look alike to keep:
// highest resolution, then the one that knows its date and place, then
// clearly sharper, then oldest. Resolution comes before sharpness because
// sharpness is measured on a downscaled copy, where a small copy of a
// photo looks as sharp as its original.
func similarBetter(a, b *dupEntry) bool {
	if pa, pb := a.Width*a.Height, b.Width*b.Height; pa != pb {
		return pa > pb
	}
	if ma, mb := a.metadata(), b.metadata(); ma != mb {
		return ma > mb
	}
	qa, qb := a.QualityScore, b.QualityScore
	if qa > qb*1.1 || qb > qa*1.1 {
		return qa > qb
	}
	if da, db := a.when(), b.when(); da != db {
		return da < db
	}
	return nameBetter(a, b)
}

func (e *dupEntry) when() int64 {
	if e.DateTaken != nil {
		return *e.DateTaken
	}
	return e.DateIndexed
}

// packWords packs a fingerprint into 64-bit words. Every pair of photos is
// compared, and similarity.Hamming, a byte at a time, took 80 seconds over
// 34,000 of them.
func packWords(h []byte) []uint64 {
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
	score    float64
}

type duplicateGroup struct {
	kind    DuplicateKind
	keep    int64
	members []duplicateMemberRow
}

func pairKey(a, b int64) [2]int64 {
	if a > b {
		a, b = b, a
	}
	return [2]int64{a, b}
}

// loadDuplicateEntries reads the user's own photos and videos that have a
// fingerprint or a whole-file hash; photos shared from someone else's
// gallery are theirs to sort out. filesDB, when there is one, gives sizes.
func loadDuplicateEntries(db, filesDB *sql.DB, log *slog.Logger) ([]dupEntry, error) {
	rows, err := db.Query(`SELECT m.id, m.file_id, m.file_path, m.fingerprint, m.width, m.height,
			m.date_taken, m.date_indexed, m.quality_score, m.content_key, COALESCE(h.sha256, ''),
			m.latitude IS NOT NULL
		FROM media m LEFT JOIN media_hashes h ON h.media_id = m.id AND h.content_key = m.content_key
		WHERE m.shared_from = '' AND ((m.fingerprint != '' AND m.fingerprint != '-') OR h.sha256 IS NOT NULL)
		ORDER BY m.id`)
	if err != nil {
		return nil, fmt.Errorf("load duplicate candidates: %w", err)
	}
	defer rows.Close()
	var entries []dupEntry
	var fileIDs []int64
	for rows.Next() {
		var e dupEntry
		var fileID int64
		if err := rows.Scan(&e.ID, &fileID, &e.Path, &e.Fingerprint, &e.Width, &e.Height, &e.DateTaken,
			&e.DateIndexed, &e.QualityScore, &e.ContentKey, &e.Hash, &e.HasGPS); err != nil {
			return nil, fmt.Errorf("load duplicate candidates: %w", err)
		}
		if e.Fingerprint != "" && e.Fingerprint != fingerprintFailed {
			b, err := hex.DecodeString(e.Fingerprint)
			if err != nil {
				log.Warn("duplicates: bad fingerprint, skipping", "id", e.ID, "err", err)
			} else {
				e.words = packWords(b)
			}
		}
		entries = append(entries, e)
		fileIDs = append(fileIDs, fileID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load duplicate candidates: %w", err)
	}
	if filesDB == nil {
		return entries, nil
	}
	sizes, err := fileSizes(filesDB)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].Size = sizes[fileIDs[i]]
	}
	return entries, nil
}

// fileSizes is every file's size by id, read at once: the files a scan
// compares are most of them.
func fileSizes(filesDB *sql.DB) (map[int64]int64, error) {
	rows, err := filesDB.Query(`SELECT id, size FROM files WHERE is_dir = 0`)
	if err != nil {
		return nil, fmt.Errorf("load file sizes: %w", err)
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, size int64
		if err := rows.Scan(&id, &size); err != nil {
			return nil, fmt.Errorf("load file sizes: %w", err)
		}
		out[id] = size
	}
	return out, rows.Err()
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
		type stored struct {
			isKeep          bool
			distance, score float64
		}
		rows, err := tx.Query(`SELECT group_id, media_id, is_keep, distance, score FROM duplicate_members ORDER BY group_id, media_id`)
		if err != nil {
			return fmt.Errorf("load duplicate groups: %w", err)
		}
		existing := map[string]int64{}
		members := map[[2]int64]stored{}
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
			var s stored
			if stop = rows.Scan(&g, &m, &s.isKeep, &s.distance, &s.score); stop != nil {
				break
			}
			if g != cur {
				flush()
				cur = g
			}
			fmt.Fprintf(&key, "%d,", m)
			members[[2]int64{g, m}] = s
		}
		flush()
		if err := errors.Join(stop, rows.Err(), rows.Close()); err != nil {
			return fmt.Errorf("load duplicate groups: %w", err)
		}

		update, err := tx.Prepare(`UPDATE duplicate_members SET is_keep = ?, distance = ?, score = ? WHERE group_id = ? AND media_id = ?`)
		if err != nil {
			return fmt.Errorf("prepare member update: %w", err)
		}
		defer update.Close()
		insert, err := tx.Prepare(`INSERT INTO duplicate_members (group_id, media_id, is_keep, distance, score) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare member insert: %w", err)
		}
		defer insert.Close()

		stays := map[int64]bool{}
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
				stays[groupID] = true
				if _, err := tx.Exec(`UPDATE duplicate_groups SET kind = ? WHERE id = ? AND kind != ?`, g.kind, groupID, g.kind); err != nil {
					return fmt.Errorf("update group %d: %w", groupID, err)
				}
				for _, m := range g.members {
					want := stored{m.mediaID == g.keep, m.distance, m.score}
					if members[[2]int64{groupID, m.mediaID}] == want {
						continue
					}
					if _, err := update.Exec(want.isKeep, want.distance, want.score, groupID, m.mediaID); err != nil {
						return fmt.Errorf("update group %d: %w", groupID, err)
					}
				}
				continue
			}
			res, err := tx.Exec(`INSERT INTO duplicate_groups (kind) VALUES (?)`, g.kind)
			if err != nil {
				return fmt.Errorf("insert group: %w", err)
			}
			if groupID, err = res.LastInsertId(); err != nil {
				return fmt.Errorf("insert group: %w", err)
			}
			stays[groupID] = true
			for _, m := range g.members {
				if _, err := insert.Exec(groupID, m.mediaID, m.mediaID == g.keep, m.distance, m.score); err != nil {
					return fmt.Errorf("add member: %w", err)
				}
			}
		}
		for _, groupID := range existing {
			if stays[groupID] {
				continue
			}
			if _, err := tx.Exec(`DELETE FROM duplicate_groups WHERE id = ?`, groupID); err != nil {
				return fmt.Errorf("delete group %d: %w", groupID, err)
			}
		}
		return nil
	})
}

// pixelScorer measures how alike two photos look from their thumbnails,
// and keeps each measurement for the next scan.
type pixelScorer struct {
	known    map[[2]int64]float64
	fresh    []measuredPair
	thumb    func(e *dupEntry) (image.Image, error)
	thumbs   map[int64]image.Image
	deadline time.Time
	pending  bool // some pair was left unmeasured for want of time
	log      *slog.Logger
}

type measuredPair struct {
	a, b       int64
	keyA, keyB string
	score      float64
}

// thumbCacheSize bounds the decoded thumbnails held at once, about 75 KB
// each; the pairs come in order of their first photo, so a small cache
// serves most of them.
const thumbCacheSize = 1024

func newPixelScorer(db *sql.DB, thumb func(e *dupEntry) (image.Image, error), deadline time.Time, log *slog.Logger) (*pixelScorer, error) {
	rows, err := db.Query(`SELECT p.media_a, p.media_b, p.score FROM duplicate_pairs p
		JOIN media a ON a.id = p.media_a AND a.content_key = p.key_a
		JOIN media b ON b.id = p.media_b AND b.content_key = p.key_b`)
	if err != nil {
		return nil, fmt.Errorf("load pixel scores: %w", err)
	}
	defer rows.Close()
	s := &pixelScorer{known: map[[2]int64]float64{}, thumb: thumb, thumbs: map[int64]image.Image{}, deadline: deadline, log: log}
	for rows.Next() {
		var a, b int64
		var score float64
		if err := rows.Scan(&a, &b, &score); err != nil {
			return nil, fmt.Errorf("load pixel scores: %w", err)
		}
		s.known[[2]int64{a, b}] = score
	}
	return s, rows.Err()
}

// score is a pairScore. A pair that cannot be measured — a thumbnail that
// will not load, or one too plain to tell apart — scores below any copy.
func (s *pixelScorer) score(a, b *dupEntry) (float64, bool) {
	if a.ID > b.ID {
		a, b = b, a
	}
	k := [2]int64{a.ID, b.ID}
	if v, ok := s.known[k]; ok {
		return v, true
	}
	if time.Now().After(s.deadline) {
		s.pending = true
		return 0, false
	}
	v := -1.0
	ia, errA := s.image(a)
	ib, errB := s.image(b)
	if err := errors.Join(errA, errB); err != nil {
		s.log.Debug("duplicates: compare thumbnails", "a", a.Path, "b", b.Path, "err", err)
	} else {
		v = thumbScore(ia, ib)
	}
	s.known[k] = v
	s.fresh = append(s.fresh, measuredPair{a.ID, b.ID, a.ContentKey, b.ContentKey, v})
	return v, true
}

func (s *pixelScorer) image(e *dupEntry) (image.Image, error) {
	if img, ok := s.thumbs[e.ID]; ok {
		return img, nil
	}
	img, err := s.thumb(e)
	if err != nil {
		return nil, err
	}
	if len(s.thumbs) >= thumbCacheSize {
		clear(s.thumbs)
	}
	s.thumbs[e.ID] = img
	return img, nil
}

// save stores the pairs measured this time.
func (s *pixelScorer) save(db *sql.DB) error {
	if len(s.fresh) == 0 {
		return nil
	}
	return inTx(db, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT OR REPLACE INTO duplicate_pairs (media_a, media_b, key_a, key_b, score) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("store pixel scores: %w", err)
		}
		defer stmt.Close()
		for _, p := range s.fresh {
			if _, err := stmt.Exec(p.a, p.b, p.keyA, p.keyB, p.score); err != nil {
				return fmt.Errorf("store pixel score: %w", err)
			}
		}
		return nil
	})
}

// thumbScore is the PSNR between two thumbnails' grey levels, once both are
// scaled to the smaller's size, or -1 when either is too plain to compare.
func thumbScore(a, b image.Image) float64 {
	ba, bb := a.Bounds(), b.Bounds()
	w, h := min(ba.Dx(), bb.Dx()), min(ba.Dy(), bb.Dy())
	if w < 8 || h < 8 {
		return -1
	}
	ga, gb := greyLevels(imaging.Resize(a, w, h)), greyLevels(imaging.Resize(b, w, h))
	if spread(ga) < flatThumb || spread(gb) < flatThumb {
		return -1
	}
	var sum float64
	for i := range ga {
		d := ga[i] - gb[i]
		sum += d * d
	}
	mse := sum / float64(len(ga))
	if mse == 0 {
		return identicalScore
	}
	return min(identicalScore, 10*math.Log10(255*255/mse))
}

func greyLevels(img *image.RGBA) []float64 {
	out := make([]float64, 0, len(img.Pix)/4)
	for i := 0; i+3 < len(img.Pix); i += 4 {
		out = append(out, 0.299*float64(img.Pix[i])+0.587*float64(img.Pix[i+1])+0.114*float64(img.Pix[i+2]))
	}
	return out
}

// spread is the standard deviation of v.
func spread(v []float64) float64 {
	var mean float64
	for _, x := range v {
		mean += x
	}
	mean /= float64(len(v))
	var sum float64
	for _, x := range v {
		sum += (x - mean) * (x - mean)
	}
	return math.Sqrt(sum / float64(len(v)))
}
