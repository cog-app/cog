package gallery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin"
)

// A photo in a folder someone shared with the user and mounted in their
// files is read by the owner's scan, when the owner's scan folders hold it:
// the user's gallery copies what that found instead of reading the file
// again. People, albums, stars and duplicate decisions stay the user's own.

type shareTargeter interface {
	ShareTarget(string) (owner, ownerPath string, ok bool)
}

// sharedSig is what changes when a scan stage stores something for a photo,
// read the same way on both sides so a copy is made only when it would
// differ.
const sharedSig = `width || '/' || height || '/' || coalesce(date_taken, '') || '/' || city || '/' ||
	quality_score || '/' || fingerprint || '/' || length(ocr_text) || '/' || content_key || '/' ||
	scan_error || '/' || recognition_state || '/' || coalesce(recognition_at, '')`

// sharedCopyCols are the media columns copied from the owner's row.
var sharedCopyCols = []string{
	"width", "height", "orientation", "date_taken", "date_indexed", "latitude", "longitude", "altitude",
	"camera_make", "camera_model", "lens_model", "focal_length", "f_number", "exposure_time",
	"iso", "flash", "duration_ms", "video_codec", "audio_codec",
	"place_name", "country", "region", "city",
	"ocr_text", "fingerprint", "quality_score", "content_key", "scan_error",
}

type sharedRow struct {
	id, fileID       int64
	ownerPath        string
	sig              string
	recognitionState int
	recognitionAt    sql.NullInt64
}

