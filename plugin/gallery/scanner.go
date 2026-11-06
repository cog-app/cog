package gallery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/vfs"

	_ "golang.org/x/image/webp"
)

// resolveLocalPath returns the local path of a user's media file, for the
// programs and decoders that read one: the file must be on a mount backed
// by a local directory. The path is checked to stay within that directory
// once symlinks are followed, so a link in the user's files cannot hand a
// reader something outside them.
func (p *Plugin) resolveLocalPath(username, filePath string) (string, error) {
	fsys, err := p.ctx.UserVFS(username)
	if err != nil {
		return "", err
	}
	u, ok := fsys.(vfs.Unwrapper)
	if !ok {
		return "", fmt.Errorf("media %s: no mounts", filePath)
	}
	backend, subpath, err := u.Unwrap("/" + filePath)
	if err != nil {
		return "", fmt.Errorf("media %s: %w", filePath, err)
	}
	lr, ok := backend.(vfs.LocalRooter)
	if !ok {
		return "", fmt.Errorf("media %s: not on a local volume", filePath)
	}
	root, err := filepath.EvalSymlinks(lr.LocalRoot())
	if err != nil {
		return "", fmt.Errorf("media %s: %w", filePath, err)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(path.Clean("/"+subpath))))
	if err != nil {
		return "", fmt.Errorf("media %s: %w", filePath, err)
	}
	if rel, err := filepath.Rel(root, real); err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("media %s: leads outside its volume", filePath)
	}
	return real, nil
}

// openMedia opens a user's media file for reading, on any mount.
func (p *Plugin) openMedia(username, filePath string) (io.ReadCloser, error) {
	fsys, err := p.ctx.UserVFS(username)
	if err != nil {
		return nil, err
	}
	return fsys.Get("/" + filePath)
}

// isMediaMIME reports whether the MIME type is a supported image or video type.
func isMediaMIME(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/") || strings.HasPrefix(mimeType, "video/")
}

// scanAllUsersErr scans all user data directories for new/changed media.
func (p *Plugin) scanAllUsersErr(ctx context.Context) (warnings []string, err error) {
	usernames, err := p.ctx.UsersOf("files")
	if err != nil {
		return nil, err
	}

	var scanned int
	for _, username := range usernames {
		if ctx.Err() != nil {
			return warnings, ctx.Err()
		}
		p.log.Info("scanning user", "user", username)
		userWarnings, err := p.scanUser(ctx, username)
		warnings = append(warnings, userWarnings...)
		if err != nil {
			p.log.Error("scan user", "user", username, "err", err)
			warnings = append(warnings, fmt.Sprintf("%s: %v", username, err))
		}
		scanned++
	}

	if scanned == 0 {
		p.log.Warn("no users with files found to scan")
	}
	return warnings, nil
}

// ScanAllUsers runs the scan pipeline for all users.
func (p *Plugin) ScanAllUsers(ctx context.Context) error {
	_, err := p.scanAllUsersErr(ctx)
	return err
}

// ScanUser runs the scan pipeline for a single user. Exported for manual trigger.
func (p *Plugin) ScanUser(ctx context.Context, username string) error {
	_, err := p.scanUser(ctx, username)
	return err
}

