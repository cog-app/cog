package gallery

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/imaging"
	"git.duckfam.us/jj-software/cog/internal/imgconv"
	"git.duckfam.us/jj-software/cog/internal/safeimage"
)

// workSize is the long side of the copy of a photo the analysis stages look
// at. Tag models take 224–448 px and SCRFD 640, a perceptual hash 32, and
// blur shows at this size where it does not in a 256 px thumbnail.
const workSize = 1024

// analysisBudget is how long one user's photos are analysed before the scan
// moves on, so one large library does not hold up everyone else's and the
// cheap whole-library stages run regularly; the scan comes back for the rest.
const analysisBudget = 10 * time.Minute

// Sentinels for a stage that failed on a photo, so the photo is not picked
// up again every scan.
const (
	qualityFailed     = -2
	fingerprintFailed = "-"
	ocrDone           = " " // also "OCR ran and found nothing", and "OCR skipped"
)

// decodedPhoto is a photo decoded once for every stage that looks at it.
type decodedPhoto struct {
	path string      // the local file, for stages that run a program on it
	work *image.RGBA // upright, at most workSize on its long side

	fullOnce sync.Once
	decoded  image.Image
	orient   int
	full     image.Image
}

// Full returns the photo upright at full size, turned on first use: only
// face alignment needs it, and only for a photo with faces.
func (d *decodedPhoto) Full() image.Image {
	d.fullOnce.Do(func() { d.full = imaging.Orient(d.decoded, d.orient) })
	return d.full
}

// decodePhoto decodes the photo at path.
func decodePhoto(path string) (*decodedPhoto, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	img, _, orientation, err := imaging.Decode(data)
	if err != nil && !errors.Is(err, safeimage.ErrTooLarge) {
		var convErr error
		if img, orientation, convErr = imgconv.Decode(context.Background(), path); convErr == nil {
			err = nil
		} else if !errors.Is(convErr, imgconv.ErrUnsupported) {
			err = errors.Join(err, convErr)
		}
	}
	if err != nil {
		return nil, err
	}
	return newDecodedPhoto(path, img, orientation), nil
}

func newDecodedPhoto(path string, img image.Image, orientation int) *decodedPhoto {
	b := img.Bounds()
	ow, oh := imaging.OrientedSize(b.Dx(), b.Dy(), orientation)
	w, h := imaging.Fit(ow, oh, workSize)
	w, h = imaging.OrientedSize(w, h, orientation)
	work := imaging.ToRGBA(imaging.Orient(imaging.Resize(img, w, h), orientation))
	return &decodedPhoto{path: path, work: work, decoded: img, orient: orientation}
}

// imageAnalyzer is a recognition backend that can work from a decoded photo
// rather than decoding the file again.
type imageAnalyzer interface {
	AnalyzeImage(ctx context.Context, photo *decodedPhoto) (*RecognitionResult, error)
}

// recognizer is the recognition configuration for one scan.
type recognizer struct {
	tag, face   RecognitionBackend
	tagMinConf  float64
	faceMinConf float64
}

func (r *recognizer) analyze(ctx context.Context, b RecognitionBackend, photo *decodedPhoto, path string) (*RecognitionResult, error) {
	if ia, ok := b.(imageAnalyzer); ok && photo != nil {
		return ia.AnalyzeImage(ctx, photo)
	}
	return b.Analyze(ctx, path)
}

// run recognises a photo. A backend that fails still leaves what the other
// found.
func (r *recognizer) run(ctx context.Context, photo *decodedPhoto, path string) (tags []RecognizedTag, faces []DetectedFace, err error) {
	if r.tag != nil && r.tag == r.face {
		res, err := r.analyze(ctx, r.tag, photo, path)
		if err != nil {
			return nil, nil, err
		}
		return res.Tags, res.Faces, nil
	}
	var errs []error
	if r.tag != nil {
		if res, err := r.analyze(ctx, r.tag, photo, path); err != nil {
			errs = append(errs, err)
		} else {
			tags = res.Tags
		}
	}
	if r.face != nil {
		if res, err := r.analyze(ctx, r.face, photo, path); err != nil {
			errs = append(errs, err)
		} else {
			faces = res.Faces
		}
	}
	return tags, faces, errors.Join(errs...)
}

// analysisConfig says which per-photo stages run in this scan.
type analysisConfig struct {
	quality     bool
	hasher      binaryHasher // nil: no fingerprints
	algorithm   DuplicateAlgorithm
	ocr         bool
	recognition *recognizer // nil: no recognition
}

