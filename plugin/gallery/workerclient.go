package gallery

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
)

var anyAddress = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}

// workerBatch is how many photos a worker leases at once. It leases the
// next batch while it still has one queued, so its engines never wait for
// a round trip.
const workerBatch = 8

// remoteWorker analyses photos for a server: it leases them, downloads each,
// runs the same stages the scan would, and sends back what it found.
type remoteWorker struct {
	p         *Plugin
	server    string
	token     string
	modelsDir string
	threads   int
	http      *http.Client

	info     atomic.Pointer[workerInfo]
	perPhoto atomic.Uint64 // math.Float64bits of seconds per photo, smoothed

	cfg     *workerConfig
	stages  *analysisConfig   // what preparing a photo needs; its recognizer is the first engine's
	engines []*analysisConfig // one per COG_ONNX_DEVICE entry, each with its own models
	release func()
	checked map[string]string // model file → the checksum it was found to have

	observed sync.Mutex
	lastDone time.Time
}

// runRemoteWorker is `cog gallery worker <server>`. The token comes from
// COG_WORKER_TOKEN, so it is not in the process list.
func (p *Plugin) runRemoteWorker(ctx context.Context, server string) error {
	token := os.Getenv("COG_WORKER_TOKEN")
	if token == "" {
		return errors.New("set COG_WORKER_TOKEN to the token from Admin > Plugins > Gallery > Workers")
	}
	threads := runtime.NumCPU()
	if v := os.Getenv("COG_WORKER_THREADS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("COG_WORKER_THREADS=%q: want a positive number", v)
		}
		threads = n
	}
	modelsDir := os.Getenv("COG_WORKER_MODELS")
	if modelsDir == "" {
		global, err := p.ctx.GlobalStorage()
		if err != nil {
			return err
		}
		if modelsDir, err = global.LocalPath("worker_models"); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}
	rw := &remoteWorker{
		p:         p,
		server:    strings.TrimRight(server, "/"),
		token:     token,
		modelsDir: modelsDir,
		threads:   threads,
		// The server is the operator's own, and usually on their network.
		http:    safehttp.ClientWith(10*time.Minute, safehttp.Options{Allow: anyAddress}),
		release: func() {},
		checked: map[string]string{},
	}
	rw.info.Store(&workerInfo{
		Protocol: workerProtocol,
		Version:  buildVersion(),
		Hostname: hostname,
		CPU:      cpuModel(),
		Threads:  threads,
		OCR:      TesseractAvailable(),
		ONNX:     (&OnnxBackend{LibraryPath: onnxLibrary()}).Available(),
	})
	defer func() { rw.release() }()
	p.log.Info("worker starting", "server", rw.server, "threads", threads, "models", modelsDir)
	return rw.run(ctx)
}

func (rw *remoteWorker) currentInfo() workerInfo {
	info := *rw.info.Load()
	info.PerPhoto = float64FromBits(rw.perPhoto.Load())
	return info
}

// run leases and analyses photos until ctx ends. Each pass runs the
// pipeline under one server configuration; a change of configuration
// drains it and starts the next.
func (rw *remoteWorker) run(ctx context.Context) error {
	go rw.heartbeat(ctx)
	var next *leaseResponse
	for ctx.Err() == nil {
		if next == nil {
			resp, err := rw.leaseRetrying(ctx, true)
			if err != nil || resp == nil {
				return err
			}
			next = resp
		}
		if err := rw.configure(ctx, next.Config); err != nil {
			var fatal *fatalError
			if errors.As(err, &fatal) {
				return err
			}
			next = nil
			if !rw.pause(ctx, err, time.Minute) {
				return nil
			}
			continue
		}
		var err error
		if next, err = rw.pipeline(ctx, next.Jobs); err != nil {
			return err
		}
	}
	return nil
}