// adoptShared marks which of a user's photos another user's gallery
// analyses and copies in what it has found since the last scan.
func (p *Plugin) adoptShared(ctx context.Context, db, filesDB *sql.DB, username string) error {
	fsys, err := p.ctx.UserVFS(username)
	if err != nil {
		return err
	}
	st, _ := fsys.(shareTargeter)

	rows, err := db.Query(`SELECT id, file_id, file_path, shared_from, ` + sharedSig + `, recognition_state, recognition_at FROM media`)
	if err != nil {
		return fmt.Errorf("list media: %w", err)
	}
	byOwner := map[string][]sharedRow{}
	var unshare []int64
	for rows.Next() {
		var r sharedRow
		var filePath, from string
		if err := rows.Scan(&r.id, &r.fileID, &filePath, &from, &r.sig, &r.recognitionState, &r.recognitionAt); err != nil {
			rows.Close()
			return fmt.Errorf("list media: %w", err)
		}
		var owner string
		if st != nil {
			owner, r.ownerPath, _ = st.ShareTarget(path.Clean("/" + filePath))
		}
		if owner == "" {
			if from != "" {
				unshare = append(unshare, r.id)
			}
			continue
		}
		byOwner[owner] = append(byOwner[owner], r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("list media: %w", err)
	}

	for owner, shared := range byOwner {
		if err := ctx.Err(); err != nil {
			return err
		}
		own, err := p.adoptFromOwner(db, filesDB, owner, shared)
		if err != nil {
			return fmt.Errorf("photos shared by %s: %w", owner, err)
		}
		unshare = append(unshare, own...)
	}
	if len(unshare) == 0 {
		return nil
	}
	return inTx(db, func(tx *sql.Tx) error {
		for _, id := range unshare {
			if _, err := tx.Exec(`UPDATE media SET shared_from = '' WHERE id = ? AND shared_from != ''`, id); err != nil {
				return fmt.Errorf("unshare media %d: %w", id, err)
			}
		}
		return nil
	})
}

// adoptFromOwner copies the owner's findings into the user's rows of photos
// the owner shared, and returns the rows the user's own scan must read: those
// outside the owner's scan folders.
func (p *Plugin) adoptFromOwner(db, filesDB *sql.DB, owner string, shared []sharedRow) (own []int64, err error) {
	ownerDB, err := p.ctx.PluginDB(owner, plugin.ReadOnly, plugin.NoHooks)
	if err != nil {
		return nil, fmt.Errorf("gallery db: %w", err)
	}
	defer ownerDB.Close()
	ownerFiles, err := p.ctx.PluginDB(owner, plugin.OfPlugin("files"), plugin.ReadOnly, plugin.NoHooks)
	if err != nil {
		return nil, fmt.Errorf("files db: %w", err)
	}
	defer ownerFiles.Close()

	patterns := ParseScanFolders(ownerDB)
	var theirs []sharedRow
	for _, r := range shared {
		if matchScanFolders(r.ownerPath, patterns) {
			theirs = append(theirs, r)
		} else {
			own = append(own, r.id)
		}
	}

	const batch = 500
	for start := 0; start < len(theirs); start += batch {
		chunk := theirs[start:min(start+batch, len(theirs))]
		if err := p.adoptBatch(db, filesDB, ownerDB, ownerFiles, owner, chunk); err != nil {
			return nil, err
		}
	}
	return own, nil
}

type ownerMedia struct {
	id               int64
	etag             string
	sig              string
	recognitionState int
	recognitionAt    sql.NullInt64
	recognitionError string
	values           []any // sharedCopyCols
}

type fileStamp struct {
	size int64
	etag string
}

func (p *Plugin) adoptBatch(db, filesDB, ownerDB, ownerFiles *sql.DB, owner string, chunk []sharedRow) error {
	paths := make([]any, len(chunk))
	fileIDs := make([]any, len(chunk))
	for i, r := range chunk {
		paths[i] = r.ownerPath
		fileIDs[i] = r.fileID
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")

	theirs := map[string]*ownerMedia{}
	rows, err := ownerDB.Query(`SELECT file_path, id, file_etag, `+sharedSig+`, recognition_state, recognition_at, recognition_error, `+
		strings.Join(sharedCopyCols, ", ")+` FROM media WHERE file_path IN (`+marks+`)`, paths...)
	if err != nil {
		return fmt.Errorf("read owner's media: %w", err)
	}
	for rows.Next() {
		var path string
		m := &ownerMedia{values: make([]any, len(sharedCopyCols))}
		dest := []any{&path, &m.id, &m.etag, &m.sig, &m.recognitionState, &m.recognitionAt, &m.recognitionError}
		for i := range m.values {
			dest = append(dest, &m.values[i])
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return fmt.Errorf("read owner's media: %w", err)
		}
		theirs[path] = m
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("read owner's media: %w", err)
	}

	ownerStamps, err := fileStamps(ownerFiles, `path`, paths, marks)
	if err != nil {
		return fmt.Errorf("owner's files: %w", err)
	}
	stamps, err := fileStamps(filesDB, `id`, fileIDs, marks)
	if err != nil {
		return fmt.Errorf("files: %w", err)
	}

	return inTx(db, func(tx *sql.Tx) error {
		for _, r := range chunk {
			if _, err := tx.Exec(`UPDATE media SET shared_from = ? WHERE id = ? AND shared_from != ?`, owner, r.id, owner); err != nil {
				return fmt.Errorf("mark media %d shared: %w", r.id, err)
			}
			m := theirs[r.ownerPath]
			if m == nil || m.sig == r.sig {
				continue
			}
			// The owner's row must describe the file as both indexes see it.
			// mtimes are not compared: an upload records the server's clock,
			// a walk of the disk the file's. A change the owner's index has yet
			// to see changes their row when it does, and is copied then.
			theirFile, mine := ownerStamps[r.ownerPath], stamps[fmt.Sprint(r.fileID)]
			if theirFile.etag != m.etag || theirFile.size != mine.size {
				continue
			}
			if err := adoptMedia(tx, ownerDB, r, m); err != nil {
				return err
			}
		}
		return nil
	})
}

// fileStamps reads size and etag of files rows keyed by col.
func fileStamps(db *sql.DB, col string, keys []any, marks string) (map[string]fileStamp, error) {
	rows, err := db.Query(`SELECT `+col+`, size, etag FROM files WHERE `+col+` IN (`+marks+`)`, keys...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]fileStamp, len(keys))
	for rows.Next() {
		var k string
		var s fileStamp
		if err := rows.Scan(&k, &s.size, &s.etag); err != nil {
			return nil, err
		}
		out[k] = s
	}
	return out, rows.Err()
}

// adoptMedia copies one photo's findings from the owner's row: its metadata
// and analysis, and when the owner's recognition has run since, its faces
// and the tags recognition gave it. Tags added by hand on either side are not
// copied or removed.
func adoptMedia(tx *sql.Tx, ownerDB *sql.DB, r sharedRow, m *ownerMedia) error {
	set := make([]string, len(sharedCopyCols))
	for i, c := range sharedCopyCols {
		set[i] = c + " = ?"
	}
	if _, err := tx.Exec(`UPDATE media SET `+strings.Join(set, ", ")+` WHERE id = ?`, append(m.values, r.id)...); err != nil {
		return fmt.Errorf("copy media %d: %w", r.id, err)
	}
	if m.recognitionState == r.recognitionState && m.recognitionAt == r.recognitionAt {
		return nil
	}

	if _, err := tx.Exec(`DELETE FROM faces WHERE media_id = ?`, r.id); err != nil {
		return fmt.Errorf("clear faces of media %d: %w", r.id, err)
	}
	if _, err := tx.Exec(`DELETE FROM media_tags WHERE media_id = ? AND confidence < 1`, r.id); err != nil {
		return fmt.Errorf("clear tags of media %d: %w", r.id, err)
	}
	if _, err := tx.Exec(`UPDATE media SET recognition_state = ?, recognition_at = ?, recognition_error = ? WHERE id = ?`,
		m.recognitionState, m.recognitionAt, m.recognitionError, r.id); err != nil {
		return fmt.Errorf("copy recognition of media %d: %w", r.id, err)
	}

	tags, err := ownerDB.Query(`SELECT t.name, t.tag_type, mt.confidence FROM media_tags mt JOIN tags t ON t.id = mt.tag_id
		WHERE mt.media_id = ? AND mt.confidence < 1`, m.id)
	if err != nil {
		return fmt.Errorf("read owner's tags: %w", err)
	}
	var found []RecognizedTag
	for tags.Next() {
		var t RecognizedTag
		if err := tags.Scan(&t.Name, &t.Type, &t.Confidence); err != nil {
			tags.Close()
			return fmt.Errorf("read owner's tags: %w", err)
		}
		found = append(found, t)
	}
	if err := errors.Join(tags.Err(), tags.Close()); err != nil {
		return fmt.Errorf("read owner's tags: %w", err)
	}
	if err := storeTags(tx, r.id, found, 0); err != nil {
		return err
	}

	faces, err := ownerDB.Query(`SELECT id, x, y, w, h, embedding, confidence, estimated_age, quality FROM faces WHERE media_id = ?`, m.id)
	if err != nil {
		return fmt.Errorf("read owner's faces: %w", err)
	}
	defer faces.Close()
	for faces.Next() {
		var id int64
		var x, y, w, h, conf, age, quality float64
		var embedding []byte
		if err := faces.Scan(&id, &x, &y, &w, &h, &embedding, &conf, &age, &quality); err != nil {
			return fmt.Errorf("read owner's faces: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO faces (media_id, x, y, w, h, embedding, confidence, estimated_age, quality, owner_face_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.id, x, y, w, h, embedding, conf, age, quality, id); err != nil {
			return fmt.Errorf("copy face to media %d: %w", r.id, err)
		}
	}
	return faces.Err()
}

// ownersFirst orders users so those with photos shared from someone else's
// gallery come last, and copy what their owners' scans found in the same
// round.
func (p *Plugin) ownersFirst(usernames []string) []string {
	var owners, recipients []string
	for _, u := range usernames {
		shared, err := p.hasShared(u)
		if err != nil {
			p.log.Error("order scan", "user", u, "err", err)
		}
		if shared {
			recipients = append(recipients, u)
		} else {
			owners = append(owners, u)
		}
	}
	return append(owners, recipients...)
}

func (p *Plugin) hasShared(username string) (bool, error) {
	db, err := p.ctx.PluginDB(username, plugin.ReadOnly, plugin.NoHooks)
	if err != nil {
		return false, err
	}
	var shared bool
	err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM media WHERE shared_from != '')`).Scan(&shared)
	return shared, errors.Join(err, db.Close())
}

// sharedMedia returns the owner's row of m when m is in a folder shared with
// username and the owner's gallery has it, so what is cached for it is the
// owner's.
func (p *Plugin) sharedMedia(username string, m *Media) (string, *Media, error) {
	fsys, err := p.ctx.UserVFS(username)
	if err != nil {
		return "", nil, err
	}
	st, ok := fsys.(shareTargeter)
	if !ok {
		return "", nil, nil
	}
	owner, ownerPath, ok := st.ShareTarget(path.Clean("/" + m.FilePath))
	if !ok {
		return "", nil, nil
	}
	db, err := p.DB(owner)
	if err != nil {
		return "", nil, err
	}
	theirs, err := scanMedia(db.QueryRow(`SELECT `+mediaCols+` FROM media m WHERE m.file_path = ?`, ownerPath))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("owner's media: %w", err)
	}
	return owner, theirs, nil
}
