package gallery

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/httperr"
)

// workerProtocol changes whenever a worker and a server of different
// versions would misread each other.
const workerProtocol = 1

// workerOnline is how recently a worker must have been heard from to count
// as online; it checks in every workerHeartbeat.
const (
	workerHeartbeat = 15 * time.Second
	workerOnline    = 3 * workerHeartbeat
)

const workerAPI = "/apps/gallery/api/workers"

type workerInfo struct {
	Protocol    int     `json:"protocol"`
	Version     string  `json:"version"`
	Hostname    string  `json:"hostname"`
	CPU         string  `json:"cpu"`
	Threads     int     `json:"threads"`
	OCR         bool    `json:"ocr"`
	ONNX        bool    `json:"onnx"`
	PerPhoto    float64 `json:"per_photo"` // seconds per photo, recently
	LastFailure string  `json:"last_failure,omitempty"`
}

type workerModel struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// workerConfig is what a worker needs to compute what the server would.
type workerConfig struct {
	TagBackend  RecognitionBackendType `json:"tag_backend"`
	FaceBackend RecognitionBackendType `json:"face_backend"`
	TagModel    TagModel               `json:"tag_model"`
	Models      []workerModel          `json:"models"`
	Algorithm   DuplicateAlgorithm     `json:"algorithm"`
}

func (c workerConfig) usesONNX() bool {
	return c.TagBackend == BackendOnnx || c.FaceBackend == BackendOnnx
}

type workerJob struct {
	User    string     `json:"user"`
	MediaID int64      `json:"media_id"`
	Lease   string     `json:"lease"`
	ETag    string     `json:"etag"`
	Path    string     `json:"path"`
	Mime    string     `json:"mime"`
	Camera  string     `json:"camera"`
	Needs   photoNeeds `json:"needs"`
}

type leaseRequest struct {
	Info workerInfo `json:"info"`
	Max  int        `json:"max"`
	Wait float64    `json:"wait"` // seconds to wait for work when there is none
}

type leaseResponse struct {
	Config workerConfig `json:"config"`
	Jobs   []workerJob  `json:"jobs"`
}

type workerResult struct {
	ETag             string             `json:"etag"`
	ContentKey       string             `json:"content_key,omitempty"`
	Quality          *float64           `json:"quality,omitempty"`
	Fingerprint      string             `json:"fingerprint,omitempty"`
	Algorithm        DuplicateAlgorithm `json:"algorithm,omitempty"`
	OCRText          string             `json:"ocr_text,omitempty"`
	Recognized       bool               `json:"recognized,omitempty"`
	Tags             []RecognizedTag    `json:"tags,omitempty"`
	Faces            []DetectedFace     `json:"faces,omitempty"`
	RecognitionError string             `json:"recognition_error,omitempty"`
	Errors           []string           `json:"errors,omitempty"`
}

func newWorkerResult(etag string, alg DuplicateAlgorithm, r *photoResult) workerResult {
	out := workerResult{
		ETag:        etag,
		ContentKey:  r.contentKey,
		Quality:     r.quality,
		Fingerprint: r.fingerprint,
		OCRText:     r.ocrText,
		Recognized:  r.recognized,
		Tags:        r.tags,
		Faces:       r.faces,
		Errors:      r.errs,
	}
	if r.fingerprint != "" {
		out.Algorithm = alg
	}
	if r.recogErr != nil {
		out.RecognitionError = r.recogErr.Error()
	}
	return out
}

func (w *workerResult) photoResult() *photoResult {
	r := &photoResult{
		contentKey:  w.ContentKey,
		quality:     w.Quality,
		fingerprint: w.Fingerprint,
		ocrText:     w.OCRText,
		recognized:  w.Recognized,
		tags:        w.Tags,
		faces:       w.Faces,
		errs:        w.Errors,
	}
	if w.RecognitionError != "" {
		r.recogErr = errors.New(w.RecognitionError)
	}
	return r
}

// worker is a registered remote worker.
type worker struct {
	ID        int64
	Name      string
	CreatedAt time.Time
	LastSeen  time.Time
	Info      workerInfo
	Done      int64
}

func (w *worker) holder() string { return "worker:" + strconv.FormatInt(w.ID, 10) }