// leaseRetrying leases photos, waiting for some when wait is set, and tries
// again while the server is unreachable. It returns nil when ctx ends, and
// an error only when the server refused the worker.
func (rw *remoteWorker) leaseRetrying(ctx context.Context, wait bool) (*leaseResponse, error) {
	backoff := time.Second
	for {
		resp, err := rw.lease(ctx, wait)
		if err == nil {
			return resp, nil
		}
		var fatal *fatalError
		if errors.As(err, &fatal) {
			return nil, err
		}
		if !rw.pause(ctx, err, backoff) {
			return nil, nil
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// pause logs a failure and waits before the next try, reporting false when
// ctx ended first.
func (rw *remoteWorker) pause(ctx context.Context, err error, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	rw.p.log.Warn("worker", "err", err, "retry in", d)
	rw.noteFailure(err)
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (rw *remoteWorker) noteFailure(err error) {
	info := *rw.info.Load()
	info.LastFailure = err.Error()
	rw.info.Store(&info)
}

// fatalError is an answer from the server that retrying will not change.
type fatalError struct{ msg string }

func (e *fatalError) Error() string { return e.msg }

func (rw *remoteWorker) heartbeat(ctx context.Context) {
	t := time.NewTicker(workerHeartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := rw.call(ctx, http.MethodPost, workerAPI+"/heartbeat", nil, rw.currentInfo(), nil); err != nil && ctx.Err() == nil {
			rw.p.log.Warn("worker heartbeat", "err", err)
		}
	}
}

// call sends a JSON request to the server and decodes a JSON answer into out.
func (rw *remoteWorker) call(ctx context.Context, method, path string, header http.Header, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, rw.server+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := rw.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	return nil
}

// do sends a request signed with the worker's token and turns an answer
// other than success into an error, fatal when the server refused the
// worker itself.
func (rw *remoteWorker) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Worker "+rw.token)
	resp, err := rw.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	msg, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return nil, err
	}
	err = &httpStatusError{code: resp.StatusCode, msg: fmt.Sprintf("%s %s: %s: %s", req.Method, req.URL.Path, resp.Status, bytes.TrimSpace(msg))}
	if resp.StatusCode == http.StatusUnauthorized || req.URL.Path == workerAPI+"/lease" && resp.StatusCode == http.StatusConflict {
		return nil, &fatalError{msg: err.Error()}
	}
	return nil, err
}

type httpStatusError struct {
	code int
	msg  string
}

func (e *httpStatusError) Error() string { return e.msg }

func statusOf(err error) int {
	var se *httpStatusError
	if errors.As(err, &se) {
		return se.code
	}
	return 0
}

func (rw *remoteWorker) lease(ctx context.Context, wait bool) (*leaseResponse, error) {
	req := leaseRequest{Info: rw.currentInfo(), Max: workerBatch}
	if wait {
		req.Wait = 25
	}
	var resp leaseResponse
	err := rw.call(ctx, http.MethodPost, workerAPI+"/lease", nil, req, &resp)
	return &resp, err
}

// configure loads what the server's configuration needs, when it changed:
// the model files, checked against the server's, and an engine for each
// device.
func (rw *remoteWorker) configure(ctx context.Context, cfg workerConfig) error {
	if rw.cfg != nil && sameConfig(*rw.cfg, cfg) {
		return nil
	}
	rw.release()
	rw.release, rw.stages, rw.engines, rw.cfg = func() {}, nil, nil, nil

	for _, m := range cfg.Models {
		if err := rw.ensureModel(ctx, m); err != nil {
			return err
		}
	}
	stages := &analysisConfig{ocr: TesseractAvailable(), algorithm: cfg.Algorithm}
	if cfg.Algorithm != "" {
		h, err := getHasher(cfg.Algorithm)
		if err != nil {
			return &fatalError{msg: err.Error()}
		}
		stages.hasher = h
	}
	devices := []string{""}
	if cfg.usesONNX() {
		devices = onnxDevices()
	}
	cpuEngines := 0
	for _, d := range devices {
		if d != DeviceCUDA {
			cpuEngines++
		}
	}
	var engines []*analysisConfig
	var backends []RecognitionBackend
	release := func() {
		for _, b := range backends {
			if rc, ok := b.(interface{ Release() }); ok {
				rc.Release()
			}
		}
	}
	for _, device := range devices {
		engine := *stages
		if cfg.TagBackend != BackendNone || cfg.FaceBackend != BackendNone {
			// A GPU engine's threads only run what the GPU cannot; the
			// CPU engines share the rest.
			threads := 2
			if device != DeviceCUDA {
				threads = max(1, rw.threads/cpuEngines)
			}
			rec, bs, err := newRecognizer(cfg.TagBackend, cfg.FaceBackend, rw.backend(cfg, device, threads))
			if err != nil {
				release()
				return &fatalError{msg: err.Error()}
			}
			engine.recognition = rec
			backends = append(backends, bs...)
		}
		engines = append(engines, &engine)
	}
	stages.recognition = engines[0].recognition
	rw.stages, rw.engines, rw.release, rw.cfg = stages, engines, release, &cfg
	rw.p.log.Info("worker configured", "tags", cfg.TagBackend, "faces", cfg.FaceBackend, "tag_model", cfg.TagModel,
		"fingerprints", cfg.Algorithm, "engines", strings.Join(devices, ","))
	return nil
}

func sameConfig(a, b workerConfig) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(ja, jb)
}

func (rw *remoteWorker) backend(cfg workerConfig, device string, threads int) func(RecognitionBackendType) (RecognitionBackend, error) {
	return func(t RecognitionBackendType) (RecognitionBackend, error) {
		switch t {
		case BackendPigo:
			return &PigoBackend{}, nil
		case BackendOnnx:
			b := &OnnxBackend{ModelsDir: rw.modelsDir, LibraryPath: onnxLibrary(), TagModel: cfg.TagModel, Threads: threads, Device: device}
			if !b.Available() {
				return nil, errors.New("onnx backend not available: libonnxruntime.so not found")
			}
			return b, nil
		default:
			return nil, fmt.Errorf("backend %q cannot run on a worker", t)
		}
	}
}

// ensureModel makes sure the models directory holds the server's copy of a
// model file, downloading it from the server when it does not.
func (rw *remoteWorker) ensureModel(ctx context.Context, m workerModel) error {
	path := filepath.Join(rw.modelsDir, m.Name)
	if rw.checked[m.Name] == m.SHA256 {
		return nil
	}
	if info, err := os.Stat(path); err == nil && info.Size() == m.Size {
		sum, err := fileSHA256(path)
		if err != nil {
			return err
		}
		if sum == m.SHA256 {
			rw.checked[m.Name] = sum
			return nil
		}
	}
	rw.p.log.Info("downloading model from server", "model", m.Name, "size", m.Size)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rw.server+workerAPI+"/models/"+m.Name, nil)
	if err != nil {
		return err
	}
	resp, err := rw.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp := path + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	err = errors.Join(err, f.Close())
	if err == nil {
		var sum string
		if sum, err = fileSHA256(tmp); err == nil && sum != m.SHA256 {
			err = fmt.Errorf("model %s: downloaded checksum %s, server says %s", m.Name, sum, m.SHA256)
		}
	}
	if err != nil {
		return errors.Join(err, os.Remove(tmp))
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	rw.checked[m.Name] = m.SHA256
	return nil
}

// prepared is a photo downloaded and through the stages that need only its
// pixels.
type prepared struct {
	job   workerJob
	media *Media
	path  string
	res   *photoResult
	photo *decodedPhoto
	err   error
}

// pipeline analyses jobs and keeps leasing more until the server's
// configuration changes or ctx ends: decoders download and decode photos
// into a queue that every engine takes from, and results go back while the
// next photos are worked on. It returns the lease that brought the new
// configuration, for the next pass.
func (rw *remoteWorker) pipeline(ctx context.Context, jobs []workerJob) (*leaseResponse, error) {
	queue := make(chan workerJob, 2*workerBatch)
	ready := make(chan prepared, len(rw.engines))
	var decoders, engines, sends sync.WaitGroup
	for range rw.decoders() {
		decoders.Go(func() {
			for job := range queue {
				select {
				case ready <- rw.prepare(ctx, job):
				case <-ctx.Done():
					return
				}
			}
		})
	}
	go func() {
		decoders.Wait()
		close(ready)
	}()
	for _, engine := range rw.engines {
		engines.Go(func() {
			for pr := range ready {
				rw.finish(ctx, engine, pr, &sends)
			}
		})
	}

	feed := func(jobs []workerJob) {
		for _, job := range jobs {
			select {
			case queue <- job:
			case <-ctx.Done():
				return
			}
		}
	}
	feed(jobs)
	var next *leaseResponse
	var err error
	for ctx.Err() == nil {
		for len(queue) >= workerBatch && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(100 * time.Millisecond):
			}
		}
		var resp *leaseResponse
		resp, err = rw.leaseRetrying(ctx, len(queue) == 0)
		if err != nil || resp == nil {
			break
		}
		if !sameConfig(*rw.cfg, resp.Config) {
			next = resp
			break
		}
		if len(resp.Jobs) == 0 {
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			continue
		}
		feed(resp.Jobs)
	}
	close(queue)
	engines.Wait()
	sends.Wait()
	return next, err
}

