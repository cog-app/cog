package gallery

import (
	"database/sql"
	"fmt"
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
	ID      int64
	Name    string
	TagType int
	Count   int // populated by list queries
}

// Person represents a recognized person.
type Person struct {
	ID                   int64
	Name                 string
	RepresentativeFaceID *int64
	Visible              bool
	CreatedAt            int64
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
	m.fingerprint, m.quality_score`

func scanMedia(row interface{ Scan(...any) error }) (*Media, error) {
	var m Media
	err := row.Scan(
		&m.ID, &m.FileID, &m.FilePath, &m.FileETag, &m.MimeType,
		&m.Width, &m.Height, &m.Orientation,
		&m.DateTaken, &m.DateIndexed, &m.Latitude, &m.Longitude, &m.Altitude,
		&m.CameraMake, &m.CameraModel, &m.LensModel, &m.FocalLength, &m.FNumber, &m.ExposureTime,
		&m.ISO, &m.Flash, &m.DurationMs, &m.VideoCodec, &m.AudioCodec,
		&m.PlaceName, &m.Country, &m.Region, &m.City, &m.RecognitionState, &m.RecognitionAt,
		&m.OCRText, &m.Fingerprint, &m.QualityScore,
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
		ORDER BY COALESCE(m.date_taken, m.date_indexed) DESC LIMIT ?`, limit)
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
func InsertMedia(db *sql.DB, m *Media) (int64, error) {
	res, err := db.Exec(`INSERT INTO media (
		file_id, file_path, file_etag, mime_type, width, height, orientation,
		date_taken, date_indexed, latitude, longitude, altitude,
		camera_make, camera_model, lens_model, focal_length, f_number, exposure_time,
		iso, flash, duration_ms, video_codec, audio_codec,
		place_name, country, region, city, recognition_state, recognition_at, ocr_text
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.FileID, m.FilePath, m.FileETag, m.MimeType, m.Width, m.Height, m.Orientation,
		m.DateTaken, m.DateIndexed, m.Latitude, m.Longitude, m.Altitude,
		m.CameraMake, m.CameraModel, m.LensModel, m.FocalLength, m.FNumber, m.ExposureTime,
		m.ISO, m.Flash, m.DurationMs, m.VideoCodec, m.AudioCodec,
		m.PlaceName, m.Country, m.Region, m.City, m.RecognitionState, m.RecognitionAt,
		m.OCRText,
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
		recognition_state = ?, recognition_at = ?, ocr_text = ?
		WHERE id = ?`,
		m.FilePath, m.FileETag, m.MimeType, m.Width, m.Height, m.Orientation,
		m.DateTaken, m.DateIndexed, m.Latitude, m.Longitude, m.Altitude,
		m.CameraMake, m.CameraModel, m.LensModel, m.FocalLength, m.FNumber,
		m.ExposureTime, m.ISO, m.Flash, m.DurationMs, m.VideoCodec, m.AudioCodec,
		m.PlaceName, m.Country, m.Region, m.City,
		m.RecognitionState, m.RecognitionAt, m.OCRText,
		m.ID,
	)
	return err
}

// ResetMediaByFileID updates a changed file's media row in-place, preserving
// the row ID (and thus all associated faces/media_tags/album_media).
// It resets metadata and recognition state so both get re-extracted.
func ResetMediaByFileID(db *sql.DB, fileID int64, path, etag, mime string, dateIndexed int64) error {
	_, err := db.Exec(`UPDATE media SET
		file_path = ?, file_etag = ?, mime_type = ?, date_indexed = ?,
		width = 0, height = 0, orientation = 0,
		date_taken = NULL, latitude = NULL, longitude = NULL, altitude = NULL,
		camera_make = '', camera_model = '', lens_model = '',
		focal_length = NULL, f_number = NULL, exposure_time = '',
		iso = NULL, flash = NULL, duration_ms = NULL, video_codec = '', audio_codec = '',
		place_name = '', country = '', region = '', city = '',
		recognition_state = 0, recognition_at = NULL, ocr_text = '', fingerprint = ''
		WHERE file_id = ?`, path, etag, mime, dateIndexed, fileID)
	return err
}

// DeleteMediaByFileID deletes a media row by its files plugin file_id.
func DeleteMediaByFileID(db *sql.DB, fileID int64) error {
	_, err := db.Exec(`DELETE FROM media WHERE file_id = ?`, fileID)
	return err
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

// AllMediaFileIDs returns a set of file_id -> etag for all indexed media.
func AllMediaFileIDs(db *sql.DB) (map[int64]string, error) {
	rows, err := db.Query(`SELECT file_id, file_etag FROM media`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]string)
	for rows.Next() {
		var id int64
		var etag string
		if err := rows.Scan(&id, &etag); err != nil {
			return nil, err
		}
		result[id] = etag
	}
	return result, rows.Err()
}

// --- Albums ---