func (w *worker) Online(now time.Time) bool { return now.Sub(w.LastSeen) < workerOnline }

// workerHub is the server's state for remote workers.
type workerHub struct {
	mu        sync.Mutex
	modelSums map[string]modelSum
	followUps map[string]*time.Timer
	stopped   bool
}

func (h *workerHub) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = true
	for _, t := range h.followUps {
		t.Stop()
	}
	h.followUps = nil
}

type modelSum struct {
	size  int64
	mtime time.Time
	sum   string
}

func newWorkerToken() (token, hash string) {
	token = "cogw_" + rand.Text()
	return token, hashWorkerToken(token)
}

func hashWorkerToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateWorker registers a worker and returns the token it signs in with,
// which is not kept.
func (p *Plugin) CreateWorker(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("a worker needs a name")
	}
	db, err := p.SystemDB()
	if err != nil {
		return "", err
	}
	token, hash := newWorkerToken()
	if _, err := db.Exec(`INSERT INTO gallery_workers (name, token_hash, created_at) VALUES (?, ?, ?)`,
		name, hash, p.ctx.Now().Unix()); err != nil {
		return "", fmt.Errorf("create worker %q: %w", name, err)
	}
	return token, nil
}

// RegenerateWorkerToken replaces a worker's token; the old one stops working.
func (p *Plugin) RegenerateWorkerToken(id int64) (string, error) {
	db, err := p.SystemDB()
	if err != nil {
		return "", err
	}
	token, hash := newWorkerToken()
	res, err := db.Exec(`UPDATE gallery_workers SET token_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return "", fmt.Errorf("regenerate worker %d token: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", fmt.Errorf("regenerate worker %d token: %w", id, err)
	} else if n == 0 {
		return "", fmt.Errorf("no worker %d", id)
	}
	return token, nil
}

func (p *Plugin) DeleteWorker(id int64) error {
	db, err := p.SystemDB()
	if err != nil {
		return err
	}
	if _, err := db.Exec(`DELETE FROM gallery_workers WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete worker %d: %w", id, err)
	}
	return nil
}

const workerCols = `id, name, created_at, last_seen, info, done`

func scanWorker(row interface{ Scan(...any) error }) (*worker, error) {
	var w worker
	var created, seen int64
	var info string
	if err := row.Scan(&w.ID, &w.Name, &created, &seen, &info, &w.Done); err != nil {
		return nil, err
	}
	w.CreatedAt = time.Unix(created, 0)
	if seen > 0 {
		w.LastSeen = time.Unix(seen, 0)
	}
	if err := json.Unmarshal([]byte(info), &w.Info); err != nil {
		return nil, fmt.Errorf("worker %q info: %w", w.Name, err)
	}
	return &w, nil
}

func (p *Plugin) Workers() ([]worker, error) {
	db, err := p.SystemDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT ` + workerCols + ` FROM gallery_workers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list workers: %w", err)
	}
	defer rows.Close()
	var out []worker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, fmt.Errorf("list workers: %w", err)
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// authWorker answers for the worker whose token a request carries. It is
// sent as "Authorization: Worker <token>" rather than Bearer, which the
// host would try as an app password and count against the address.
func (p *Plugin) authWorker(w http.ResponseWriter, r *http.Request) (*worker, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Worker ")
	if !ok || token == "" {
		http.Error(w, "worker token required", http.StatusUnauthorized)
		return nil, false
	}
	db, err := p.SystemDB()
	if err != nil {
		httperr.Internal(w, r, err)
		return nil, false
	}
	wk, err := scanWorker(db.QueryRow(`SELECT `+workerCols+` FROM gallery_workers WHERE token_hash = ?`, hashWorkerToken(token)))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "unknown worker token", http.StatusUnauthorized)
		return nil, false
	}
	if err != nil {
		httperr.Internal(w, r, err)
		return nil, false
	}
	return wk, true
}