// decoders is how many photos are downloaded and decoded at once: enough
// to keep every engine fed, as decoding a photo takes about as long as a
// GPU takes to recognise one.
func (rw *remoteWorker) decoders() int {
	if v := os.Getenv("COG_WORKER_DECODERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		rw.p.log.Warn("COG_WORKER_DECODERS: want a positive number", "value", v)
	}
	return max(2, len(rw.engines)+1)
}

// finish runs one engine's stages on a prepared photo and sends the result.
func (rw *remoteWorker) finish(ctx context.Context, engine *analysisConfig, pr prepared, sends *sync.WaitGroup) {
	if pr.err != nil {
		if ctx.Err() == nil {
			rw.p.log.Warn("worker download", "path", pr.job.Path, "err", pr.err)
			rw.noteFailure(pr.err)
		}
		return
	}
	rw.p.finishPhoto(ctx, pr.res, pr.photo, pr.path, pr.media, pr.job.Needs, engine, &scanReport{})
	if err := os.Remove(pr.path); err != nil {
		rw.p.log.Warn("worker remove download", "path", pr.path, "err", err)
	}
	rw.observe()
	result := newWorkerResult(pr.job.ETag, rw.cfg.Algorithm, pr.res)
	sends.Go(func() { rw.send(ctx, pr.job, result) })
}