// pendingClause is the WHERE condition for photos with work left to do.
func (c *analysisConfig) pendingClause() string {
	conds := []string{"m.content_key = ''"}
	if c.quality {
		conds = append(conds, "m.quality_score = -1")
	}
	if c.hasher != nil {
		conds = append(conds, "m.fingerprint = ''")
	}
	if c.ocr {
		conds = append(conds, "m.ocr_text = ''")
	}
	if c.recognition != nil {
		conds = append(conds, "m.recognition_state = 0")
	}
	return "m.mime_type LIKE 'image/%' AND m.width > 0 AND m.shared_from = '' AND (" + strings.Join(conds, " OR ") + ")"
}

// stageStat is what one stage did in a scan.
type stageStat struct {
	done, failed int
	took         time.Duration
}

// scanReport counts what each stage of a user's scan did, for the line the
// scan logs at the end.
type scanReport struct {
	stages map[string]*stageStat
	order  []string
}

func (r *scanReport) stage(name string) *stageStat {
	if r.stages == nil {
		r.stages = map[string]*stageStat{}
	}
	s := r.stages[name]
	if s == nil {
		s = &stageStat{}
		r.stages[name] = s
		r.order = append(r.order, name)
	}
	return s
}

// measure runs fn as stage name, counting its time.
func (r *scanReport) measure(name string, fn func()) {
	_ = r.time(name, func() error { fn(); return nil })
}

// time runs fn as stage name, counting its time and whether it failed.
func (r *scanReport) time(name string, fn func() error) error {
	start := time.Now()
	err := fn()
	s := r.stage(name)
	s.took += time.Since(start)
	if err != nil {
		s.failed++
	} else {
		s.done++
	}
	return err
}

// attrs renders the report as log attributes: "stage", "done/failed time".
func (r *scanReport) attrs() []any {
	var out []any
	for _, name := range r.order {
		s := r.stages[name]
		out = append(out, name, fmt.Sprintf("%d/%d %s", s.done, s.failed, s.took.Round(time.Millisecond)))
	}
	return out
}

// analysisConfig reads which per-photo stages are on. The release function
// frees recognition models and must be called when the scan is done.
func (p *Plugin) analysisConfig(sysDB, db *sql.DB) (*analysisConfig, func(), error) {
	cfg, err := p.stageConfig(sysDB, db)
	if err != nil || cfg.recognition == nil {
		return cfg, func() {}, err
	}
	rec, release, err := p.recognizer(sysDB)
	if err != nil {
		return nil, release, err
	}
	cfg.recognition = rec
	return cfg, release, nil
}

// stageConfig is analysisConfig without the recognition models: enough to
// say what a photo needs and to store what was found for it. Its recognizer
// has the thresholds and no backends.
func (p *Plugin) stageConfig(sysDB, db *sql.DB) (*analysisConfig, error) {
	cfg := &analysisConfig{quality: true, ocr: TesseractAvailable()}
	if sysDB == nil {
		return cfg, nil
	}
	cfg.quality = SettingQualityScoringEnabled.MustGet(sysDB)
	cfg.ocr = cfg.ocr && SettingOCREnabled.MustGet(sysDB)

	if SettingDuplicateDetectionEnabled.MustGet(sysDB) {
		algorithm := SettingDuplicateAlgorithm.MustGet(sysDB)
		hasher, err := getHasher(algorithm)
		if err != nil {
			return nil, err
		}
		// Fingerprints from another algorithm, or another input, do not
		// compare: they are cleared and computed again.
		version := fingerprintVersion(algorithm)
		if last := SettingDuplicateAlgorithmUsed.MustGet(db); last != version {
			if last != "" {
				p.log.Info("fingerprints changed, clearing", "old", last, "new", version)
			}
			if err := ClearAllFingerprints(db); err != nil {
				return nil, fmt.Errorf("clear fingerprints: %w", err)
			}
			if err := SettingDuplicateAlgorithmUsed.Set(db, version); err != nil {
				return nil, fmt.Errorf("record fingerprint version: %w", err)
			}
		}
		cfg.hasher = hasher
		cfg.algorithm = algorithm
	}

	if SettingTagBackend.MustGet(sysDB) != BackendNone || SettingFaceBackend.MustGet(sysDB) != BackendNone {
		cfg.recognition = &recognizer{
			tagMinConf:  SettingTagMinConfidence.MustGet(sysDB),
			faceMinConf: SettingFaceMinConfidence.MustGet(sysDB),
		}
	}
	return cfg, nil
}