func (p *Plugin) touchWorker(wk *worker, info workerInfo, done int64) error {
	db, err := p.SystemDB()
	if err != nil {
		return err
	}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE gallery_workers SET last_seen = ?, info = ?, done = done + ? WHERE id = ?`,
		p.ctx.Now().Unix(), string(data), done, wk.ID); err != nil {
		return fmt.Errorf("record worker %q: %w", wk.Name, err)
	}
	return nil
}

func (p *Plugin) countWorkerDone(wk *worker) error {
	db, err := p.SystemDB()
	if err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE gallery_workers SET last_seen = ?, done = done + 1 WHERE id = ?`, p.ctx.Now().Unix(), wk.ID); err != nil {
		return fmt.Errorf("record worker %q: %w", wk.Name, err)
	}
	return nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		httperr.Internal(w, r, err)
	}
}

func checkProtocol(w http.ResponseWriter, info workerInfo) bool {
	if info.Protocol != workerProtocol {
		http.Error(w, fmt.Sprintf("worker speaks protocol %d, server %d: run the same version of cog as the server", info.Protocol, workerProtocol), http.StatusConflict)
		return false
	}
	return true
}

func (p *Plugin) workerHeartbeatHandler(w http.ResponseWriter, r *http.Request) {
	wk, ok := p.authWorker(w, r)
	if !ok {
		return
	}
	var info workerInfo
	if !decodeJSON(w, r, 64<<10, &info) || !checkProtocol(w, info) {
		return
	}
	if err := p.touchWorker(wk, info, 0); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// workerLease hands a worker up to the number of photos it asks for,
// waiting up to the time it says when there are none.
func (p *Plugin) workerLease(w http.ResponseWriter, r *http.Request) {
	wk, ok := p.authWorker(w, r)
	if !ok {
		return
	}
	var req leaseRequest
	if !decodeJSON(w, r, 64<<10, &req) || !checkProtocol(w, req.Info) {
		return
	}
	if err := p.touchWorker(wk, req.Info, 0); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	sysDB, err := p.SystemDB()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	cfg, err := p.workerConfig(sysDB)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	limit := min(max(req.Max, 1), 16)
	wait := time.Duration(min(max(req.Wait, 0), 30) * float64(time.Second))
	deadline := time.Now().Add(wait)
	for {
		jobs, err := p.leaseJobs(sysDB, wk, req.Info, cfg, limit)
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		if len(jobs) > 0 || !time.Now().Before(deadline) {
			writeJSON(w, r, leaseResponse{Config: cfg, Jobs: jobs})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(min(3*time.Second, time.Until(deadline))):
		}
	}
}

// workerConfig is the server's analysis configuration as a worker needs
// it. Recognition is left out when a worker could not match the server: a
// backend that calls a service, or a model file the server lacks, which
// the worker would otherwise fetch from upstream and maybe get another of.
func (p *Plugin) workerConfig(sysDB *sql.DB) (workerConfig, error) {
	cfg := workerConfig{
		TagBackend:  SettingTagBackend.MustGet(sysDB),
		FaceBackend: SettingFaceBackend.MustGet(sysDB),
		TagModel:    SettingTagModel.MustGet(sysDB),
	}
	if SettingDuplicateDetectionEnabled.MustGet(sysDB) {
		cfg.Algorithm = SettingDuplicateAlgorithm.MustGet(sysDB)
	}
	local := func(t RecognitionBackendType) bool { return t == BackendNone || t == BackendOnnx || t == BackendPigo }
	if !local(cfg.TagBackend) || !local(cfg.FaceBackend) {
		cfg.TagBackend, cfg.FaceBackend = BackendNone, BackendNone
		return cfg, nil
	}
	if !cfg.usesONNX() {
		return cfg, nil
	}
	dir, err := p.onnxModelsDir(sysDB)
	if err != nil {
		return cfg, err
	}
	for _, name := range onnxModelFiles(cfg.TagModel) {
		m, err := p.modelChecksum(dir, name)
		if errors.Is(err, os.ErrNotExist) {
			p.log.Warn("model missing; workers will not recognise", "model", name)
			cfg.TagBackend, cfg.FaceBackend, cfg.Models = BackendNone, BackendNone, nil
			return cfg, nil
		}
		if err != nil {
			return cfg, err
		}
		cfg.Models = append(cfg.Models, m)
	}
	return cfg, nil
}

// modelChecksum hashes a model file once for each size and time it has.
func (p *Plugin) modelChecksum(dir, name string) (workerModel, error) {
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil {
		return workerModel{}, err
	}
	p.hub.mu.Lock()
	cached, ok := p.hub.modelSums[name]
	p.hub.mu.Unlock()
	if ok && cached.size == info.Size() && cached.mtime.Equal(info.ModTime()) {
		return workerModel{Name: name, Size: info.Size(), SHA256: cached.sum}, nil
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return workerModel{}, err
	}
	p.hub.mu.Lock()
	if p.hub.modelSums == nil {
		p.hub.modelSums = map[string]modelSum{}
	}
	p.hub.modelSums[name] = modelSum{size: info.Size(), mtime: info.ModTime(), sum: sum}
	p.hub.mu.Unlock()
	return workerModel{Name: name, Size: info.Size(), SHA256: sum}, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// leaseJobs claims up to limit photos with work a worker can do, newest
// first, one user after another.
func (p *Plugin) leaseJobs(sysDB *sql.DB, wk *worker, info workerInfo, wcfg workerConfig, limit int) ([]workerJob, error) {
	usernames, err := p.ctx.UsersOf("files")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	var jobs []workerJob
	for _, username := range usernames {
		if len(jobs) >= limit {
			break
		}
		db, err := p.DB(username)
		if err != nil {
			return nil, fmt.Errorf("gallery db of %s: %w", username, err)
		}
		cfg, err := p.stageConfig(sysDB, db)
		if err != nil {
			return nil, fmt.Errorf("analysis config of %s: %w", username, err)
		}
		cfg.ocr = cfg.ocr && info.OCR
		if wcfg.TagBackend == BackendNone && wcfg.FaceBackend == BackendNone || wcfg.usesONNX() && !info.ONNX {
			cfg.recognition = nil
		}
		if cfg.hasher != nil && cfg.algorithm != wcfg.Algorithm {
			cfg.hasher = nil
		}
		rows, err := db.Query(`SELECT `+mediaCols+` FROM media m WHERE `+cfg.pendingClause()+` AND `+unleased+`
			ORDER BY COALESCE(m.date_taken, m.date_indexed) DESC, m.id DESC LIMIT ?`, p.ctx.Now().Unix(), limit-len(jobs))
		if err != nil {
			return nil, fmt.Errorf("list photos of %s to analyse: %w", username, err)
		}
		batch, err := scanMediaRows(rows)
		if err != nil {
			return nil, fmt.Errorf("list photos of %s to analyse: %w", username, err)
		}
		for i := range batch {
			m := &batch[i]
			token, ok, err := p.claimLease(db, m.ID, wk.holder())
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			jobs = append(jobs, workerJob{
				User: username, MediaID: m.ID, Lease: token, ETag: m.FileETag,
				Path: m.FilePath, Mime: m.MimeType, Camera: m.CameraModel, Needs: cfg.needs(m),
			})
		}
	}
	return jobs, nil
}

// leasedMedia is the photo a request's lease is on, or an answer saying why
// there is none.
func (p *Plugin) leasedMedia(w http.ResponseWriter, r *http.Request) (string, *sql.DB, *Media, string, bool) {
	username := r.PathValue("user")
	id, err := strconv.ParseInt(r.PathValue("media"), 10, 64)
	if err != nil {
		http.Error(w, "bad media id", http.StatusBadRequest)
		return "", nil, nil, "", false
	}
	token := r.Header.Get("X-Cog-Lease")
	usernames, err := p.ctx.UsersOf("files")
	if err != nil {
		httperr.Internal(w, r, err)
		return "", nil, nil, "", false
	}
	if !slices.Contains(usernames, username) {
		http.Error(w, "no such lease", http.StatusConflict)
		return "", nil, nil, "", false
	}
	db, err := p.DB(username)
	if err != nil {
		httperr.Internal(w, r, err)
		return "", nil, nil, "", false
	}
	held, err := holdsLease(db, id, token)
	if err != nil {
		httperr.Internal(w, r, err)
		return "", nil, nil, "", false
	}
	if !held {
		http.Error(w, "the lease has gone to someone else", http.StatusConflict)
		return "", nil, nil, "", false
	}
	m, err := GetMedia(db, id)
	if err != nil {
		httperr.Internal(w, r, err)
		return "", nil, nil, "", false
	}
	return username, db, m, token, true
}

// workerFile sends a worker the photo it leased. One the server cannot open
// is marked failed here, as the scan would have, rather than sent back to
// the queue for every worker to fail on.
func (p *Plugin) workerFile(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.authWorker(w, r); !ok {
		return
	}
	username, db, m, token, ok := p.leasedMedia(w, r)
	if !ok {
		return
	}
	f, err := p.openLocal(username, m.FilePath)
	if err != nil {
		p.log.Warn("analyse photo", "path", m.FilePath, "err", err)
		cfg, cerr := p.stageConfigFor(db)
		if cerr == nil {
			cerr = p.storePhotoResult(db, m, failedResult(cfg.needs(m), "open", err), cfg)
		}
		if cerr == nil {
			cerr = releaseLease(db, m.ID, token)
		}
		if cerr != nil {
			httperr.Internal(w, r, cerr)
			return
		}
		http.Error(w, "the photo could not be read", http.StatusGone)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	http.ServeContent(w, r, filepath.Base(m.FilePath), info.ModTime(), f)
}

func (p *Plugin) openLocal(username, filePath string) (*os.File, error) {
	path, err := p.resolveLocalPath(username, filePath)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (p *Plugin) stageConfigFor(db *sql.DB) (*analysisConfig, error) {
	sysDB, err := p.SystemDB()
	if err != nil {
		return nil, err
	}
	return p.stageConfig(sysDB, db)
}

// workerResultHandler stores what a worker found for a photo it leased. A
// result for a file that has changed since, or from settings that have,
// keeps only what still holds.
func (p *Plugin) workerResultHandler(w http.ResponseWriter, r *http.Request) {
	wk, ok := p.authWorker(w, r)
	if !ok {
		return
	}
	var res workerResult
	if !decodeJSON(w, r, 16<<20, &res) {
		return
	}
	username, db, m, token, ok := p.leasedMedia(w, r)
	if !ok {
		return
	}
	if res.ETag != m.FileETag {
		if err := releaseLease(db, m.ID, token); err != nil {
			httperr.Internal(w, r, err)
			return
		}
		http.Error(w, "the file changed while it was being analysed", http.StatusConflict)
		return
	}
	cfg, err := p.stageConfigFor(db)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	pr := res.photoResult()
	if pr.recognized && cfg.recognition == nil {
		pr.recognized, pr.tags, pr.faces, pr.recogErr = false, nil, nil, nil
	}
	if pr.fingerprint != "" && pr.fingerprint != fingerprintFailed && res.Algorithm != cfg.algorithm {
		pr.fingerprint = ""
	}
	if err := p.storePhotoResult(db, m, pr, cfg); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if err := errors.Join(releaseLease(db, m.ID, token), p.countWorkerDone(wk)); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if pr.recognized {
		p.followUpScan(username)
	}
	w.WriteHeader(http.StatusNoContent)
}

// followUpScan scans a user a minute after remote results stop arriving,
// and every minute while they do, so new faces are clustered into people:
// the scan finds nothing to analyse while workers hold the photos.
func (p *Plugin) followUpScan(username string) {
	p.hub.mu.Lock()
	defer p.hub.mu.Unlock()
	if p.hub.followUps == nil {
		p.hub.followUps = map[string]*time.Timer{}
	}
	if _, pending := p.hub.followUps[username]; pending {
		return
	}
	if p.hub.stopped {
		return
	}
	p.hub.followUps[username] = time.AfterFunc(time.Minute, func() {
		p.hub.mu.Lock()
		delete(p.hub.followUps, username)
		p.hub.mu.Unlock()
		p.requestScan(username)
	})
}

// workerModelFile sends a worker one of the model files the server's
// configuration uses.
func (p *Plugin) workerModelFile(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.authWorker(w, r); !ok {
		return
	}
	sysDB, err := p.SystemDB()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	name := r.PathValue("name")
	if !slices.Contains(onnxModelFiles(SettingTagModel.MustGet(sysDB)), name) {
		http.NotFound(w, r)
		return
	}
	dir, err := p.onnxModelsDir(sysDB)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	http.ServeFile(w, r, filepath.Join(dir, name))
}
