package gallery

import (
	"context"
	"database/sql"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/vfs"

	_ "golang.org/x/image/webp"
)

// resolveLocalPath resolves a user's file path to a local filesystem path.
// It tries VFS Unwrap + LocalRooter first, then falls back to UserFilesDir.
func (p *Plugin) resolveLocalPath(username, filePath string) string {
	if fs := p.ctx.UserVFS(username); fs != nil {
		if u, ok := fs.(vfs.Unwrapper); ok {
			if backend, subpath, err := u.Unwrap("/" + filePath); err == nil {
				if lr, ok := backend.(vfs.LocalRooter); ok {
					return filepath.Join(lr.LocalRoot(), subpath)
				}
			}
		}
	}
	return filepath.Join(p.ctx.UserFilesDir(username), filePath)
}

// isMediaMIME reports whether the MIME type is a supported image or video type.
func isMediaMIME(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/") || strings.HasPrefix(mimeType, "video/")
}

// scanAllUsersErr scans all user data directories for new/changed media.
func (p *Plugin) scanAllUsersErr(ctx context.Context) (warnings []string, err error) {
	entries, err := os.ReadDir(p.ctx.DataDir())
	if err != nil {
		return nil, fmt.Errorf("read data dir: %w", err)
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return warnings, ctx.Err()
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		username := entry.Name()
		// Only scan users that have a files plugin DB.
		filesDBPath := filepath.Join(p.ctx.DataDir(), username, "app_data", "files", "plugin.db")
		if _, err := os.Stat(filesDBPath); err != nil {
			continue
		}
		userWarnings, err := p.scanUser(ctx, username)
		warnings = append(warnings, userWarnings...)
		if err != nil {
			p.log.Error("scan user", "user", username, "err", err)
			warnings = append(warnings, fmt.Sprintf("%s: %v", username, err))
		}
	}
	return warnings, nil
}

// ScanAllUsers runs the scan pipeline for all users. Exported for manual trigger.
func (p *Plugin) ScanAllUsers(ctx context.Context) {
	p.runScan(ctx)
}

// ScanUser runs the scan pipeline for a single user. Exported for manual trigger.
func (p *Plugin) ScanUser(ctx context.Context, username string) error {
	_, err := p.scanUser(ctx, username)
	return err
}

func (p *Plugin) scanUser(ctx context.Context, username string) (warnings []string, err error) {
	galleryDB, err := p.DB(username)
	if err != nil {
		return nil, fmt.Errorf("gallery db: %w", err)
	}
	filesDB, err := p.files.DB(username)
	if err != nil {
		return nil, fmt.Errorf("files db: %w", err)
	}

	// Stage 1: File discovery
	if err := p.discoverFiles(ctx, galleryDB, filesDB, username); err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}

	// Stage 2: Metadata extraction (EXIF for images)
	if err := p.extractMetadata(ctx, galleryDB, username); err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}

	// Stage 3: OCR text extraction (if tesseract available)
	if err := p.extractOCR(ctx, galleryDB, username); err != nil {
		p.log.Warn("ocr", "user", username, "err", err)
		warnings = append(warnings, fmt.Sprintf("%s: ocr: %v", username, err))
	}

	// Stage 4: Reverse geocoding
	p.reverseGeocode(galleryDB)

	// Stage 5: Auto-album generation
	if err := p.generateAutoAlbums(ctx, galleryDB); err != nil {
		return warnings, fmt.Errorf("auto-albums: %w", err)
	}

	// Stage 6: Image recognition (if configured)
	if err := p.runRecognition(ctx, galleryDB, username); err != nil {
		p.log.Warn("recognition", "user", username, "err", err)
		warnings = append(warnings, fmt.Sprintf("%s: recognition: %v", username, err))
	}

	// Stage 7: Face clustering
	if err := clusterFaces(galleryDB); err != nil {
		p.log.Warn("clustering", "user", username, "err", err)
		warnings = append(warnings, fmt.Sprintf("%s: clustering: %v", username, err))
	}

	return warnings, nil
}

