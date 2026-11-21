package gallery

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strconv"
	"strings"
	"time"
)

// AlbumType constants.
const (
	AlbumManual   = 0
	AlbumDate     = 1
	AlbumLocation = 2
	AlbumCamera   = 3
	AlbumFolder   = 4
	AlbumFace     = 5
)

// Media represents a single photo or video in the gallery index.
type Media struct {
	ID               int64
	FileID           int64
	FilePath         string
	FileETag         string
	MimeType         string
	Width            int
	Height           int
	Orientation      int
	DateTaken        *int64
	DateIndexed      int64
	Latitude         *float64
	Longitude        *float64
	Altitude         *float64
	CameraMake       string
	CameraModel      string
	LensModel        string
	FocalLength      *float64
	FNumber          *float64
	ExposureTime     string
	ISO              *int
	Flash            *int
	DurationMs       *int64
	VideoCodec       string
	AudioCodec       string
	PlaceName        string
	Country          string
	Region           string
	City             string
	RecognitionState int
	RecognitionAt    *int64
	OCRText          string
	Fingerprint      string
	QualityScore     float64
	ContentKey       string // see contentKey; empty until the file is read
}

// IsVideo reports whether this media item is a video.
func (m *Media) IsVideo() bool {
	return strings.HasPrefix(m.MimeType, "video/")
}

// FileName returns just the filename part of the path.
func (m *Media) FileName() string {
	if i := strings.LastIndex(m.FilePath, "/"); i >= 0 {
		return m.FilePath[i+1:]
	}
	return m.FilePath
}

// Album represents a photo album.
type Album struct {
	ID           int64
	Name         string
	Description  string
	AlbumType    int
	AutoKey      string
	CoverMediaID *int64
	SortOrder    int
	CreatedAt    int64
	UpdatedAt    int64
	MediaCount   int // populated by list queries
}

// AlbumTypeName returns a human-readable name for the album type.
func (a *Album) AlbumTypeName() string {
	switch a.AlbumType {
	case AlbumDate:
		return "Date"
	case AlbumLocation:
		return "Location"
	case AlbumCamera:
		return "Camera"
	case AlbumFolder:
		return "Folder"
	case AlbumFace:
		return "Person"
	default:
		return "Album"
	}
}

// Tag represents a media tag.
type Tag struct {
	ID         int64
	Name       string
	TagType    int
	Count      int     // populated by list queries
	Confidence float64 // recognition confidence 0-1, populated by TagsForMedia
}

// Person represents a recognized person.
type Person struct {
	ID                   int64
	Name                 string
	RepresentativeFaceID *int64
	Visible              bool
	CreatedAt            int64
	ContactUID           string // vCard UID of linked contact, empty if unlinked
	FaceCount            int    // populated by list queries
	BestFaceID           *int64 // face with largest crop area, populated by list queries
}

// DisplayName returns the person's name or "Unknown" if unnamed.
func (p *Person) DisplayName() string {
	if p.Name == "" {
		return "Unknown"
	}
	return p.Name
}

// TimelineDay represents a single day in the timeline with its media count.
type TimelineDay struct {
	DayID int64 // floor(unix_timestamp / 86400)
	Count int
}

// Date returns the date for this timeline day.
func (d *TimelineDay) Date() time.Time {
	return time.Unix(d.DayID*86400, 0).UTC()
}

var mediaCols = `m.id, m.file_id, m.file_path, m.file_etag, m.mime_type, m.width, m.height, m.orientation,
	m.date_taken, m.date_indexed, m.latitude, m.longitude, m.altitude,
	m.camera_make, m.camera_model, m.lens_model, m.focal_length, m.f_number, m.exposure_time,
	m.iso, m.flash, m.duration_ms, m.video_codec, m.audio_codec,
	m.place_name, m.country, m.region, m.city, m.recognition_state, m.recognition_at, m.ocr_text,
	m.fingerprint, m.quality_score, m.content_key`

func scanMedia(row interface{ Scan(...any) error }) (*Media, error) {
	var m Media
	err := row.Scan(
		&m.ID, &m.FileID, &m.FilePath, &m.FileETag, &m.MimeType,
		&m.Width, &m.Height, &m.Orientation,
		&m.DateTaken, &m.DateIndexed, &m.Latitude, &m.Longitude, &m.Altitude,
		&m.CameraMake, &m.CameraModel, &m.LensModel, &m.FocalLength, &m.FNumber, &m.ExposureTime,
		&m.ISO, &m.Flash, &m.DurationMs, &m.VideoCodec, &m.AudioCodec,
		&m.PlaceName, &m.Country, &m.Region, &m.City, &m.RecognitionState, &m.RecognitionAt,
		&m.OCRText, &m.Fingerprint, &m.QualityScore, &m.ContentKey,
	)
	return &m, err
}

func scanMediaRows(rows *sql.Rows) ([]Media, error) {
	var result []Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *m)
	}
	return result, rows.Err()
}

// GetMedia returns a single media item by ID.
func GetMedia(db *sql.DB, id int64) (*Media, error) {
	row := db.QueryRow(`SELECT `+mediaCols+` FROM media m WHERE m.id = ?`, id)
	return scanMedia(row)
}

// GetMediaByFileID returns a media item by its files plugin file_id.
func GetMediaByFileID(db *sql.DB, fileID int64) (*Media, error) {
	row := db.QueryRow(`SELECT `+mediaCols+` FROM media m WHERE m.file_id = ?`, fileID)
	return scanMedia(row)
}