func (p *Plugin) scanUser(ctx context.Context, username string) (warnings []string, err error) {
	var galleryDB *sql.DB
	if p.isWorker {
		// Worker subprocess: open DB directly to avoid search hook → Bleve flock contention.
		galleryDB, err = p.directDB(username)
		if err != nil {
			return nil, fmt.Errorf("gallery db: %w", err)
		}
		defer galleryDB.Close()
	} else {
		galleryDB, err = p.DB(username)
		if err != nil {
			return nil, fmt.Errorf("gallery db: %w", err)
		}
	}
	var filesDB *sql.DB
	if p.isWorker {
		// The worker only reads the file index, and must not take the search
		// hooks: it runs beside the server, which holds the index files.
		filesDB, err = p.ctx.PluginDB(username, plugin.OfPlugin("files"), plugin.ReadOnly, plugin.NoHooks)
		if err != nil {
			return nil, fmt.Errorf("files db: %w", err)
		}
		defer filesDB.Close()
	} else {
		filesDB, err = p.ctx.PluginDB(username, plugin.OfPlugin("files"))
		if err != nil {
			return nil, fmt.Errorf("files db: %w", err)
		}
	}

	// Clean up orphaned recognition data from before CASCADE was added.
	if err := CleanOrphans(galleryDB); err != nil {
		p.log.Warn("clean orphans", "user", username, "err", err)
	}

	// Stage 1: File discovery
	if err := p.discoverFiles(ctx, galleryDB, filesDB, username); err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}

	// Stage 2: Metadata extraction (EXIF for images)
	if err := p.extractMetadata(ctx, galleryDB, username); err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}

	// Load system settings for feature gating.
	sysDB, err := p.SystemDB()
	if err != nil {
		p.log.Warn("system db; using default settings", "err", err)
		sysDB = nil
	}

	// Stage 2.5: Pre-generate thumbnails for grid view.
	if sysDB == nil || SettingPreviewGenerationEnabled.MustGet(sysDB) {
		if err := p.preGenerateThumbnails(ctx, galleryDB, username); err != nil {
			p.log.Warn("thumbnails", "user", username, "err", err)
		}
	}

	// Stage 2.6: Quality scoring (Laplacian sharpness).
	if sysDB == nil || SettingQualityScoringEnabled.MustGet(sysDB) {
		if err := p.computeQualityScores(ctx, galleryDB, username); err != nil {
			p.log.Warn("quality scores", "user", username, "err", err)
		}
	}

	// Stage 2.75: Fingerprint computation for duplicate detection.
	if err := p.computeFingerprints(ctx, galleryDB, username); err != nil {
		p.log.Warn("fingerprints", "user", username, "err", err)
		warnings = append(warnings, fmt.Sprintf("%s: fingerprints: %v", username, err))
	}

	// Stage 3: OCR text extraction (if tesseract available)
	if err := p.extractOCR(ctx, galleryDB, username); err != nil {
		p.log.Warn("ocr", "user", username, "err", err)
		warnings = append(warnings, fmt.Sprintf("%s: ocr: %v", username, err))
	}

	// Stage 4: Reverse geocoding
	if err := p.reverseGeocode(galleryDB); err != nil {
		p.log.Warn("geocode", "user", username, "err", err)
		warnings = append(warnings, fmt.Sprintf("%s: geocode: %v", username, err))
	}

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
	if err := clusterFaces(galleryDB, sysDB); err != nil {
		p.log.Warn("clustering", "user", username, "err", err)
		warnings = append(warnings, fmt.Sprintf("%s: clustering: %v", username, err))
	}

	// Stage 8: Duplicate detection
	if err := p.detectDuplicates(galleryDB); err != nil {
		p.log.Warn("duplicates", "user", username, "err", err)
		warnings = append(warnings, fmt.Sprintf("%s: duplicates: %v", username, err))
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

	// Filter by configured scan folders.
	patterns := ParseScanFolders(galleryDB)
	var filtered []fileEntry
	for _, f := range fileEntries {
		if matchScanFolders(f.Path, patterns) {
			filtered = append(filtered, f)
		}
	}
	fileEntries = filtered

	// Get existing indexed media.
	existing, err := AllMediaFileIDs(galleryDB)
	if err != nil {
		return err
	}

	now := p.ctx.Now().Unix()
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
			// File changed — update in-place to preserve row ID and associated
			// faces/media_tags/album_media. Resets metadata + recognition state.
			if err := ResetMediaByFileID(galleryDB, f.ID, f.Path, f.ETag, f.MimeType, now); err != nil {
				p.log.Error("reset changed media", "file_id", f.ID, "err", err)
			}
			continue
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
	rows, err := db.Query(`SELECT ` + mediaCols + ` FROM media m WHERE m.width = 0 AND m.height = 0`)
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
		srcPath, err := p.resolveLocalPath(username, m.FilePath)
		if err == nil {
			if m.IsVideo() {
				err = extractVideoMetadata(srcPath, m)
			} else {
				err = extractImageMetadata(srcPath, m)
			}
		}
		switch {
		case errors.Is(err, image.ErrFormat):
			// A format Go cannot measure (HEIC, TIFF): expected, and seen
			// again every scan, so not worth a warning each time.
			p.log.Debug("extract metadata", "path", m.FilePath, "err", err)
		case err != nil:
			// Whatever was read is still stored below.
			p.log.Warn("extract metadata", "path", m.FilePath, "err", err)
		}

		m.DateIndexed = p.ctx.Now().Unix()
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
	if err != nil {
		p.log.Warn("system db; running ocr by default", "err", err)
	} else if !SettingOCREnabled.MustGet(sysDB) {
		return nil
	}

	rows, err := db.Query(`SELECT ` + mediaCols + ` FROM media m
		WHERE m.ocr_text = '' AND m.mime_type LIKE 'image/%' AND m.width > 0
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
		srcPath, err := p.resolveLocalPath(username, m.FilePath)
		var text string
		if err == nil {
			text, err = tesseractOCR(srcPath)
		}
		if err != nil {
			p.log.Warn("ocr failed", "path", m.FilePath, "err", err)
			// Mark as attempted so we don't retry — store a sentinel.
			if _, err := db.Exec(`UPDATE media SET ocr_text = ' ' WHERE id = ?`, m.ID); err != nil {
				return fmt.Errorf("mark %s ocr attempted: %w", m.FilePath, err)
			}
			continue
		}
		if text == "" {
			// No text found — store a single space sentinel to avoid re-processing.
			text = " "
		}

		if _, err := db.Exec(`UPDATE media SET ocr_text = ? WHERE id = ?`, text, m.ID); err != nil {
			return fmt.Errorf("store ocr for %s: %w", m.FilePath, err)
		}
	}

	return nil
}

// extractImageMetadata reads EXIF and image dimensions from an image file.
// A format Go cannot decode leaves the dimensions unset but still reads the
// EXIF; that is reported as an error along with any failure to read the file.
func extractImageMetadata(path string, m *Media) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// Read image dimensions.
	cfg, _, decodeErr := image.DecodeConfig(f)
	if decodeErr == nil {
		m.Width = cfg.Width
		m.Height = cfg.Height
	} else {
		decodeErr = fmt.Errorf("dimensions: %w", decodeErr)
	}

	// Reset for EXIF reading.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind for exif: %w", err)
	}
	readEXIF(f, m)
	return decodeErr
}

// extractVideoMetadata extracts metadata from a video file using ffprobe.
func extractVideoMetadata(path string, m *Media) error {
	info, err := ffprobeInfo(path)
	if err != nil {
		// ffprobe not available or failed — set dimensions to 1x1 so we don't retry.
		m.Width = 1
		m.Height = 1
		return fmt.Errorf("ffprobe: %w", err)
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
	return nil
}

// reverseGeocode fills in place_name for media with GPS coordinates but no city.
func (p *Plugin) reverseGeocode(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, latitude, longitude FROM media
		WHERE latitude IS NOT NULL AND longitude IS NOT NULL AND city = ''`)
	if err != nil {
		return fmt.Errorf("list ungeocoded media: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var lat, lon float64
		if err := rows.Scan(&id, &lat, &lon); err != nil {
			return fmt.Errorf("scan ungeocoded media: %w", err)
		}
		city, region, country, placeName := p.geocoder.Reverse(lat, lon)
		if _, err := db.Exec(`UPDATE media SET city = ?, region = ?, country = ?, place_name = ? WHERE id = ?`,
			city, region, country, placeName, id); err != nil {
			return fmt.Errorf("store place for media %d: %w", id, err)
		}
	}
	return rows.Err()
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
		if err := addToAutoAlbum(db, AlbumDate, monthKey, monthName, r.ID); err != nil {
			return err
		}

		// Folder album
		dir := filepath.Dir(r.FilePath)
		if dir != "" && dir != "." {
			folderName := dir
			if i := strings.LastIndex(dir, "/"); i >= 0 {
				folderName = dir[i+1:]
			}
			if err := addToAutoAlbum(db, AlbumFolder, dir, folderName, r.ID); err != nil {
				return err
			}
		}

		// Camera album
		if r.CameraModel != "" {
			cameraKey := r.CameraMake + "/" + r.CameraModel
			cameraName := strings.TrimSpace(r.CameraMake + " " + r.CameraModel)
			if err := addToAutoAlbum(db, AlbumCamera, cameraKey, cameraName, r.ID); err != nil {
				return err
			}
		}

		// Location album (by city)
		if r.City != "" {
			locationKey := r.City + "/" + r.Country
			locationName := r.City
			if r.Country != "" {
				locationName = r.City + ", " + r.Country
			}
			if err := addToAutoAlbum(db, AlbumLocation, locationKey, locationName, r.ID); err != nil {
				return err
			}
		}
	}

	return nil
}