// discoverFiles syncs the gallery index with the files plugin database.
func (p *Plugin) discoverFiles(ctx context.Context, galleryDB, filesDB *sql.DB, username string) error {
	// Get all media files from the files plugin.
	rows, err := filesDB.Query(`SELECT id, path, mime_type, etag FROM files
		WHERE is_dir = 0 AND (mime_type LIKE 'image/%' OR mime_type LIKE 'video/%')`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type fileEntry struct {
		ID       int64
		Path     string
		MimeType string
		ETag     string
	}
	var fileEntries []fileEntry
	for rows.Next() {
		var f fileEntry
		if err := rows.Scan(&f.ID, &f.Path, &f.MimeType, &f.ETag); err != nil {
			return err
		}
		fileEntries = append(fileEntries, f)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Get existing indexed media.
	existing, err := AllMediaFileIDs(galleryDB)
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	fileIDsSeen := make(map[int64]bool, len(fileEntries))

	for _, f := range fileEntries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fileIDsSeen[f.ID] = true

		existingETag, exists := existing[f.ID]
		if exists && existingETag == f.ETag {
			continue // unchanged
		}

		if exists {
			// File changed — delete old row (will be re-inserted with new metadata)
			if err := DeleteMediaByFileID(galleryDB, f.ID); err != nil {
				p.log.Error("delete changed media", "file_id", f.ID, "err", err)
				continue
			}
		}

		// Insert new media row with minimal info; metadata extraction happens in stage 2.
		m := &Media{
			FileID:      f.ID,
			FilePath:    f.Path,
			FileETag:    f.ETag,
			MimeType:    f.MimeType,
			DateIndexed: now,
		}
		if _, err := InsertMedia(galleryDB, m); err != nil {
			p.log.Error("insert media", "file_id", f.ID, "path", f.Path, "err", err)
		}
	}

	// Remove media for deleted files.
	for fileID := range existing {
		if !fileIDsSeen[fileID] {
			if err := DeleteMediaByFileID(galleryDB, fileID); err != nil {
				p.log.Error("cleanup deleted file", "file_id", fileID, "err", err)
			}
		}
	}

	return nil
}

// extractMetadata reads EXIF data from images that haven't been processed yet.
func (p *Plugin) extractMetadata(ctx context.Context, db *sql.DB, username string) error {
	// Find media that needs metadata extraction (width == 0 means unprocessed).
	rows, err := db.Query(`SELECT ` + mediaCols + ` FROM media WHERE width = 0 AND height = 0`)
	if err != nil {
		return err
	}
	defer rows.Close()

	items, err := scanMediaRows(rows)
	if err != nil {
		return err
	}

	for i := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		m := &items[i]
		srcPath := p.resolveLocalPath(username, m.FilePath)

		if m.IsVideo() {
			extractVideoMetadata(srcPath, m)
		} else {
			extractImageMetadata(srcPath, m)
		}

		m.DateIndexed = time.Now().Unix()
		if err := UpdateMediaMetadata(db, m); err != nil {
			p.log.Error("update metadata", "id", m.ID, "err", err)
		}
	}

	return nil
}

// extractOCR runs tesseract on images that haven't been OCR'd yet.
func (p *Plugin) extractOCR(ctx context.Context, db *sql.DB, username string) error {
	if !TesseractAvailable() {
		return nil
	}
	sysDB, err := p.SystemDB()
	if err == nil && GetSetting(sysDB, "ocr_enabled", "true") != "true" {
		return nil
	}

	rows, err := db.Query(`SELECT ` + mediaCols + ` FROM media
		WHERE ocr_text = '' AND mime_type LIKE 'image/%' AND width > 0
		LIMIT 50`)
	if err != nil {
		return err
	}
	defer rows.Close()

	items, err := scanMediaRows(rows)
	if err != nil {
		return err
	}

	for i := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		m := &items[i]
		srcPath := p.resolveLocalPath(username, m.FilePath)

		text, err := tesseractOCR(srcPath)
		if err != nil {
			p.log.Warn("ocr failed", "path", m.FilePath, "err", err)
			// Mark as attempted so we don't retry — store a sentinel.
			db.Exec(`UPDATE media SET ocr_text = ' ' WHERE id = ?`, m.ID)
			continue
		}
		if text == "" {
			// No text found — store a single space sentinel to avoid re-processing.
			text = " "
		}

		db.Exec(`UPDATE media SET ocr_text = ? WHERE id = ?`, text, m.ID)
	}

	return nil
}

// extractImageMetadata reads EXIF and image dimensions from an image file.
func extractImageMetadata(path string, m *Media) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	// Read image dimensions.
	cfg, _, err := image.DecodeConfig(f)
	if err == nil {
		m.Width = cfg.Width
		m.Height = cfg.Height
	}

	// Reset for EXIF reading.
	f.Seek(0, 0)
	readEXIF(f, m)
}

// extractVideoMetadata extracts metadata from a video file using ffprobe.
func extractVideoMetadata(path string, m *Media) {
	info, err := ffprobeInfo(path)
	if err != nil {
		// ffprobe not available or failed — set dimensions to 1x1 so we don't retry.
		m.Width = 1
		m.Height = 1
		return
	}

	m.Width = info.Width
	m.Height = info.Height
	m.DurationMs = &info.DurationMs
	m.VideoCodec = info.VideoCodec
	m.AudioCodec = info.AudioCodec
	if info.CreationTime != nil {
		ts := info.CreationTime.Unix()
		m.DateTaken = &ts
	}
}