// RecentMedia returns the most recent media items by date_taken.
func RecentMedia(db *sql.DB, limit int) ([]Media, error) {
	rows, err := db.Query(`SELECT `+mediaCols+` FROM media m
		ORDER BY COALESCE(m.date_taken, m.date_indexed) DESC, m.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMediaRows(rows)
}

// TimelineMedia returns media for a specific day.
func TimelineMedia(db *sql.DB, dayID int64, limit, offset int) ([]Media, error) {
	start := dayID * 86400
	end := start + 86400
	rows, err := db.Query(`SELECT `+mediaCols+` FROM media m
		WHERE COALESCE(m.date_taken, m.date_indexed) >= ? AND COALESCE(m.date_taken, m.date_indexed) < ?
		ORDER BY COALESCE(m.date_taken, m.date_indexed) DESC LIMIT ? OFFSET ?`,
		start, end, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMediaRows(rows)
}

// TimelineDays returns a list of days with their media counts.
func TimelineDays(db *sql.DB) ([]TimelineDay, error) {
	rows, err := db.Query(`SELECT COALESCE(date_taken, date_indexed) / 86400 AS day_id, COUNT(*) AS cnt
		FROM media
		GROUP BY day_id
		ORDER BY day_id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var days []TimelineDay
	for rows.Next() {
		var d TimelineDay
		if err := rows.Scan(&d.DayID, &d.Count); err != nil {
			return nil, err
		}
		days = append(days, d)
	}
	return days, rows.Err()
}

// MediaCount returns the total number of media items.
func MediaCount(db *sql.DB) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM media`).Scan(&count)
	return count, err
}

// VideoMedia returns all video media items.
func VideoMedia(db *sql.DB, limit, offset int) ([]Media, error) {
	rows, err := db.Query(`SELECT `+mediaCols+` FROM media m
		WHERE m.mime_type LIKE 'video/%'
		ORDER BY COALESCE(m.date_taken, m.date_indexed) DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMediaRows(rows)
}