func addToAutoAlbum(db *sql.DB, albumType int, key, name string, mediaID int64) error {
	albumID, err := UpsertAutoAlbum(db, albumType, key, name)
	if err != nil {
		return fmt.Errorf("auto-album %q: %w", key, err)
	}
	if err := AddMediaToAlbum(db, albumID, mediaID); err != nil {
		return fmt.Errorf("add media %d to auto-album %q: %w", mediaID, key, err)
	}
	return nil
}

// preGenerateThumbnails generates 256px thumbnails for all media that don't have one cached yet.
func (p *Plugin) preGenerateThumbnails(ctx context.Context, db *sql.DB, username string) error {
	rows, err := db.Query(`SELECT ` + mediaCols + ` FROM media m WHERE m.width > 0`)
	if err != nil {
		return err
	}
	defer rows.Close()

	items, err := scanMediaRows(rows)
	if err != nil {
		return err
	}

	var generated, failed int
	for i := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m := &items[i]
		if m.IsVideo() && !FFmpegAvailable() {
			continue
		}
		f, err := p.PreviewPath(username, m, 256)
		if err != nil {
			// Formats with no previewer fail here every scan; one summary
			// line below says how many, this says which.
			p.log.Debug("thumbnail", "path", m.FilePath, "err", err)
			failed++
			continue
		}
		f.Close()
		generated++
		if generated%100 == 0 {
			p.log.Info("thumbnails", "user", username, "generated", generated)
		}
	}
	if generated > 0 || failed > 0 {
		p.log.Info("thumbnails done", "user", username, "generated", generated, "failed", failed)
	}
	return nil
}