func fingerprintVersion(algorithm DuplicateAlgorithm) string {
	return string(algorithm) + fingerprintInput
}

// fingerprintInput names what fingerprints are computed from, so a change
// of input clears them like a change of algorithm does.
const fingerprintInput = "@work1024"

// recognizer resolves the configured recognition backends, or returns nil
// when none is configured.
func (p *Plugin) recognizer(sysDB *sql.DB) (*recognizer, func(), error) {
	release := func() {}
	tagType := SettingTagBackend.MustGet(sysDB)
	faceType := SettingFaceBackend.MustGet(sysDB)
	if tagType == BackendNone && faceType == BackendNone {
		return nil, release, nil
	}
	r, backends, err := newRecognizer(tagType, faceType, func(t RecognitionBackendType) (RecognitionBackend, error) {
		return p.resolveBackend(sysDB, t)
	})
	if err != nil {
		return nil, release, err
	}
	r.tagMinConf = SettingTagMinConfidence.MustGet(sysDB)
	r.faceMinConf = SettingFaceMinConfidence.MustGet(sysDB)

	// Registered so the ReleaseMemory RPC can reach them under memory
	// pressure; released after the scan to free what onnxruntime allocated.
	p.activeBackendsMu.Lock()
	p.activeBackends = backends
	p.activeBackendsMu.Unlock()
	release = func() {
		for _, b := range backends {
			if rc, ok := b.(interface{ Release() }); ok {
				rc.Release()
			}
		}
		p.activeBackendsMu.Lock()
		p.activeBackends = nil
		p.activeBackendsMu.Unlock()
	}
	return r, release, nil
}

// newRecognizer resolves the tag and face backends, sharing one when both
// are the same type, and checks that each started. It returns the distinct
// backends for the caller to release.
func newRecognizer(tagType, faceType RecognitionBackendType, resolve func(RecognitionBackendType) (RecognitionBackend, error)) (*recognizer, []RecognitionBackend, error) {
	r := &recognizer{}
	var err error
	if tagType != BackendNone {
		if r.tag, err = resolve(tagType); err != nil {
			return nil, nil, fmt.Errorf("tag backend: %w", err)
		}
	}
	if faceType != BackendNone {
		if faceType == tagType && r.tag != nil {
			r.face = r.tag
		} else if r.face, err = resolve(faceType); err != nil {
			return nil, nil, fmt.Errorf("face backend: %w", err)
		}
	}
	var backends []RecognitionBackend
	for _, b := range []RecognitionBackend{r.tag, r.face} {
		if b == nil || slices.Contains(backends, b) {
			continue
		}
		if ic, ok := b.(interface{ InitErr() error }); ok {
			if err := ic.InitErr(); err != nil {
				return nil, nil, fmt.Errorf("recognition backend init: %w", err)
			}
		}
		backends = append(backends, b)
	}
	return r, backends, nil
}

// analyzePhotos runs the per-photo stages on every photo that needs one,
// newest first, until done or the deadline passes. It reports whether
// photos were left for the next scan.
func (p *Plugin) analyzePhotos(ctx context.Context, db *sql.DB, username string, cfg *analysisConfig, deadline time.Time, report *scanReport) (remaining bool, err error) {
	query := `SELECT ` + mediaCols + ` FROM media m WHERE ` + cfg.pendingClause() + ` AND ` + unleased + `
		ORDER BY COALESCE(m.date_taken, m.date_indexed) DESC, m.id DESC LIMIT 50`
	// A photo the stages could not settle would otherwise come back in
	// every batch.
	seen := map[int64]bool{}
	for {
		rows, err := db.Query(query, p.ctx.Now().Unix())
		if err != nil {
			return false, fmt.Errorf("list photos to analyse: %w", err)
		}
		batch, err := scanMediaRows(rows)
		if err != nil {
			return false, fmt.Errorf("list photos to analyse: %w", err)
		}
		batch = slices.DeleteFunc(batch, func(m Media) bool { return seen[m.ID] })
		if len(batch) == 0 {
			return false, nil
		}
		for i := range batch {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			if time.Now().After(deadline) {
				return true, nil
			}
			seen[batch[i].ID] = true
			token, ok, err := p.claimLease(db, batch[i].ID, localHolder)
			if err != nil {
				return true, err
			}
			if !ok {
				continue
			}
			err = p.analyzePhoto(ctx, db, username, &batch[i], cfg, report)
			if rerr := releaseLease(db, batch[i].ID, token); rerr != nil {
				err = errors.Join(err, rerr)
			}
			if err != nil {
				return true, err
			}
		}
	}
}