// observe smooths the time between finished photos, whichever engine
// finished them: the worker's seconds per photo.
func (rw *remoteWorker) observe() {
	rw.observed.Lock()
	defer rw.observed.Unlock()
	now := time.Now()
	if !rw.lastDone.IsZero() {
		next := now.Sub(rw.lastDone).Seconds()
		if prev := float64FromBits(rw.perPhoto.Load()); prev > 0 {
			next = 0.95*prev + 0.05*next
		}
		rw.perPhoto.Store(float64Bits(next))
	}
	rw.lastDone = now
}

// prepare downloads a leased photo and runs the stages that need only its
// pixels. A stage the worker cannot run is left for the server.
func (rw *remoteWorker) prepare(ctx context.Context, job workerJob) prepared {
	pr := prepared{job: job}
	if job.Needs.Recognition && rw.stages.recognition == nil {
		pr.job.Needs.Recognition = false
	}
	if job.Needs.Fingerprint && rw.stages.hasher == nil {
		pr.job.Needs.Fingerprint = false
	}
	pr.job.Needs.OCR = job.Needs.OCR && rw.stages.ocr
	pr.media = &Media{ID: job.MediaID, FilePath: job.Path, FileETag: job.ETag, MimeType: job.Mime, CameraModel: job.Camera}
	pr.path, pr.err = rw.download(ctx, job)
	if pr.err != nil {
		return pr
	}
	pr.res, pr.photo = rw.p.preparePhoto(pr.path, pr.media, pr.job.Needs, rw.stages, &scanReport{})
	return pr
}

func (rw *remoteWorker) jobPath(job workerJob, what string) string {
	return fmt.Sprintf("%s/jobs/%s/%d/%s", workerAPI, job.User, job.MediaID, what)
}

func (rw *remoteWorker) download(ctx context.Context, job workerJob) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rw.server+rw.jobPath(job, "file"), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Cog-Lease", job.Lease)
	resp, err := rw.do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp("", "cog-worker-*"+filepath.Ext(job.Path))
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, resp.Body)
	if err = errors.Join(err, f.Close()); err != nil {
		return "", errors.Join(err, os.Remove(f.Name()))
	}
	return f.Name(), nil
}

// send delivers a result, trying again a few times while the server is
// unreachable. A lease that went to someone else is not worth retrying.
func (rw *remoteWorker) send(ctx context.Context, job workerJob, result workerResult) {
	header := http.Header{"X-Cog-Lease": {job.Lease}}
	for attempt := 0; ; attempt++ {
		err := rw.call(ctx, http.MethodPost, rw.jobPath(job, "result"), header, result, nil)
		if err == nil || ctx.Err() != nil {
			return
		}
		if code := statusOf(err); code == http.StatusConflict || attempt == 4 {
			rw.p.log.Warn("worker result dropped", "path", job.Path, "err", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
}

func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 8 {
			return s.Value[:8]
		}
	}
	return info.Main.Version
}

func cpuModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return runtime.GOARCH
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), ":"); ok && strings.TrimSpace(k) == "model name" {
			return strings.TrimSpace(v)
		}
	}
	return runtime.GOARCH
}

func float64Bits(f float64) uint64     { return math.Float64bits(f) }
func float64FromBits(b uint64) float64 { return math.Float64frombits(b) }