// computeQualityScores calculates Laplacian sharpness scores for unscored images.
func (p *Plugin) computeQualityScores(ctx context.Context, db *sql.DB, username string) error {
	rows, err := db.Query(`SELECT ` + mediaCols + ` FROM media m
		WHERE m.quality_score = -1 AND m.mime_type LIKE 'image/%' AND m.width > 0
		LIMIT 100`)
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
		f, err := p.PreviewPath(username, m, 256)
		if err != nil {
			p.log.Debug("quality score: thumbnail", "path", m.FilePath, "err", err)
			continue
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			p.log.Warn("quality score: decode thumbnail", "path", m.FilePath, "err", err)
			continue
		}
		score := laplacianVariance(img)
		if err := UpdateQualityScore(db, m.ID, score); err != nil {
			return fmt.Errorf("store quality score for %s: %w", m.FilePath, err)
		}
	}
	return nil
}

// PreviewPath returns the path for a media thumbnail, using the files plugin for images
// or the video thumbnail cache for videos.
func (p *Plugin) PreviewPath(username string, m *Media, size int) (cogplugin.StoredFile, error) {
	if m.IsVideo() {
		return p.videoThumbnail(username, m, size)
	}
	fp, err := p.filesPlugin()
	if err != nil {
		return nil, err
	}
	return fp.Preview(username, m.FilePath, size)
}

// errNoFiles reports that the files plugin, which holds the media, is not
// registered.
var errNoFiles = errors.New("gallery: files plugin unavailable")

// filesPlugin returns the files plugin, or errNoFiles when it is not
// registered.
func (p *Plugin) filesPlugin() (*files.Plugin, error) {
	if p.files == nil {
		return nil, errNoFiles
	}
	return p.files, nil
}

// videoThumbnail returns a cached video thumbnail, generating it if needed.
func (p *Plugin) videoThumbnail(username string, m *Media, size int) (cogplugin.StoredFile, error) {
	cache, err := p.ctx.UserCache(username)
	if err != nil {
		return nil, err
	}
	size = cogplugin.ThumbnailSize(size)
	name := fmt.Sprintf("video_thumbs/%s_%d.jpg", path.Base(m.FileETag), size)
	if f, err := cache.Open(name); err == nil {
		return f, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	if !FFmpegAvailable() {
		return nil, fmt.Errorf("ffmpeg not available")
	}

	srcPath, err := p.resolveLocalPath(username, m.FilePath)
	if err != nil {
		return nil, err
	}
	cachePath, err := cache.LocalPath(name)
	if err != nil {
		return nil, err
	}
	if err := ffmpegThumbnail(srcPath, cachePath, size); err != nil {
		return nil, err
	}

	return cache.Open(name)
}