// photoResult is what the stages found for one photo, written together.
type photoResult struct {
	contentKey  string
	quality     *float64
	fingerprint string
	ocrText     string
	recognized  bool
	tags        []RecognizedTag
	faces       []DetectedFace
	recogErr    error
	errs        []string
}

func (r *photoResult) fail(stage string, err error) {
	r.errs = append(r.errs, stage+": "+err.Error())
}

// photoNeeds says which stages a photo still needs.
type photoNeeds struct {
	ContentKey  bool `json:"content_key,omitempty"`
	Quality     bool `json:"quality,omitempty"`
	Fingerprint bool `json:"fingerprint,omitempty"`
	OCR         bool `json:"ocr,omitempty"`
	Recognition bool `json:"recognition,omitempty"`
}

func (n photoNeeds) any() bool {
	return n.ContentKey || n.Quality || n.Fingerprint || n.OCR || n.Recognition
}

func (c *analysisConfig) needs(m *Media) photoNeeds {
	return photoNeeds{
		ContentKey:  m.ContentKey == "",
		Quality:     c.quality && m.QualityScore == -1,
		Fingerprint: c.hasher != nil && m.Fingerprint == "",
		OCR:         c.ocr && m.OCRText == "",
		Recognition: c.recognition != nil && m.RecognitionState == 0,
	}
}

// failedResult marks every stage a photo needs as failed, for a photo whose
// file could not be read, so the next scan does not pick it up again.
func failedResult(n photoNeeds, stage string, err error) *photoResult {
	res := &photoResult{}
	res.fail(stage, err)
	if n.Quality {
		q := float64(qualityFailed)
		res.quality = &q
	}
	if n.Fingerprint {
		res.fingerprint = fingerprintFailed
	}
	if n.OCR {
		res.ocrText = ocrDone
	}
	if n.Recognition {
		res.recognized, res.recogErr = true, err
	}
	return res
}

// analyzePhoto decodes one photo once and runs every stage it still needs.
// A stage that fails is recorded on the photo; only a failure to store the
// results is returned.
func (p *Plugin) analyzePhoto(ctx context.Context, db *sql.DB, username string, m *Media, cfg *analysisConfig, report *scanReport) error {
	n := cfg.needs(m)
	path, err := p.resolveLocalPath(username, m.FilePath)
	if err != nil {
		p.log.Warn("analyse photo", "path", m.FilePath, "err", err)
		return p.storePhotoResult(db, m, failedResult(n, "open", err), cfg)
	}
	res, photo := p.preparePhoto(path, m, n, cfg, report)
	p.finishPhoto(ctx, res, photo, path, m, n, cfg, report)
	return p.storePhotoResult(db, m, res, cfg)
}

// preparePhoto runs the stages that need only the decoded photo: the
// content key, sharpness and fingerprint. The decoded photo is returned for
// finishPhoto.
func (p *Plugin) preparePhoto(path string, m *Media, n photoNeeds, cfg *analysisConfig, report *scanReport) (*photoResult, *decodedPhoto) {
	res := &photoResult{}
	if n.ContentKey {
		if err := report.time("content key", func() error {
			var err error
			res.contentKey, err = contentKey(path)
			return err
		}); err != nil {
			res.fail("content key", err)
		}
	}

	var photo *decodedPhoto
	if n.Quality || n.Fingerprint || n.Recognition {
		err := report.time("decode", func() error {
			var err error
			photo, err = decodePhoto(path)
			return err
		})
		if err != nil {
			p.log.Debug("decode photo", "path", m.FilePath, "err", err)
			res.fail("decode", err)
		}
	}

	if n.Quality {
		q := float64(qualityFailed)
		if photo != nil {
			report.measure("quality", func() { q = laplacianVariance(photo.work) })
		}
		res.quality = &q
	}
	if n.Fingerprint {
		res.fingerprint = fingerprintFailed
		if photo != nil {
			report.measure("fingerprint", func() {
				res.fingerprint = hex.EncodeToString([]byte(cfg.hasher.Calculate(photo.work)))
			})
		}
	}
	return res, photo
}