// reverseGeocode fills in place_name for media with GPS coordinates but no city.
func (p *Plugin) reverseGeocode(db *sql.DB) {
	rows, err := db.Query(`SELECT id, latitude, longitude FROM media
		WHERE latitude IS NOT NULL AND longitude IS NOT NULL AND city = ''`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var lat, lon float64
		if err := rows.Scan(&id, &lat, &lon); err != nil {
			continue
		}
		city, region, country, placeName := p.geocoder.Reverse(lat, lon)
		db.Exec(`UPDATE media SET city = ?, region = ?, country = ?, place_name = ? WHERE id = ?`,
			city, region, country, placeName, id)
	}
}

// generateAutoAlbums creates auto-albums by date (month) and folder.
func (p *Plugin) generateAutoAlbums(ctx context.Context, db *sql.DB) error {
	rows, err := db.Query(`SELECT id, file_path, date_taken, date_indexed, camera_make, camera_model, city, country
		FROM media ORDER BY COALESCE(date_taken, date_indexed) DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type mediaRef struct {
		ID          int64
		FilePath    string
		DateTaken   *int64
		DateIndexed int64
		CameraMake  string
		CameraModel string
		City        string
		Country     string
	}

	var refs []mediaRef
	for rows.Next() {
		var r mediaRef
		if err := rows.Scan(&r.ID, &r.FilePath, &r.DateTaken, &r.DateIndexed, &r.CameraMake, &r.CameraModel, &r.City, &r.Country); err != nil {
			return err
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range refs {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		ts := r.DateIndexed
		if r.DateTaken != nil {
			ts = *r.DateTaken
		}

		// Date album (by month)
		monthKey := fmtMonth(ts)
		monthName := fmtMonthName(monthKey)
		if albumID, err := UpsertAutoAlbum(db, AlbumDate, monthKey, monthName); err == nil {
			AddMediaToAlbum(db, albumID, r.ID)
		}

		// Folder album
		dir := filepath.Dir(r.FilePath)
		if dir != "" && dir != "." {
			folderName := dir
			if i := strings.LastIndex(dir, "/"); i >= 0 {
				folderName = dir[i+1:]
			}
			if albumID, err := UpsertAutoAlbum(db, AlbumFolder, dir, folderName); err == nil {
				AddMediaToAlbum(db, albumID, r.ID)
			}
		}

		// Camera album
		if r.CameraModel != "" {
			cameraKey := r.CameraMake + "/" + r.CameraModel
			cameraName := strings.TrimSpace(r.CameraMake + " " + r.CameraModel)
			if albumID, err := UpsertAutoAlbum(db, AlbumCamera, cameraKey, cameraName); err == nil {
				AddMediaToAlbum(db, albumID, r.ID)
			}
		}

		// Location album (by city)
		if r.City != "" {
			locationKey := r.City + "/" + r.Country
			locationName := r.City
			if r.Country != "" {
				locationName = r.City + ", " + r.Country
			}
			if albumID, err := UpsertAutoAlbum(db, AlbumLocation, locationKey, locationName); err == nil {
				AddMediaToAlbum(db, albumID, r.ID)
			}
		}
	}

	return nil
}

// PreviewPath returns the path for a media thumbnail, using the files plugin for images
// or the video thumbnail cache for videos.
func (p *Plugin) PreviewPath(username string, m *Media, size int) (*os.File, error) {
	if m.IsVideo() {
		return p.videoThumbnail(username, m, size)
	}
	return p.files.Preview(username, m.FilePath, size)
}

// videoThumbnail returns a cached video thumbnail, generating it if needed.
func (p *Plugin) videoThumbnail(username string, m *Media, size int) (*os.File, error) {
	cacheDir := filepath.Join(p.ctx.DataDir(), username, "app_data", PluginID, "video_thumbs")
	cachePath := filepath.Join(cacheDir, fmt.Sprintf("%s_%d.jpg", m.FileETag, size))

	if f, err := os.Open(cachePath); err == nil {
		return f, nil
	}

	if !FFmpegAvailable() {
		return nil, fmt.Errorf("ffmpeg not available")
	}

	srcPath := p.resolveLocalPath(username, m.FilePath)

	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, err
	}

	if err := ffmpegThumbnail(srcPath, cachePath, size); err != nil {
		return nil, err
	}

	return os.Open(cachePath)
}
