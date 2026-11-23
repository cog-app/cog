package gallery

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	"git.duckfam.us/jj-software/cog/plugin/files"
)

// RenameData is the template context for file rename patterns.
type RenameData struct {
	Year     string
	Month    string
	Day      string
	Hour     string
	Minute   string
	Second   string
	Camera   string
	Lens     string
	Original string // filename without extension
	Ext      string // e.g. ".jpg"
	Place    string
	Country  string
	Width    int
	Height   int
}

// baseRenamePattern is the default pattern below the first scan folder.
const baseRenamePattern = "{{.Year}}/{{.Month}}/{{.Year}}-{{.Month}}-{{.Day}}_{{.Hour}}{{.Minute}}{{.Second}}{{.Ext}}"

// DefaultRenamePattern is the pattern offered before one has been applied:
// the base pattern inside the first scan folder, so renamed photos stay
// where the gallery looks for them.
func DefaultRenamePattern(db *sql.DB) string {
	return scanFolderDir(ParseScanFolders(db)[0]) + baseRenamePattern
}

// RenamePattern is the pattern last applied, or the default.
func RenamePattern(db *sql.DB) string {
	if p := SettingRenamePattern.MustGet(db); p != "" {
		return p
	}
	return DefaultRenamePattern(db)
}

// scanFolderDir is the fixed directory a scan folder pattern starts with,
// with a trailing slash: "Photos/" for "Photos/" and "Camera/" for
// "Camera/*.jpg". It is empty when the pattern starts with a glob or is "/".
func scanFolderDir(pattern string) string {
	var dirs []string
	for seg := range strings.SplitSeq(TrimScanPath(pattern), "/") {
		if seg == "" || strings.ContainsAny(seg, `*?[\`) {
			break
		}
		dirs = append(dirs, seg)
	}
	if len(dirs) == 0 {
		return ""
	}
	return strings.Join(dirs, "/") + "/"
}

// RenamePreview is one planned rename.
type RenamePreview struct {
	MediaID  int64
	FileID   int64
	OldPath  string
	NewPath  string
	Numbered bool // the pattern's name was taken, so a number was added
}

// RenameSkip is a photo the rename leaves where it is, and why.
type RenameSkip struct {
	Path   string
	Reason string
}

// RenamePlan is what applying a pattern would do.
type RenamePlan struct {
	Renames []RenamePreview // in the gallery's date order
	Skipped []RenameSkip
	// Unscanned counts files in the scan folders the gallery has not
	// indexed yet. A rename only knows of indexed photos, so these stay put.
	Unscanned int
}

// PlanRename works out the new path of every photo for pattern, without
// touching any file. A new name is never one another file already has:
// a file in the way that is itself being renamed is moved first, and any
// other is a conflict that policy settles.
func PlanRename(db, filesDB *sql.DB, pattern string, policy ConflictPolicy) (*RenamePlan, error) {
	tmpl, err := template.New("rename").Parse(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid rename pattern: %w", err)
	}
	rows, err := db.Query(`SELECT ` + mediaCols + ` FROM media m ORDER BY COALESCE(m.date_taken, m.date_indexed), m.id`)
	if err != nil {
		return nil, err
	}
	items, err := scanMediaRows(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}

	plan := &RenamePlan{}
	unscanned, err := countUnscanned(db, filesDB, items)
	if err != nil {
		return nil, err
	}
	plan.Unscanned = unscanned

	type candidate struct {
		m      *Media
		target string
	}
	patterns := ParseScanFolders(db)
	var cands []candidate
	for i := range items {
		m := &items[i]
		// The scanner stores "/Photos/a.jpg"; renames before this one
		// stored "Photos/a.jpg" until the next scan put it right.
		m.FilePath = path.Clean("/" + m.FilePath)
		var buf strings.Builder
		if err := tmpl.Execute(&buf, buildRenameData(m)); err != nil {
			plan.Skipped = append(plan.Skipped, RenameSkip{m.FilePath, "The pattern failed: " + err.Error()})
			continue
		}
		target, ok := renameTarget(buf.String())
		switch {
		case !ok:
			plan.Skipped = append(plan.Skipped, RenameSkip{m.FilePath, "The pattern gave “" + buf.String() + "”, which is not a file name."})
			continue
		case target == m.FilePath:
			continue
		case !matchScanFolders(target, patterns):
			plan.Skipped = append(plan.Skipped, RenameSkip{m.FilePath, "“" + target + "” is outside the scan folders, so the gallery would lose the photo."})
			continue
		}
		cands = append(cands, candidate{m, target})
	}

	targets := make([]string, len(cands))
	for i, c := range cands {
		targets[i] = c.target
	}
	exists, err := pathChecker(filesDB, targets)
	if err != nil {
		return nil, err
	}

	// A conflict skipped leaves its file where it was, which can make a
	// name chosen earlier a conflict too, so skipping goes round until
	// nothing changes. Numbering skips nothing and settles in one pass.
	skipped := make(map[int]bool)
	for {
		moving := make(map[string]bool, len(cands))
		for i, c := range cands {
			if !skipped[i] {
				moving[c.m.FilePath] = true
			}
		}
		claimed := make(map[string]bool, len(cands))
		taken := func(p string) (bool, error) {
			if claimed[p] {
				return true, nil
			}
			if moving[p] {
				return false, nil
			}
			return exists(p)
		}
		var renames []RenamePreview
		changed := false
		for i, c := range cands {
			if skipped[i] {
				continue
			}
			target, numbered := c.target, false
			busy, err := taken(target)
			if err != nil {
				return nil, err
			}
			if busy {
				if policy == ConflictSkip {
					skipped[i] = true
					changed = true
					continue
				}
				ext := path.Ext(c.target)
				base := strings.TrimSuffix(c.target, ext)
				for n := 1; busy; n++ {
					target = fmt.Sprintf("%s_%d%s", base, n, ext)
					if busy, err = taken(target); err != nil {
						return nil, err
					}
				}
				numbered = true
			}
			claimed[target] = true
			renames = append(renames, RenamePreview{
				MediaID: c.m.ID, FileID: c.m.FileID,
				OldPath: c.m.FilePath, NewPath: target, Numbered: numbered,
			})
		}
		if !changed {
			plan.Renames = renames
			break
		}
	}
	for i, c := range cands {
		if skipped[i] {
			plan.Skipped = append(plan.Skipped, RenameSkip{c.m.FilePath, "“" + c.target + "” is already taken."})
		}
	}
	return plan, nil
}

// renameTarget turns a pattern's output into an absolute file path, or
// reports that it is not one: empty, a folder, or climbing out with "..".
func renameTarget(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, "/") {
		return "", false
	}
	for seg := range strings.SplitSeq(s, "/") {
		if seg == ".." {
			return "", false
		}
	}
	p := path.Clean("/" + s)
	if p == "/" {
		return "", false
	}
	return p, true
}

// pathChecker reports whether a path is taken in the file index. The
// likely names are looked up together; any other, such as a numbered
// alternative, is looked up when asked.
func pathChecker(filesDB *sql.DB, likely []string) (func(string) (bool, error), error) {
	known := make(map[string]bool, len(likely))
	const chunk = 500
	for start := 0; start < len(likely); start += chunk {
		batch := likely[start:min(start+chunk, len(likely))]
		args := make([]any, len(batch))
		for i, p := range batch {
			args[i] = p
			known[p] = false
		}
		rows, err := filesDB.Query(`SELECT path FROM files WHERE path IN (?`+strings.Repeat(",?", len(batch)-1)+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("look up rename targets: %w", err)
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, err
			}
			known[p] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return func(p string) (bool, error) {
		if v, ok := known[p]; ok {
			return v, nil
		}
		var one int
		err := filesDB.QueryRow(`SELECT 1 FROM files WHERE path = ?`, p).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			known[p] = false
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("look up %s: %w", p, err)
		}
		known[p] = true
		return true, nil
	}, nil
}

// countUnscanned counts photos and videos in the scan folders that are not
// in media yet.
func countUnscanned(db, filesDB *sql.DB, media []Media) (int, error) {
	indexed := make(map[int64]bool, len(media))
	for _, m := range media {
		indexed[m.FileID] = true
	}
	rows, err := filesDB.Query(`SELECT id, path FROM files
		WHERE is_dir = 0 AND (mime_type LIKE 'image/%' OR mime_type LIKE 'video/%')`)
	if err != nil {
		return 0, fmt.Errorf("list media files: %w", err)
	}
	defer rows.Close()
	patterns := ParseScanFolders(db)
	n := 0
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return 0, err
		}
		if !indexed[id] && matchScanFolders(p, patterns) {
			n++
		}
	}
	return n, rows.Err()
}

// renameStep is one move of a file: a rename, or half of one that goes
// through a temporary name to break a cycle.
type renameStep struct {
	MediaID, FileID int64
	From, To        string
}

// renameSteps orders renames so that none lands on a file still waiting to
// move: a file whose new name another file holds moves after that one. A
// cycle of files taking each other's names is broken by parking one of
// them under a temporary name first.
func renameSteps(renames []RenamePreview) []renameStep {
	bySource := make(map[string]int, len(renames))
	for i, r := range renames {
		bySource[r.OldPath] = i
	}
	const (
		unvisited = iota
		visiting
		done
	)
	state := make([]int, len(renames))
	at := make([]string, len(renames)) // where each file is now
	for i, r := range renames {
		at[i] = r.OldPath
	}
	var steps []renameStep
	var visit func(i int)
	visit = func(i int) {
		state[i] = visiting
		if j, ok := bySource[renames[i].NewPath]; ok && j != i {
			switch state[j] {
			case unvisited:
				visit(j)
			case visiting:
				tmp := fmt.Sprintf("%s.cog-rename-%d", renames[j].OldPath, j)
				steps = append(steps, renameStep{renames[j].MediaID, renames[j].FileID, at[j], tmp})
				at[j] = tmp
			}
		}
		steps = append(steps, renameStep{renames[i].MediaID, renames[i].FileID, at[i], renames[i].NewPath})
		state[i] = done
	}
	for i := range renames {
		if state[i] == unvisited {
			visit(i)
		}
	}
	return steps
}

// moveNoReplace renames from to to, leaving alone any file already at to —
// including one written there since the plan was made, or one the file
// index has not caught up with.
func moveNoReplace(fp *files.Plugin, username, from, to string) error {
	_, err := fp.RenameNoReplace(username, from, to)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s is already taken: %w", to, err)
	}
	return err
}

// RenameKind is a group of renames that move files the same way: the same
// folders, up to their numbers and at most a folder level apart, with the
// file names treated the same. A "*" segment is one that differed.
type RenameKind struct {
	From, To string
	Count    int
	Numbered int
	Example  RenamePreview
}

type renameKind struct {
	from, to        []string
	count, numbered int
	widened         bool
	example         RenamePreview
}

var digitRun = regexp.MustCompile(`[0-9]+`)

// renameShape is a rename's path segments with numbers generalised and the
// file name told by how it changes rather than what it is.
func renameShape(p RenamePreview) (from, to []string) {
	oldDir, oldBase := path.Split(p.OldPath)
	newDir, newBase := path.Split(p.NewPath)
	if p.Numbered {
		ext := path.Ext(newBase)
		if i := strings.LastIndexByte(strings.TrimSuffix(newBase, ext), '_'); i >= 0 {
			newBase = newBase[:i] + ext
		}
	}
	segments := func(dir string) []string {
		var out []string
		for seg := range strings.SplitSeq(strings.Trim(dir, "/"), "/") {
			if seg != "" {
				out = append(out, digitRun.ReplaceAllString(seg, "#"))
			}
		}
		return out
	}
	from, to = segments(oldDir), segments(newDir)
	if newBase == oldBase {
		from, to = append(from, "{name}"), append(to, "{name}")
	} else {
		from, to = append(from, digitRun.ReplaceAllString(oldBase, "#")), append(to, digitRun.ReplaceAllString(newBase, "#"))
	}
	return from, to
}

// absorbs reports whether a rename of the given shape belongs to the kind:
// the same depth on both sides and at most one segment apart, never the
// top folder, which is what tells one source or destination from another.
// A kind widens by one segment only, or merging one neighbour at a time
// would chain every kind into "*/*/*".
func (k *renameKind) absorbs(from, to []string) (side []string, at int, ok bool) {
	if len(from) != len(k.from) || len(to) != len(k.to) {
		return nil, 0, false
	}
	diffs := 0
	for _, pair := range []struct{ kind, shape []string }{{k.from, from}, {k.to, to}} {
		for i := range pair.shape {
			if pair.kind[i] == "*" || pair.kind[i] == pair.shape[i] {
				continue
			}
			if i == 0 && len(pair.shape) > 1 {
				return nil, 0, false
			}
			diffs++
			side, at = pair.kind, i
		}
	}
	if diffs == 1 && k.widened {
		return nil, 0, false
	}
	return side, at, diffs <= 1
}

// RenameSummary is a long rename plan told by kinds rather than file by
// file. Other counts renames of no kind once the limit of kinds was found.
type RenameSummary struct {
	Kinds []RenameKind
	Other int
}

// renameDetailLimit is how many renames, or kinds of them, a preview lists.
const renameDetailLimit = 100

// SummarizeRenames groups renames into at most max kinds, largest first.
func SummarizeRenames(previews []RenamePreview, max int) RenameSummary {
	var sum RenameSummary
	var found []*renameKind
	seen := map[string]*renameKind{}
	for _, p := range previews {
		from, to := renameShape(p)
		key := strings.Join(from, "/") + "\x00" + strings.Join(to, "/")
		k := seen[key]
		if k == nil {
			for _, candidate := range found {
				if side, at, ok := candidate.absorbs(from, to); ok {
					if side != nil {
						side[at] = "*"
						candidate.widened = true
					}
					k = candidate
					break
				}
			}
		}
		if k == nil {
			if len(found) == max {
				sum.Other++
				continue
			}
			k = &renameKind{from: from, to: to, example: p}
			found = append(found, k)
		}
		seen[key] = k
		k.count++
		if p.Numbered {
			k.numbered++
		}
	}
	slices.SortStableFunc(found, func(a, b *renameKind) int { return b.count - a.count })
	for _, k := range found {
		sum.Kinds = append(sum.Kinds, RenameKind{
			From:     "/" + strings.Join(k.from, "/"),
			To:       "/" + strings.Join(k.to, "/"),
			Count:    k.count,
			Numbered: k.numbered,
			Example:  k.example,
		})
	}
	return sum
}

// SkipKind is a reason photos are left alone, with how many and one of
// them.
type SkipKind struct {
	Reason  string
	Count   int
	Example RenameSkip
}

var quoted = regexp.MustCompile(`“[^”]*”`)

// SummarizeSkips groups skipped photos by their reason with the paths and
// errors it quotes taken out, largest first.
func SummarizeSkips(skipped []RenameSkip) []SkipKind {
	var kinds []SkipKind
	index := map[string]int{}
	for _, s := range skipped {
		reason := quoted.ReplaceAllString(s.Reason, "…")
		reason, _, _ = strings.Cut(reason, ": ")
		i, ok := index[reason]
		if !ok {
			i = len(kinds)
			index[reason] = i
			kinds = append(kinds, SkipKind{Reason: reason, Example: s})
		}
		kinds[i].Count++
	}
	slices.SortStableFunc(kinds, func(a, b SkipKind) int { return b.Count - a.Count })
	return kinds
}

// ApplyRename executes the planned renames using the files plugin. It never
// replaces a file: one that appeared at a new path since the plan was made
// leaves that photo where it is.
func (p *Plugin) ApplyRename(username string, renames []RenamePreview) error {
	fp, err := p.filesPlugin()
	if err != nil {
		return err
	}
	if len(renames) == 0 {
		return nil
	}

	db, err := p.DB(username)
	if err != nil {
		return err
	}

	batchID, err := NextRenameBatchID(db)
	if err != nil {
		return fmt.Errorf("next batch id: %w", err)
	}

	// One file that cannot be renamed does not stop the rest; the failures
	// are reported together at the end. A file waiting on the failed one's
	// name then finds it taken and stays put too.
	var errs []error
	for _, s := range renameSteps(renames) {
		if err := moveNoReplace(fp, username, s.From, s.To); err != nil {
			p.log.Error("rename", "from", s.From, "to", s.To, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", s.From, err))
			continue
		}

		// Log for undo first: a renamed file the batch does not know of
		// could not be put back.
		if err := LogRename(db, batchID, s.FileID, s.From, s.To); err != nil {
			p.log.Error("log rename", "from", s.From, "err", err)
			errs = append(errs, fmt.Errorf("%s: record for undo: %w", s.From, err))
		}

		if _, err := db.Exec(`UPDATE media SET file_path = ? WHERE id = ?`, s.To, s.MediaID); err != nil {
			p.log.Error("update media path", "media_id", s.MediaID, "err", err)
			errs = append(errs, fmt.Errorf("%s: update gallery: %w", s.From, err))
		}
	}

	return renameErrors(errs, len(renames))
}

// renameErrors summarises the failures of a batch of n renames, or is nil.
func renameErrors(errs []error, n int) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%d of %d failed: %w", len(errs), n, errors.Join(errs...))
}

// UndoLastRename reverses the most recent rename batch.
func (p *Plugin) UndoLastRename(username string) error {
	fp, err := p.filesPlugin()
	if err != nil {
		return err
	}
	db, err := p.DB(username)
	if err != nil {
		return err
	}

	batchID, entries, err := LastRenameBatch(db)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no rename batch to undo")
	}

	// Backwards, so each name is free again by the time it is wanted.
	var errs []error
	for _, e := range slices.Backward(entries) {
		if err := moveNoReplace(fp, username, e.NewPath, e.OldPath); err != nil {
			p.log.Error("undo rename", "from", e.NewPath, "to", e.OldPath, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", e.NewPath, err))
			continue
		}
		if _, err := db.Exec(`UPDATE media SET file_path = ? WHERE file_id = ?`, e.OldPath, e.FileID); err != nil {
			p.log.Error("undo update media path", "file_id", e.FileID, "err", err)
			errs = append(errs, fmt.Errorf("%s: update gallery: %w", e.OldPath, err))
		}
	}

	if err := DeleteRenameBatch(db, batchID); err != nil {
		errs = append(errs, fmt.Errorf("forget batch: %w", err))
	}
	return renameErrors(errs, len(entries))
}

func buildRenameData(m *Media) RenameData {
	var t time.Time
	if m.DateTaken != nil {
		t = time.Unix(*m.DateTaken, 0).UTC()
	} else {
		t = time.Unix(m.DateIndexed, 0).UTC()
	}

	ext := path.Ext(m.FilePath)
	original := strings.TrimSuffix(path.Base(m.FilePath), ext)

	return RenameData{
		Year:     t.Format("2006"),
		Month:    t.Format("01"),
		Day:      t.Format("02"),
		Hour:     t.Format("15"),
		Minute:   t.Format("04"),
		Second:   t.Format("05"),
		Camera:   sanitizeFilename(strings.TrimSpace(m.CameraMake + " " + m.CameraModel)),
		Lens:     sanitizeFilename(m.LensModel),
		Original: original,
		Ext:      ext,
		Place:    sanitizeFilename(m.City),
		Country:  sanitizeFilename(m.Country),
		Width:    m.Width,
		Height:   m.Height,
	}
}

// sanitizeFilename removes characters unsafe for filenames.
func sanitizeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\x00':
			return '_'
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}