// finishPhoto runs recognition and OCR, the stages that take models or a
// program and so run one photo at a time.
func (p *Plugin) finishPhoto(ctx context.Context, res *photoResult, photo *decodedPhoto, path string, m *Media, n photoNeeds, cfg *analysisConfig, report *scanReport) {
	if n.Recognition {
		res.recognized = true
		res.recogErr = report.time("recognition", func() error {
			var err error
			res.tags, res.faces, err = cfg.recognition.run(ctx, photo, path)
			return err
		})
		if res.recogErr != nil {
			p.log.Warn("recognize", "path", m.FilePath, "err", res.recogErr)
		}
	}
	if n.OCR {
		res.ocrText = ocrDone
		if likelyText(m, res.tags) {
			err := report.time("ocr", func() error {
				text, err := tesseractOCR(path)
				if err == nil && text != "" {
					res.ocrText = text
				}
				return err
			})
			if err != nil {
				p.log.Warn("ocr", "path", m.FilePath, "err", err)
				res.fail("ocr", err)
			}
		} else {
			report.stage("ocr skipped").done++
		}
	}
}

// textTags are words in a tag that suggest a photo has text worth reading.
var textTags = []string{"text", "document", "sign", "menu", "poster", "book", "screenshot", "web site",
	"receipt", "letter", "paper", "whiteboard", "comic", "crossword", "scoreboard", "envelope", "label"}

// likelyText reports whether OCR is worth running on a photo. Tesseract
// takes seconds on a 12-megapixel photo and finds nothing in nearly every
// one a camera took, so it runs on what a camera did not take - screenshots,
// scans, saved images - and on photos whose tags suggest text.
func likelyText(m *Media, tags []RecognizedTag) bool {
	if m.CameraModel == "" || m.MimeType == "image/png" {
		return true
	}
	for _, t := range tags {
		name := strings.ToLower(t.Name)
		for _, w := range textTags {
			if strings.Contains(name, w) {
				return true
			}
		}
	}
	return false
}

// storePhotoResult writes what the stages found for a photo in one
// transaction.
func (p *Plugin) storePhotoResult(db *sql.DB, m *Media, res *photoResult, cfg *analysisConfig) error {
	return inTx(db, func(tx *sql.Tx) error {
		if res.contentKey != "" {
			if _, err := tx.Exec(`UPDATE media SET content_key = ? WHERE id = ?`, res.contentKey, m.ID); err != nil {
				return fmt.Errorf("store content key of %s: %w", m.FilePath, err)
			}
		}
		if res.quality != nil {
			if _, err := tx.Exec(`UPDATE media SET quality_score = ? WHERE id = ?`, *res.quality, m.ID); err != nil {
				return fmt.Errorf("store quality of %s: %w", m.FilePath, err)
			}
		}
		if res.fingerprint != "" {
			if _, err := tx.Exec(`UPDATE media SET fingerprint = ? WHERE id = ?`, res.fingerprint, m.ID); err != nil {
				return fmt.Errorf("store fingerprint of %s: %w", m.FilePath, err)
			}
		}
		if res.ocrText != "" {
			if _, err := tx.Exec(`UPDATE media SET ocr_text = ? WHERE id = ?`, res.ocrText, m.ID); err != nil {
				return fmt.Errorf("store ocr of %s: %w", m.FilePath, err)
			}
		}
		if res.recognized {
			if err := storeRecognitionTx(tx, m.ID, res.tags, res.faces, cfg.recognition.tagMinConf, cfg.recognition.faceMinConf, p.ctx.Now().Unix(), res.recogErr); err != nil {
				return fmt.Errorf("store recognition of %s: %w", m.FilePath, err)
			}
		}
		if _, err := tx.Exec(`UPDATE media SET scan_error = ? WHERE id = ?`, strings.Join(res.errs, "; "), m.ID); err != nil {
			return fmt.Errorf("store scan errors of %s: %w", m.FilePath, err)
		}
		return nil
	})
}

// laplacianVariance is the variance of the Laplacian of an image's
// luminance, a measure of sharpness: higher is sharper.
func laplacianVariance(img *image.RGBA) float64 {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if w < 3 || h < 3 {
		return 0
	}
	lum := make([]float64, w*h)
	for y := range h {
		row := img.Pix[y*img.Stride:]
		for x := range w {
			lum[y*w+x] = (0.299*float64(row[x*4]) + 0.587*float64(row[x*4+1]) + 0.114*float64(row[x*4+2])) / 255
		}
	}
	var sum, sumSq float64
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*w + x
			lap := lum[i-w] + lum[i+w] + lum[i-1] + lum[i+1] - 4*lum[i]
			sum += lap
			sumSq += lap * lap
		}
	}
	n := float64((w - 2) * (h - 2))
	mean := sum / n
	return sumSq/n - mean*mean
}
