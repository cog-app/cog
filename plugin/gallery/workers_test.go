package gallery_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
)

const workerAPI = "/apps/gallery/api/workers"

// newWorkerEnv has alice's photos scanned and then forgotten by the
// per-photo stages, so they wait for a worker. OCR is off: whether
// tesseract is installed depends on the machine.
func newWorkerEnv(t *testing.T, photos ...string) *scanEnv {
	t.Helper()
	e := newScanEnv(t)
	sysDB, err := e.g.SystemDB()
	require.NoError(t, err)
	require.NoError(t, gallery.SettingOCREnabled.Set(sysDB, false))
	// Pigo is pure Go, so recognition runs on the worker without ONNX.
	require.NoError(t, gallery.SettingFaceBackend.Set(sysDB, gallery.BackendPigo))
	for i, p := range photos {
		e.upload(t, p, photoJPEG(t, uint64(i+1), 64, 64))
	}
	e.scan(t)
	_, err = e.db.Exec(`UPDATE media SET quality_score = -1, fingerprint = '', content_key = '', recognition_state = 0`)
	require.NoError(t, err)
	return e
}

func workerAuth(token string) cogtest.HTTPOpt {
	return cogtest.Header("Authorization", "Worker "+token)
}

type leaseResponse struct {
	Jobs []struct {
		User    string `json:"user"`
		MediaID int64  `json:"media_id"`
		Lease   string `json:"lease"`
		ETag    string `json:"etag"`
	} `json:"jobs"`
}

func lease(t *testing.T, e *scanEnv, token string, max int) leaseResponse {
	t.Helper()
	var resp leaseResponse
	e.PostJSON(t, workerAPI+"/lease", map[string]any{"info": map[string]any{"protocol": 1}, "max": max}, workerAuth(token)).Status(200).JSON(&resp)
	return resp
}

func pendingAnalysis(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM media WHERE quality_score = -1 OR fingerprint = '' OR content_key = '' OR recognition_state = 0`).Scan(&n))
	return n
}

// A worker leases the photos, downloads them, analyses them as the scan
// would, and the server stores what it found.
func TestRemoteWorkerAnalysesPhotos(t *testing.T) {
	e := newWorkerEnv(t, "/Photos/a.jpg", "/Photos/b.jpg")
	require.Equal(t, 2, pendingAnalysis(t, e.db))
	token, err := e.g.CreateWorker("desktop")
	require.NoError(t, err)

	srv := httptest.NewServer(e.Handler)
	defer srv.Close()
	t.Setenv("COG_WORKER_TOKEN", token)
	t.Setenv("COG_WORKER_MODELS", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.g.RunRemoteWorker(ctx, srv.URL) }()

	deadline := time.Now().Add(20 * time.Second)
	for pendingAnalysis(t, e.db) > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	require.NoError(t, <-done)

	require.Equal(t, 0, pendingAnalysis(t, e.db))
	var quality float64
	require.NoError(t, e.db.QueryRow(`SELECT quality_score FROM media WHERE file_path = '/Photos/a.jpg'`).Scan(&quality))
	require.True(t, quality >= 0)
	var leases int
	require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM analysis_leases`).Scan(&leases))
	require.Equal(t, 0, leases)

	workers, err := e.g.Workers()
	require.NoError(t, err)
	require.Equal(t, 1, len(workers))
	require.Equal(t, int64(2), workers[0].Done)
	require.True(t, workers[0].Online(e.Now()))
}

// A photo goes to one holder at a time. A lease that runs out goes to the
// next who asks, and the first holder's result is refused.
func TestWorkerLeaseIsExclusive(t *testing.T) {
	e := newWorkerEnv(t, "/Photos/a.jpg")
	first, err := e.g.CreateWorker("first")
	require.NoError(t, err)
	second, err := e.g.CreateWorker("second")
	require.NoError(t, err)

	got := lease(t, e, first, 4)
	require.Equal(t, 1, len(got.Jobs))
	require.Equal(t, 0, len(lease(t, e, second, 4).Jobs))

	e.Clock.Advance(11 * time.Minute)
	again := lease(t, e, second, 4)
	require.Equal(t, 1, len(again.Jobs))

	job := got.Jobs[0]
	path := workerAPI + "/jobs/alice/" + strconv.FormatInt(job.MediaID, 10)
	e.Get(t, path+"/file", workerAuth(first), cogtest.Header("X-Cog-Lease", job.Lease)).Status(http.StatusConflict)
	e.PostJSON(t, path+"/result", map[string]any{"etag": job.ETag, "quality": 0.5}, workerAuth(first), cogtest.Header("X-Cog-Lease", job.Lease)).Status(http.StatusConflict)

	j2 := again.Jobs[0]
	e.Get(t, path+"/file", workerAuth(second), cogtest.Header("X-Cog-Lease", j2.Lease)).Status(http.StatusOK)
	e.PostJSON(t, path+"/result", map[string]any{"etag": j2.ETag, "quality": 0.5, "content_key": "k"}, workerAuth(second), cogtest.Header("X-Cog-Lease", j2.Lease)).Status(http.StatusNoContent)
	var quality float64
	require.NoError(t, e.db.QueryRow(`SELECT quality_score FROM media WHERE id = ?`, j2.MediaID).Scan(&quality))
	require.Equal(t, 0.5, quality)
}

// A result for a file that changed while it was out is not stored.
func TestWorkerResultForChangedFileIsRefused(t *testing.T) {
	e := newWorkerEnv(t, "/Photos/a.jpg")
	token, err := e.g.CreateWorker("w")
	require.NoError(t, err)
	job := lease(t, e, token, 1).Jobs[0]
	e.PostJSON(t, workerAPI+"/jobs/alice/"+strconv.FormatInt(job.MediaID, 10)+"/result", map[string]any{"etag": "stale", "quality": 0.5},
		workerAuth(token), cogtest.Header("X-Cog-Lease", job.Lease)).Status(http.StatusConflict)
	require.Equal(t, 1, pendingAnalysis(t, e.db))
}

func TestWorkerToken(t *testing.T) {
	e := newWorkerEnv(t)
	e.PostJSON(t, workerAPI+"/lease", map[string]any{}).Status(http.StatusUnauthorized)
	e.PostJSON(t, workerAPI+"/lease", map[string]any{}, workerAuth("cogw_nope")).Status(http.StatusUnauthorized)

	token, err := e.g.CreateWorker("w")
	require.NoError(t, err)
	e.PostJSON(t, workerAPI+"/lease", map[string]any{"info": map[string]any{"protocol": 99}}, workerAuth(token)).Status(http.StatusConflict)
	lease(t, e, token, 1)

	workers, err := e.g.Workers()
	require.NoError(t, err)
	fresh, err := e.g.RegenerateWorkerToken(workers[0].ID)
	require.NoError(t, err)
	e.PostJSON(t, workerAPI+"/lease", map[string]any{}, workerAuth(token)).Status(http.StatusUnauthorized)
	lease(t, e, fresh, 1)

	require.NoError(t, e.g.DeleteWorker(workers[0].ID))
	e.PostJSON(t, workerAPI+"/lease", map[string]any{}, workerAuth(fresh)).Status(http.StatusUnauthorized)
}