// InsertMedia inserts a new media row and returns its ID.
func InsertMedia(db execQuerier, m *Media) (int64, error) {
	res, err := db.Exec(`INSERT INTO media (
		file_id, file_path, file_etag, mime_type, width, height, orientation,
		date_taken, date_indexed, latitude, longitude, altitude,
		camera_make, camera_model, lens_model, focal_length, f_number, exposure_time,
		iso, flash, duration_ms, video_codec, audio_codec,
		place_name, country, region, city, recognition_state, recognition_at, ocr_text, content_key
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.FileID, m.FilePath, m.FileETag, m.MimeType, m.Width, m.Height, m.Orientation,
		m.DateTaken, m.DateIndexed, m.Latitude, m.Longitude, m.Altitude,
		m.CameraMake, m.CameraModel, m.LensModel, m.FocalLength, m.FNumber, m.ExposureTime,
		m.ISO, m.Flash, m.DurationMs, m.VideoCodec, m.AudioCodec,
		m.PlaceName, m.Country, m.Region, m.City, m.RecognitionState, m.RecognitionAt,
		m.OCRText, m.ContentKey,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateMediaMetadata updates the extracted metadata fields for an existing media row.
func UpdateMediaMetadata(db *sql.DB, m *Media) error {
	_, err := db.Exec(`UPDATE media SET
		file_path = ?, file_etag = ?, mime_type = ?, width = ?, height = ?, orientation = ?,
		date_taken = ?, date_indexed = ?, latitude = ?, longitude = ?, altitude = ?,
		camera_make = ?, camera_model = ?, lens_model = ?, focal_length = ?, f_number = ?,
		exposure_time = ?, iso = ?, flash = ?, duration_ms = ?, video_codec = ?, audio_codec = ?,
		place_name = ?, country = ?, region = ?, city = ?,
		recognition_state = ?, recognition_at = ?, ocr_text = ?, content_key = ?
		WHERE id = ?`,
		m.FilePath, m.FileETag, m.MimeType, m.Width, m.Height, m.Orientation,
		m.DateTaken, m.DateIndexed, m.Latitude, m.Longitude, m.Altitude,
		m.CameraMake, m.CameraModel, m.LensModel, m.FocalLength, m.FNumber,
		m.ExposureTime, m.ISO, m.Flash, m.DurationMs, m.VideoCodec, m.AudioCodec,
		m.PlaceName, m.Country, m.Region, m.City,
		m.RecognitionState, m.RecognitionAt, m.OCRText, m.ContentKey,
		m.ID,
	)
	return err
}

// ResetMediaByFileID updates a changed file's media row in-place, preserving
// the row ID (and thus all associated faces/media_tags/album_media).
// It resets metadata and recognition state so both get re-extracted.
func ResetMediaByFileID(db execQuerier, fileID int64, path, etag, mime, contentKey string, dateIndexed int64) error {
	_, err := db.Exec(`UPDATE media SET content_key = ?, quality_score = -1,
		file_path = ?, file_etag = ?, mime_type = ?, date_indexed = ?,
		width = 0, height = 0, orientation = 0,
		date_taken = NULL, latitude = NULL, longitude = NULL, altitude = NULL,
		camera_make = '', camera_model = '', lens_model = '',
		focal_length = NULL, f_number = NULL, exposure_time = '',
		iso = NULL, flash = NULL, duration_ms = NULL, video_codec = '', audio_codec = '',
		place_name = '', country = '', region = '', city = '',
		recognition_state = 0, recognition_at = NULL, ocr_text = '', fingerprint = ''
		WHERE file_id = ?`, contentKey, path, etag, mime, dateIndexed, fileID)
	return err
}

// DeleteMediaByFileID deletes a media row by its files plugin file_id.
func DeleteMediaByFileID(db execQuerier, fileID int64) error {
	_, err := db.Exec(`DELETE FROM media WHERE file_id = ?`, fileID)
	return err
}

// StarredFileIDs returns file IDs that are marked as favorites in the files plugin DB.
func StarredFileIDs(filesDB *sql.DB) ([]int64, error) {
	rows, err := filesDB.Query(`SELECT id FROM files WHERE favorite = 1 AND is_dir = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// StarredMedia returns gallery media matching the given starred file IDs.
func StarredMedia(db *sql.DB, fileIDs []int64) ([]Media, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(fileIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(fileIDs))
	for i, id := range fileIDs {
		args[i] = id
	}
	rows, err := db.Query(`SELECT `+mediaCols+` FROM media m
		WHERE m.file_id IN (`+placeholders+`)
		ORDER BY COALESCE(m.date_taken, m.date_indexed) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMediaRows(rows)
}

// StarredMediaCount returns the count of starred media items.
func StarredMediaCount(filesDB, galleryDB *sql.DB) (int, error) {
	fileIDs, err := StarredFileIDs(filesDB)
	if err != nil {
		return 0, err
	}
	if len(fileIDs) == 0 {
		return 0, nil
	}
	placeholders := strings.Repeat("?,", len(fileIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(fileIDs))
	for i, id := range fileIDs {
		args[i] = id
	}
	var count int
	if err := galleryDB.QueryRow(`SELECT COUNT(*) FROM media WHERE file_id IN (`+placeholders+`)`, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count starred media: %w", err)
	}
	return count, nil
}

// CleanOrphans removes recognition data that references non-existent media rows.
// This fixes databases where CASCADE constraints were missing when rows were created.
func CleanOrphans(db *sql.DB) error {
	_, err := db.Exec(`
		DELETE FROM media_tags WHERE media_id NOT IN (SELECT id FROM media);
		DELETE FROM faces WHERE media_id NOT IN (SELECT id FROM media);
		DELETE FROM people WHERE id NOT IN (SELECT DISTINCT person_id FROM faces WHERE person_id IS NOT NULL);
		DELETE FROM tags WHERE id NOT IN (SELECT DISTINCT tag_id FROM media_tags)`)
	return err
}

// indexedFile is what discovery needs of a media row.
type indexedFile struct {
	ID         int64
	ETag       string
	ContentKey string
}

// IndexedFiles returns every media row by its files-plugin file_id.
func IndexedFiles(db *sql.DB) (map[int64]indexedFile, error) {
	rows, err := db.Query(`SELECT file_id, id, file_etag, content_key FROM media`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]indexedFile)
	for rows.Next() {
		var fileID int64
		var f indexedFile
		if err := rows.Scan(&fileID, &f.ID, &f.ETag, &f.ContentKey); err != nil {
			return nil, err
		}
		result[fileID] = f
	}
	return result, rows.Err()
}

// --- Albums ---

// albumCoverCol selects the album's cover: the one chosen for it, or else its
// first photo, so an album never shows an empty card when it has photos.
const albumCoverCol = `COALESCE(a.cover_media_id, (SELECT am.media_id FROM album_media am
		WHERE am.album_id = a.id ORDER BY am.sort_order, am.added_at, am.media_id LIMIT 1))`

// ListAlbums returns all albums with their media counts.
func ListAlbums(db *sql.DB, albumType int) ([]Album, error) {
	q := `SELECT a.id, a.name, a.description, a.album_type, a.auto_key,
		` + albumCoverCol + `, a.sort_order, a.created_at, a.updated_at,
		(SELECT COUNT(*) FROM album_media am WHERE am.album_id = a.id) AS media_count
		FROM albums a`
	var args []any
	if albumType >= 0 {
		q += ` WHERE a.album_type = ?`
		args = append(args, albumType)
	}
	q += ` ORDER BY a.updated_at DESC`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAlbumRows(rows)
}

// GetAlbum returns a single album by ID.
func GetAlbum(db *sql.DB, id int64) (*Album, error) {
	row := db.QueryRow(`SELECT a.id, a.name, a.description, a.album_type, a.auto_key,
		`+albumCoverCol+`, a.sort_order, a.created_at, a.updated_at,
		(SELECT COUNT(*) FROM album_media am WHERE am.album_id = a.id) AS media_count
		FROM albums a WHERE a.id = ?`, id)
	return scanAlbum(row)
}

func scanAlbum(row interface{ Scan(...any) error }) (*Album, error) {
	var a Album
	err := row.Scan(&a.ID, &a.Name, &a.Description, &a.AlbumType, &a.AutoKey,
		&a.CoverMediaID, &a.SortOrder, &a.CreatedAt, &a.UpdatedAt, &a.MediaCount)
	return &a, err
}

func scanAlbumRows(rows *sql.Rows) ([]Album, error) {
	var result []Album
	for rows.Next() {
		a, err := scanAlbum(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *a)
	}
	return result, rows.Err()
}

// AlbumMedia returns the media items in an album.
func AlbumMedia(db *sql.DB, albumID int64, limit, offset int) ([]Media, error) {
	rows, err := db.Query(`SELECT `+mediaCols+` FROM media m
		JOIN album_media am ON am.media_id = m.id
		WHERE am.album_id = ?
		ORDER BY am.sort_order, COALESCE(m.date_taken, m.date_indexed) DESC
		LIMIT ? OFFSET ?`, albumID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMediaRows(rows)
}

// CreateAlbum creates a new manual album and returns its ID.
func CreateAlbumRow(db *sql.DB, name, description string) (int64, error) {
	res, err := db.Exec(`INSERT INTO albums (name, description) VALUES (?, ?)`, name, description)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpsertAutoAlbum creates or finds an auto-album and returns its ID. The
// conflict target names the partial index's condition, which is what lets
// SQLite match it.
func UpsertAutoAlbum(db *sql.DB, albumType int, autoKey, name string) (int64, error) {
	var id int64
	err := db.QueryRow(`INSERT INTO albums (name, album_type, auto_key)
		VALUES (?, ?, ?)
		ON CONFLICT(album_type, auto_key) WHERE album_type != 0 DO UPDATE SET updated_at = unixepoch()
		RETURNING id`,
		name, albumType, autoKey).Scan(&id)
	return id, err
}

// AddMediaToAlbum links a media item to an album.
func AddMediaToAlbum(db *sql.DB, albumID, mediaID int64) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO album_media (album_id, media_id) VALUES (?, ?)`,
		albumID, mediaID)
	return err
}

// RemoveMediaFromAlbum removes a media item from an album.
func RemoveMediaFromAlbum(db *sql.DB, albumID, mediaID int64) error {
	_, err := db.Exec(`DELETE FROM album_media WHERE album_id = ? AND media_id = ?`,
		albumID, mediaID)
	return err
}

// DeleteAlbumRow deletes an album by ID.
func DeleteAlbumRow(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM albums WHERE id = ?`, id)
	return err
}

// RenameAlbumRow changes an album's name and description.
func RenameAlbumRow(db *sql.DB, id int64, name, description string) error {
	_, err := db.Exec(`UPDATE albums SET name = ?, description = ?, updated_at = unixepoch() WHERE id = ?`,
		name, description, id)
	return err
}

// NeighbourMedia returns the photos either side of m in timeline order
// (newest first): newer is the one before it, older the one after. Either is
// 0 at the end of the timeline.
func NeighbourMedia(db *sql.DB, m *Media) (newer, older int64, err error) {
	when := m.DateIndexed
	if m.DateTaken != nil {
		when = *m.DateTaken
	}
	err = db.QueryRow(`SELECT id FROM media
		WHERE (COALESCE(date_taken, date_indexed), id) > (?, ?)
		ORDER BY COALESCE(date_taken, date_indexed), id LIMIT 1`, when, m.ID).Scan(&newer)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	err = db.QueryRow(`SELECT id FROM media
		WHERE (COALESCE(date_taken, date_indexed), id) < (?, ?)
		ORDER BY COALESCE(date_taken, date_indexed) DESC, id DESC LIMIT 1`, when, m.ID).Scan(&older)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	return newer, older, nil
}

// ErrNotInAlbum is a cover chosen from outside the album.
var ErrNotInAlbum = errors.New("the photo is not in this album")

// SetAlbumCover makes one of an album's photos its cover. The choice lasts
// until that photo leaves the album (the album_cover_removed trigger), when
// the album goes back to showing its first photo.
func SetAlbumCover(db *sql.DB, albumID, mediaID int64) error {
	res, err := db.Exec(`UPDATE albums SET cover_media_id = ?
		WHERE id = ? AND EXISTS (SELECT 1 FROM album_media WHERE album_id = ? AND media_id = ?)`,
		mediaID, albumID, albumID, mediaID)
	if err != nil {
		return fmt.Errorf("set album cover: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set album cover: %w", err)
	}
	if n == 0 {
		return ErrNotInAlbum
	}
	return nil
}

// --- Tags ---

// ListTags returns all tags with their media counts.
func ListTags(db *sql.DB) ([]Tag, error) {
	rows, err := db.Query(`SELECT id, name, tag_type, cnt FROM (
		SELECT t.id, t.name, t.tag_type,
			(SELECT COUNT(*) FROM media_tags mt JOIN media m ON m.id = mt.media_id WHERE mt.tag_id = t.id) AS cnt
		FROM tags t
	) WHERE cnt > 0 ORDER BY cnt DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.TagType, &t.Count); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// MediaByTag returns media items with a specific tag.
func MediaByTag(db *sql.DB, tagName string, limit, offset int) ([]Media, error) {
	rows, err := db.Query(`SELECT `+mediaCols+` FROM media m
		JOIN media_tags mt ON mt.media_id = m.id
		JOIN tags t ON t.id = mt.tag_id
		WHERE t.name = ?
		ORDER BY COALESCE(m.date_taken, m.date_indexed) DESC
		LIMIT ? OFFSET ?`, tagName, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMediaRows(rows)
}

// UpsertTag ensures a tag exists and returns its ID.
func UpsertTag(db execQuerier, name string, tagType int) (int64, error) {
	// RETURNING, not LastInsertId: when the upsert updates, SQLite leaves
	// last_insert_rowid at whatever this connection inserted before.
	var id int64
	err := db.QueryRow(`INSERT INTO tags (name, tag_type) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET tag_type = excluded.tag_type
		RETURNING id`, name, tagType).Scan(&id)
	return id, err
}

// AddMediaTag links a tag to a media item.
func AddMediaTag(db execQuerier, mediaID, tagID int64, confidence float64) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO media_tags (media_id, tag_id, confidence) VALUES (?, ?, ?)`,
		mediaID, tagID, confidence)
	return err
}

// TagsForMedia returns all tags for a media item.
func TagsForMedia(db *sql.DB, mediaID int64) ([]Tag, error) {
	rows, err := db.Query(`SELECT t.id, t.name, t.tag_type, mt.confidence
		FROM tags t JOIN media_tags mt ON mt.tag_id = t.id
		WHERE mt.media_id = ?
		ORDER BY mt.confidence DESC`, mediaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.TagType, &t.Confidence); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// RemoveMediaTag removes a tag from a media item.
func RemoveMediaTag(db *sql.DB, mediaID, tagID int64) error {
	_, err := db.Exec(`DELETE FROM media_tags WHERE media_id = ? AND tag_id = ?`, mediaID, tagID)
	return err
}

// SearchTags returns tags matching a query string.
func SearchTags(db *sql.DB, query string, limit int) ([]Tag, error) {
	rows, err := db.Query(`SELECT id, name, tag_type, 0 FROM tags
		WHERE name LIKE '%' || ? || '%'
		ORDER BY name LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.TagType, &t.Count); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// --- Faces ---

// Face represents a detected face with optional person info for display.
type Face struct {
	ID           int64
	MediaID      int64
	PersonID     *int64
	Name         string  // person name, "" if unnamed
	X, Y         float64 // normalized bounding box (0..1)
	W, H         float64
	EstimatedAge float64 // -1 if not estimated
	Confidence   float64 // detection confidence 0-1
}

// FacesForMedia returns all faces detected in a media item.
func FacesForMedia(db *sql.DB, mediaID int64) ([]Face, error) {
	rows, err := db.Query(`SELECT f.id, f.media_id, f.person_id, COALESCE(p.name, ''), f.x, f.y, f.w, f.h, f.estimated_age, f.confidence
		FROM faces f LEFT JOIN people p ON p.id = f.person_id
		WHERE f.media_id = ?
		ORDER BY f.id`, mediaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var faces []Face
	for rows.Next() {
		var f Face
		if err := rows.Scan(&f.ID, &f.MediaID, &f.PersonID, &f.Name, &f.X, &f.Y, &f.W, &f.H, &f.EstimatedAge, &f.Confidence); err != nil {
			return nil, err
		}
		faces = append(faces, f)
	}
	return faces, rows.Err()
}

// FacesForMediaBatch returns faces for multiple media items, keyed by media ID.
func FacesForMediaBatch(db *sql.DB, mediaIDs []int64) (map[int64][]Face, error) {
	if len(mediaIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(mediaIDs))
	args := make([]any, len(mediaIDs))
	for i, id := range mediaIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := db.Query(`SELECT f.id, f.media_id, f.person_id, COALESCE(p.name, ''), f.x, f.y, f.w, f.h, f.estimated_age, f.confidence
		FROM faces f LEFT JOIN people p ON p.id = f.person_id
		WHERE f.media_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY f.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64][]Face)
	for rows.Next() {
		var f Face
		if err := rows.Scan(&f.ID, &f.MediaID, &f.PersonID, &f.Name, &f.X, &f.Y, &f.W, &f.H, &f.EstimatedAge, &f.Confidence); err != nil {
			return nil, err
		}
		result[f.MediaID] = append(result[f.MediaID], f)
	}
	return result, rows.Err()
}

// FaceWithMedia holds a face and the dimensions of its source media.
type FaceWithMedia struct {
	Face
	MediaWidth  int
	MediaHeight int
}

// GetFaceWithMedia returns a face by ID with its source media dimensions.
func GetFaceWithMedia(db *sql.DB, faceID int64) (*FaceWithMedia, error) {
	var f FaceWithMedia
	err := db.QueryRow(`SELECT f.id, f.media_id, f.person_id, COALESCE(p.name, ''), f.x, f.y, f.w, f.h,
		m.width, m.height
		FROM faces f JOIN media m ON m.id = f.media_id
		LEFT JOIN people p ON p.id = f.person_id
		WHERE f.id = ?`, faceID).Scan(
		&f.ID, &f.MediaID, &f.PersonID, &f.Name, &f.X, &f.Y, &f.W, &f.H,
		&f.MediaWidth, &f.MediaHeight)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// FacesByPerson returns all faces for a person, ordered by crop area (largest first).
func FacesByPerson(db *sql.DB, personID int64, limit, offset int) ([]FaceWithMedia, error) {
	rows, err := db.Query(`SELECT f.id, f.media_id, f.person_id, COALESCE(p.name, ''), f.x, f.y, f.w, f.h,
		m.width, m.height
		FROM faces f JOIN media m ON m.id = f.media_id
		LEFT JOIN people p ON p.id = f.person_id
		WHERE f.person_id = ?
		ORDER BY f.w * m.width * f.h * m.height DESC
		LIMIT ? OFFSET ?`, personID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var faces []FaceWithMedia
	for rows.Next() {
		var f FaceWithMedia
		if err := rows.Scan(&f.ID, &f.MediaID, &f.PersonID, &f.Name, &f.X, &f.Y, &f.W, &f.H,
			&f.MediaWidth, &f.MediaHeight); err != nil {
			return nil, err
		}
		faces = append(faces, f)
	}
	return faces, rows.Err()
}

// DeleteFace removes a face record entirely.
func DeleteFace(db *sql.DB, faceID int64) error {
	_, err := db.Exec(`DELETE FROM faces WHERE id = ?`, faceID)
	return err
}

// CreatePersonFromFace creates a new unnamed person and assigns the given face to it.
func CreatePersonFromFace(db *sql.DB, faceID int64) (int64, error) {
	var personID int64
	err := inTx(db, func(tx *sql.Tx) error {
		var err error
		personID, err = createPersonFromFace(tx, faceID)
		return err
	})
	return personID, err
}

func createPersonFromFace(tx *sql.Tx, faceID int64) (int64, error) {
	var personID int64
	if err := tx.QueryRow(`INSERT INTO people (name, representative_face_id) VALUES ('', ?) RETURNING id`, faceID).Scan(&personID); err != nil {
		return 0, fmt.Errorf("create person for face %d: %w", faceID, err)
	}
	if _, err := tx.Exec(`UPDATE faces SET person_id = ? WHERE id = ?`, personID, faceID); err != nil {
		return 0, fmt.Errorf("assign face %d: %w", faceID, err)
	}
	return personID, nil
}

// --- People ---

// SearchPeople returns people matching a query string, ordered by face count.
func SearchPeople(db *sql.DB, query string, excludeID int64, limit int) ([]Person, error) {
	rows, err := db.Query(`SELECT p.id, p.name, p.contact_uid,
		(SELECT COUNT(*) FROM faces f WHERE f.person_id = p.id) AS face_count,
		(SELECT f.id FROM faces f JOIN media m ON m.id = f.media_id
			WHERE f.person_id = p.id ORDER BY f.w * m.width * f.h * m.height DESC LIMIT 1) AS best_face_id
		FROM people p
		WHERE p.name LIKE '%' || ? || '%' AND p.id != ? AND p.visible = 1
		ORDER BY face_count DESC LIMIT ?`, query, excludeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var people []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Name, &p.ContactUID, &p.FaceCount, &p.BestFaceID); err != nil {
			return nil, err
		}
		people = append(people, p)
	}
	return people, rows.Err()
}

// ListPeople returns all visible people with face counts and best face ID.
func ListPeople(db *sql.DB) ([]Person, error) {
	rows, err := db.Query(`SELECT id, name, representative_face_id, visible, created_at, contact_uid, face_count, best_face_id FROM (
		SELECT p.id, p.name, p.representative_face_id, p.visible, p.created_at, p.contact_uid,
			(SELECT COUNT(*) FROM faces f JOIN media m ON m.id = f.media_id WHERE f.person_id = p.id) AS face_count,
			(SELECT f.id FROM faces f JOIN media m ON m.id = f.media_id
				WHERE f.person_id = p.id ORDER BY f.w * m.width * f.h * m.height DESC LIMIT 1) AS best_face_id
		FROM people p WHERE p.visible = 1
	) WHERE face_count > 0 ORDER BY face_count DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var people []Person
	for rows.Next() {
		var person Person
		if err := rows.Scan(&person.ID, &person.Name, &person.RepresentativeFaceID,
			&person.Visible, &person.CreatedAt, &person.ContactUID, &person.FaceCount, &person.BestFaceID); err != nil {
			return nil, err
		}
		people = append(people, person)
	}
	return people, rows.Err()
}

// GetPerson returns a single person by ID.
func GetPerson(db *sql.DB, id int64) (*Person, error) {
	var p Person
	err := db.QueryRow(`SELECT p.id, p.name, p.representative_face_id, p.visible, p.created_at, p.contact_uid,
		(SELECT COUNT(*) FROM faces f JOIN media m ON m.id = f.media_id WHERE f.person_id = p.id) AS face_count,
		(SELECT f.id FROM faces f JOIN media m ON m.id = f.media_id
			WHERE f.person_id = p.id ORDER BY f.w * m.width * f.h * m.height DESC LIMIT 1) AS best_face_id
		FROM people p WHERE p.id = ?`, id).Scan(
		&p.ID, &p.Name, &p.RepresentativeFaceID, &p.Visible, &p.CreatedAt, &p.ContactUID, &p.FaceCount, &p.BestFaceID)
	return &p, err
}

// MediaByPerson returns media items containing a specific person.
func MediaByPerson(db *sql.DB, personID int64, limit, offset int) ([]Media, error) {
	rows, err := db.Query(`SELECT DISTINCT `+mediaCols+` FROM media m
		JOIN faces f ON f.media_id = m.id
		WHERE f.person_id = ?
		ORDER BY COALESCE(m.date_taken, m.date_indexed) DESC
		LIMIT ? OFFSET ?`, personID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMediaRows(rows)
}

// NamePerson sets a person's display name.
func NamePersonRow(db *sql.DB, id int64, name string) error {
	_, err := db.Exec(`UPDATE people SET name = ? WHERE id = ?`, name, id)
	return err
}

// LinkPersonToContact sets the contact_uid on a person, linking them to a contact.
func LinkPersonToContact(db *sql.DB, personID int64, contactUID string) error {
	_, err := db.Exec(`UPDATE people SET contact_uid = ? WHERE id = ?`, contactUID, personID)
	return err
}

// PersonByContactUID returns the visible person linked to a contact UID, or sql.ErrNoRows.
func PersonByContactUID(db *sql.DB, contactUID string) (*Person, error) {
	var p Person
	err := db.QueryRow(`SELECT id, name, contact_uid FROM people WHERE contact_uid = ? AND visible = 1 LIMIT 1`,
		contactUID).Scan(&p.ID, &p.Name, &p.ContactUID)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// HidePersonRow toggles a person's visibility.
func HidePersonRow(db *sql.DB, id int64) error {
	_, err := db.Exec(`UPDATE people SET visible = NOT visible WHERE id = ?`, id)
	return err
}

// MergePeopleRows merges source person's faces into target person, then deletes source.
func MergePeopleRows(db *sql.DB, targetID, sourceID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE faces SET person_id = ? WHERE person_id = ?`, targetID, sourceID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM people WHERE id = ?`, sourceID); err != nil {
		return err
	}
	return tx.Commit()
}

// --- Recognition Stats ---

// RecognitionStats holds aggregate counts for recognition status across all media.
type RecognitionStats struct {
	TotalImages int
	Pending     int
	Done        int
	Errored     int
	Errors      []string // distinct error messages from errored images
	Users       []UserPending
}

// UserPending is what one user's scan has still to do.
type UserPending struct {
	Username string
	Pending
}

// GetRecognitionStats returns recognition state counts for images.
func GetRecognitionStats(db *sql.DB) (RecognitionStats, error) {
	var s RecognitionStats
	err := db.QueryRow(`SELECT
		COUNT(*),
		COALESCE(SUM(CASE WHEN recognition_state = 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN recognition_state = 1 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN recognition_state = 2 THEN 1 ELSE 0 END), 0)
		FROM media WHERE mime_type LIKE 'image/%'`).Scan(&s.TotalImages, &s.Pending, &s.Done, &s.Errored)
	if err != nil {
		return s, err
	}
	if s.Errored > 0 {
		rows, err := db.Query(`SELECT DISTINCT recognition_error FROM media
			WHERE recognition_state = 2 AND recognition_error != '' LIMIT 10`)
		if err != nil {
			return s, fmt.Errorf("recognition errors: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var msg string
			if err := rows.Scan(&msg); err != nil {
				return s, fmt.Errorf("scan recognition error: %w", err)
			}
			s.Errors = append(s.Errors, msg)
		}
		if err := rows.Err(); err != nil {
			return s, fmt.Errorf("recognition errors: %w", err)
		}
	}
	return s, nil
}

// PurgeRecognitions resets all recognition state and deletes derived data
// (tags, faces, people) so recognition can be re-run from scratch.
func PurgeRecognitions(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM media_tags`,
		`DELETE FROM faces`,
		`DELETE FROM people`,
		`DELETE FROM tags`,
		`UPDATE media SET recognition_state = 0, recognition_at = NULL, recognition_error = '' WHERE mime_type LIKE 'image/%'`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("purge recognitions: %s: %w", q, err)
		}
	}
	return tx.Commit()
}

// --- Settings ---

// GetSetting returns a setting value or the default.
func GetSetting(db *sql.DB, key, defaultValue string) string {
	var value string
	err := db.QueryRow(`SELECT value FROM gallery_settings WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return defaultValue
	}
	return value
}

// SetSetting sets a gallery setting.
func SetSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT INTO gallery_settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// SetSettings writes several settings together: all of them or none.
func SetSettings(db *sql.DB, kv [][2]string) error {
	if len(kv) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin settings: %w", err)
	}
	defer tx.Rollback()
	for _, p := range kv {
		if _, err := tx.Exec(`INSERT INTO gallery_settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, p[0], p[1]); err != nil {
			return fmt.Errorf("set %s: %w", p[0], err)
		}
	}
	return tx.Commit()
}

// --- Typed Settings ---

// setting is a typed wrapper around gallery_settings key/value pairs.
// It centralizes defaults and parsing so callers don't duplicate conversion logic.
type setting[T any] struct {
	key          string
	defaultValue T
	parse        func(s string) (T, error)
	stringify    func(v T) string
}

func (s *setting[T]) Get(db *sql.DB) (T, error) {
	var zero T
	var value string
	err := db.QueryRow(`SELECT value FROM gallery_settings WHERE key = ?`, s.key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return s.defaultValue, nil
	}
	if err != nil {
		return zero, err
	}
	if value == "" {
		return s.defaultValue, nil
	}
	return s.parse(value)
}

func (s *setting[T]) Set(db *sql.DB, value T) error {
	str := fmt.Sprint(value)
	if s.stringify != nil {
		str = s.stringify(value)
	}
	_, err := db.Exec(`INSERT INTO gallery_settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, s.key, str)
	return err
}

// MustGet is like Get but logs an error and returns the default.
func (s *setting[T]) MustGet(db *sql.DB) T {
	v, err := s.Get(db)
	if err != nil {
		slog.Warn("gallery setting; using default", "key", s.key, "default", s.defaultValue, "err", err)
		return s.defaultValue
	}
	return v
}

func boolSetting(key string, defaultValue bool) setting[bool] {
	return setting[bool]{
		key: key, defaultValue: defaultValue,
		parse: func(s string) (bool, error) { return s == "true", nil },
		stringify: func(v bool) string {
			if v {
				return "true"
			}
			return ""
		},
	}
}

func floatSetting(key string, defaultValue float64) setting[float64] {
	return setting[float64]{
		key: key, defaultValue: defaultValue,
		parse:     func(s string) (float64, error) { return strconv.ParseFloat(s, 64) },
		stringify: func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) },
	}
}

func intSetting(key string, defaultValue int) setting[int] {
	return setting[int]{
		key: key, defaultValue: defaultValue,
		parse:     strconv.Atoi,
		stringify: strconv.Itoa,
	}
}

func stringSetting[T ~string](key string, defaultValue T) setting[T] {
	return setting[T]{
		key: key, defaultValue: defaultValue,
		parse:     func(s string) (T, error) { return T(s), nil },
		stringify: func(v T) string { return string(v) },
	}
}

// Enum types.

// RecognitionBackendType identifies which recognition backend to use.
type RecognitionBackendType string

const (
	BackendNone   RecognitionBackendType = "none"
	BackendPigo   RecognitionBackendType = "pigo"
	BackendOllama RecognitionBackendType = "ollama"
	BackendOpenAI RecognitionBackendType = "openai"
	BackendOnnx   RecognitionBackendType = "onnx"
)

// TagModel identifies which ONNX model to use for image tagging.
type TagModel string

const (
	TagModelMobileNet TagModel = "mobilenet" // ImageNet 1000-class (legacy)
	TagModelJoyTag    TagModel = "joytag"    // Multi-label, 5813 tags, 448×448
	TagModelWD14      TagModel = "wd14"      // Multi-label, 10861 tags, 448×448 NHWC
)

// DuplicateAlgorithm identifies which perceptual hash algorithm to use.
type DuplicateAlgorithm string

const (
	AlgoDifference DuplicateAlgorithm = "difference"
	AlgoAverage    DuplicateAlgorithm = "average"
	AlgoPHash      DuplicateAlgorithm = "phash"
	AlgoMedian     DuplicateAlgorithm = "median"
	AlgoBlockMean  DuplicateAlgorithm = "blockmean"
)

// All gallery settings.
var (
	// Tag classification — independent backend
	SettingTagBackend       = stringSetting("tag_backend", BackendNone)
	SettingTagModel         = stringSetting("tag_model", TagModelJoyTag)
	SettingTagMinConfidence = floatSetting("tag_min_confidence", 0.35)

	// Face detection — independent backend
	SettingFaceBackend       = stringSetting("face_backend", BackendNone)
	SettingFaceMinConfidence = floatSetting("face_min_confidence", 0.7)
	SettingClusterThreshold  = floatSetting("cluster_threshold", 0.35)
	SettingAgePenaltyWeight  = floatSetting("age_penalty_weight", 0.0) // 0 = disabled, 0.02 = moderate

	// Shared backend configuration (used by both tag + face when ONNX/Ollama/OpenAI)
	SettingRecognitionURL    = stringSetting("recognition_url", "")
	SettingRecognitionAPIKey = stringSetting("recognition_api_key", "")
	SettingRecognitionModel  = stringSetting("recognition_model", "")
	SettingOnnxLibraryPath   = stringSetting("onnx_library_path", "")
	SettingOnnxModelsDir     = stringSetting("onnx_models_dir", "")

	// OCR
	SettingOCREnabled = boolSetting("ocr_enabled", true)

	// Duplicates
	SettingDuplicateDetectionEnabled = boolSetting("duplicate_detection_enabled", true)
	SettingDuplicateAlgorithm        = stringSetting("duplicate_algorithm", AlgoDifference)
	SettingDuplicateDistance         = intSetting("duplicate_distance", 5)
	SettingDuplicateAlgorithmUsed    = stringSetting[string]("duplicate_algorithm_used", "")

	// Processing
	SettingPreviewGenerationEnabled = boolSetting("preview_generation_enabled", true)
	SettingQualityScoringEnabled    = boolSetting("quality_scoring_enabled", true)

	// Worker resource limits
	SettingWorkerMaxRSS       = intSetting("worker_max_rss_mb", 2048)
	SettingRecognitionThreads = intSetting("recognition_threads", 2)

	// Scan status (internal)
	SettingScanRunning   = boolSetting("scan_running", false)
	SettingScanLastRun   = intSetting("scan_last_run", 0)
	SettingScanLastError = stringSetting[string]("scan_last_error", "")

	// File rename
	SettingRenamePattern = stringSetting[string]("rename_pattern",
		"{{.Year}}/{{.Month}}/{{.Year}}-{{.Month}}-{{.Day}}_{{.Hour}}{{.Minute}}{{.Second}}{{.Ext}}")

	// Scan folders (per-user)
	SettingScanFolders = stringSetting[string]("scan_folders", "Photos/")

	// Legacy aliases — read during migration from old settings
	SettingRecognitionEnabled   = boolSetting("recognition_enabled", false)
	SettingRecognitionBackend   = stringSetting("recognition_backend", BackendNone)
	SettingFaceDetectionEnabled = boolSetting("face_detection_enabled", true)
)

// --- Scan Folder Matching ---

// ParseScanFolders parses the newline-delimited scan folders setting into patterns.
// A bare folder name like "Photos/" is treated as a prefix match on "Photos/".
// Patterns with glob characters use path.Match per path segment.
func ParseScanFolders(db *sql.DB) []string {
	raw := SettingScanFolders.MustGet(db)
	var patterns []string
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			patterns = append(patterns, line)
		}
	}
	if len(patterns) == 0 {
		return []string{"Photos/"}
	}
	return patterns
}

// TrimScanPath strips the leading slash the files plugin stores on every path
// ("/Photos/a.jpg"), so it can be compared against a scan pattern the user wrote
// either way round.
func TrimScanPath(p string) string { return strings.TrimPrefix(p, "/") }

// matchScanFolders checks if a file path matches any configured scan folder pattern.
// Patterns ending in "/" are prefix matches. Others use path.Match against each
// path segment up to the pattern depth.
func matchScanFolders(filePath string, patterns []string) bool {
	filePath = TrimScanPath(filePath)
	for _, p := range patterns {
		p = TrimScanPath(p)
		if p == "" {
			return true // "/" — everything
		}
		if strings.HasSuffix(p, "/") {
			// Prefix match: "Photos/" matches "Photos/foo.jpg", "Photos/sub/bar.jpg"
			if strings.HasPrefix(filePath, p) {
				return true
			}
			continue
		}
		// Glob match: match the pattern against the file path.
		// path.Match only works per-segment, so for patterns like "Photos/*.jpg"
		// we match against the same depth of the file path.
		if matched, _ := path.Match(p, filePath); matched {
			return true
		}
		// Also try matching just the portion of the path up to the pattern depth.
		// e.g., pattern "Photos/*.jpg" against "Photos/foo.jpg"
		patternDepth := strings.Count(p, "/")
		parts := strings.SplitN(filePath, "/", patternDepth+2)
		if len(parts) > patternDepth+1 {
			truncated := strings.Join(parts[:patternDepth+1], "/")
			if matched, _ := path.Match(p, truncated); matched {
				return true
			}
		}
	}
	return false
}

// --- Rename Log ---

// LogRename records a file rename for undo support.
func LogRename(db *sql.DB, batchID, fileID int64, oldPath, newPath string) error {
	_, err := db.Exec(`INSERT INTO rename_log (batch_id, file_id, old_path, new_path) VALUES (?, ?, ?, ?)`,
		batchID, fileID, oldPath, newPath)
	return err
}

// LastRenameBatch returns the most recent rename batch entries.
func LastRenameBatch(db *sql.DB) (int64, []struct {
	FileID  int64
	OldPath string
	NewPath string
}, error) {
	var batchID int64
	err := db.QueryRow(`SELECT COALESCE(MAX(batch_id), 0) FROM rename_log`).Scan(&batchID)
	if err != nil || batchID == 0 {
		return 0, nil, err
	}
	rows, err := db.Query(`SELECT file_id, old_path, new_path FROM rename_log WHERE batch_id = ? ORDER BY id`, batchID)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var entries []struct {
		FileID  int64
		OldPath string
		NewPath string
	}
	for rows.Next() {
		var e struct {
			FileID  int64
			OldPath string
			NewPath string
		}
		if err := rows.Scan(&e.FileID, &e.OldPath, &e.NewPath); err != nil {
			return 0, nil, err
		}
		entries = append(entries, e)
	}
	return batchID, entries, rows.Err()
}

// NextRenameBatchID returns the next batch ID for rename operations.
func NextRenameBatchID(db *sql.DB) (int64, error) {
	var id int64
	err := db.QueryRow(`SELECT COALESCE(MAX(batch_id), 0) + 1 FROM rename_log`).Scan(&id)
	return id, err
}

// DeleteRenameBatch deletes all entries in a rename batch.
func DeleteRenameBatch(db *sql.DB, batchID int64) error {
	_, err := db.Exec(`DELETE FROM rename_log WHERE batch_id = ?`, batchID)
	return err
}

// formatDayID computes day ID from a unix timestamp.
func formatDayID(ts int64) int64 {
	return ts / 86400
}

// fmtMonth returns a "YYYY-MM" string for a unix timestamp.
func fmtMonth(ts int64) string {
	t := time.Unix(ts, 0).UTC()
	return fmt.Sprintf("%d-%02d", t.Year(), t.Month())
}

// fmtMonthName returns a human-readable month name for a "YYYY-MM" key.
func fmtMonthName(key string) string {
	t, err := time.Parse("2006-01", key)
	if err != nil {
		return key
	}
	return t.Format("January 2006")
}

// Pending counts a user's photos still waiting for each stage of the scan.
type Pending struct {
	Metadata, Quality, Fingerprint, OCR, Recognition int
	Failed                                           int // photos with a stage that failed
}

// Any reports whether anything is waiting.
func (p Pending) Any() bool {
	return p.Metadata+p.Quality+p.Fingerprint+p.OCR+p.Recognition > 0
}

func (p Pending) String() string {
	return fmt.Sprintf("metadata=%d quality=%d fingerprint=%d ocr=%d recognition=%d failed=%d",
		p.Metadata, p.Quality, p.Fingerprint, p.OCR, p.Recognition, p.Failed)
}

// PendingWork counts what is waiting, in one pass over the media table.
// A stage that is switched off still counts what it would do.
func PendingWork(db *sql.DB) (Pending, error) {
	var p Pending
	err := db.QueryRow(`SELECT
		COALESCE(SUM(width = 0), 0),
		COALESCE(SUM(mime_type LIKE 'image/%' AND width > 0 AND quality_score = -1), 0),
		COALESCE(SUM(mime_type LIKE 'image/%' AND width > 0 AND fingerprint = ''), 0),
		COALESCE(SUM(mime_type LIKE 'image/%' AND width > 0 AND ocr_text = ''), 0),
		COALESCE(SUM(mime_type LIKE 'image/%' AND width > 0 AND recognition_state = 0), 0),
		COALESCE(SUM(scan_error != '' OR recognition_state = 2), 0)
		FROM media`).Scan(&p.Metadata, &p.Quality, &p.Fingerprint, &p.OCR, &p.Recognition, &p.Failed)
	if err != nil {
		return p, fmt.Errorf("count pending work: %w", err)
	}
	return p, nil
}