// ListAlbums returns all albums with their media counts.
func ListAlbums(db *sql.DB, albumType int) ([]Album, error) {
	q := `SELECT a.id, a.name, a.description, a.album_type, a.auto_key,
		a.cover_media_id, a.sort_order, a.created_at, a.updated_at,
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
		a.cover_media_id, a.sort_order, a.created_at, a.updated_at,
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

// UpsertAutoAlbum creates or finds an auto-album and returns its ID.
func UpsertAutoAlbum(db *sql.DB, albumType int, autoKey, name string) (int64, error) {
	res, err := db.Exec(`INSERT INTO albums (name, album_type, auto_key)
		VALUES (?, ?, ?)
		ON CONFLICT(album_type, auto_key) DO UPDATE SET updated_at = unixepoch()`,
		name, albumType, autoKey)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		// ON CONFLICT hit — look up existing
		err = db.QueryRow(`SELECT id FROM albums WHERE album_type = ? AND auto_key = ?`,
			albumType, autoKey).Scan(&id)
	}
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

// SetAlbumCover sets the cover media for an album.
func SetAlbumCover(db *sql.DB, albumID, mediaID int64) error {
	_, err := db.Exec(`UPDATE albums SET cover_media_id = ? WHERE id = ?`, mediaID, albumID)
	return err
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
func UpsertTag(db *sql.DB, name string, tagType int) (int64, error) {
	res, err := db.Exec(`INSERT INTO tags (name, tag_type) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET tag_type = excluded.tag_type`, name, tagType)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		err = db.QueryRow(`SELECT id FROM tags WHERE name = ?`, name).Scan(&id)
	}
	return id, err
}

// AddMediaTag links a tag to a media item.
func AddMediaTag(db *sql.DB, mediaID, tagID int64, confidence float64) error {
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
		var confidence float64
		if err := rows.Scan(&t.ID, &t.Name, &t.TagType, &confidence); err != nil {
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
}

// FacesForMedia returns all faces detected in a media item.
func FacesForMedia(db *sql.DB, mediaID int64) ([]Face, error) {
	rows, err := db.Query(`SELECT f.id, f.media_id, f.person_id, COALESCE(p.name, ''), f.x, f.y, f.w, f.h, f.estimated_age
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
		if err := rows.Scan(&f.ID, &f.MediaID, &f.PersonID, &f.Name, &f.X, &f.Y, &f.W, &f.H, &f.EstimatedAge); err != nil {
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
	rows, err := db.Query(`SELECT f.id, f.media_id, f.person_id, COALESCE(p.name, ''), f.x, f.y, f.w, f.h, f.estimated_age
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
		if err := rows.Scan(&f.ID, &f.MediaID, &f.PersonID, &f.Name, &f.X, &f.Y, &f.W, &f.H, &f.EstimatedAge); err != nil {
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
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO people (name) VALUES ('')`)
	if err != nil {
		return 0, err
	}
	personID, _ := res.LastInsertId()
	if _, err := tx.Exec(`UPDATE faces SET person_id = ? WHERE id = ?`, personID, faceID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE people SET representative_face_id = ? WHERE id = ?`, faceID, personID); err != nil {
		return 0, err
	}
	return personID, tx.Commit()
}

// --- People ---

// SearchPeople returns people matching a query string, ordered by face count.
func SearchPeople(db *sql.DB, query string, excludeID int64, limit int) ([]Person, error) {
	rows, err := db.Query(`SELECT p.id, p.name,
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
		if err := rows.Scan(&p.ID, &p.Name, &p.FaceCount, &p.BestFaceID); err != nil {
			return nil, err
		}
		people = append(people, p)
	}
	return people, rows.Err()
}

// ListPeople returns all visible people with face counts and best face ID.
func ListPeople(db *sql.DB) ([]Person, error) {
	rows, err := db.Query(`SELECT id, name, representative_face_id, visible, created_at, face_count, best_face_id FROM (
		SELECT p.id, p.name, p.representative_face_id, p.visible, p.created_at,
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
			&person.Visible, &person.CreatedAt, &person.FaceCount, &person.BestFaceID); err != nil {
			return nil, err
		}
		people = append(people, person)
	}
	return people, rows.Err()
}

// GetPerson returns a single person by ID.
func GetPerson(db *sql.DB, id int64) (*Person, error) {
	var p Person
	err := db.QueryRow(`SELECT p.id, p.name, p.representative_face_id, p.visible, p.created_at,
		(SELECT COUNT(*) FROM faces f JOIN media m ON m.id = f.media_id WHERE f.person_id = p.id) AS face_count
		FROM people p WHERE p.id = ?`, id).Scan(
		&p.ID, &p.Name, &p.RepresentativeFaceID, &p.Visible, &p.CreatedAt, &p.FaceCount)
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
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var msg string
				if rows.Scan(&msg) == nil {
					s.Errors = append(s.Errors, msg)
				}
			}
		}
	}
	return s, nil
}

// PurgeRecognitions resets all recognition state and deletes derived data
// (tags, faces, people) so recognition can be re-run from scratch.
func PurgeRecognitions(db *sql.DB) error {
	_, err := db.Exec(`DELETE FROM media_tags;
		DELETE FROM faces;
		DELETE FROM people;
		DELETE FROM tags;
		UPDATE media SET recognition_state = 0, recognition_at = NULL, recognition_error = ''
			WHERE mime_type LIKE 'image/%'`)
	return err
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
