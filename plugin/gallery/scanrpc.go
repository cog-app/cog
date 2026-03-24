package gallery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/rpc"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"
)

// --- RPC types ---

// ScanUserArgs holds arguments for the ScanUser RPC.
type ScanUserArgs struct {
	Username string
}

// ScanAllArgs is a placeholder for the ScanAllUsers RPC.
type ScanAllArgs struct{}

// ScanResult holds the result of a scan operation.
type ScanResult struct {
	Warnings []string
	Error    string
}

// ScanStatusArgs is a placeholder for the GetStatus RPC.
type ScanStatusArgs struct{}

// ScanStatusResult holds the current scan state.
type ScanStatusResult struct {
	Running   bool
	LastRun   time.Time
	LastError string
	Warnings  []string
}

// --- RPC server (runs in subprocess) ---

// ScanRPC implements the RPC service for the scan worker subprocess.
type ScanRPC struct {
	plugin *Plugin
	ctx    context.Context
	mu     sync.Mutex
	status ScanStatusResult
}

func (s *ScanRPC) ScanUser(args *ScanUserArgs, result *ScanResult) error {
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		result.Error = "scan already running"
		return nil
	}
	s.mu.Unlock()
	s.startScan()

	go s.runScan(func() ([]string, error) {
		return s.plugin.scanUser(s.ctx, args.Username)
	})
	return nil
}

func (s *ScanRPC) ScanAllUsers(args *ScanAllArgs, result *ScanResult) error {
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		result.Error = "scan already running"
		return nil
	}
	s.mu.Unlock()
	s.startScan()

	go s.runScan(func() ([]string, error) {
		return s.plugin.scanAllUsersErr(s.ctx)
	})
	return nil
}

func (s *ScanRPC) runScan(fn func() ([]string, error)) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("scan panicked", "panic", r)
			s.finishScan(nil, fmt.Errorf("panic: %v", r))
		}
	}()
	warnings, err := fn()
	s.finishScan(warnings, err)
}

func (s *ScanRPC) finishScan(warnings []string, err error) {
	s.mu.Lock()
	s.status.Running = false
	s.status.LastRun = time.Now()
	s.status.Warnings = warnings
	if err != nil {
		s.status.LastError = err.Error()
	} else {
		s.status.LastError = ""
	}
	s.mu.Unlock()

	// Persist to DB so status survives restarts.
	s.persistStatus()
}

func (s *ScanRPC) startScan() {
	s.mu.Lock()
	s.status.Running = true
	s.mu.Unlock()
	s.persistStatus()
}

func (s *ScanRPC) persistStatus() {
	db, err := s.plugin.SystemDB()
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	SettingScanRunning.Set(db, s.status.Running)
	SettingScanLastRun.Set(db, int(s.status.LastRun.Unix()))
	SettingScanLastError.Set(db, s.status.LastError)
}

func (s *ScanRPC) GetStatus(args *ScanStatusArgs, result *ScanStatusResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	*result = s.status
	return nil
}

// ReleaseMemory frees ONNX sessions and triggers GC. Called by the parent's
// memory watchdog when the worker's RSS exceeds the configured limit.
func (s *ScanRPC) ReleaseMemory(_ *struct{}, _ *struct{}) error {
	s.plugin.activeBackendsMu.Lock()
	backends := s.plugin.activeBackends
	s.plugin.activeBackendsMu.Unlock()
	for _, b := range backends {
		if rc, ok := b.(interface{ Release() }); ok {
			slog.Info("releasing ONNX sessions due to memory pressure")
			rc.Release()
		}
	}
	runtime.GC()
	return nil
}

// ProfileArgs specifies which pprof profile to retrieve.
type ProfileArgs struct {
	Name    string // "heap", "goroutine", "allocs", "block", "mutex", "threadcreate"
	Debug   int
	Seconds int // for "profile" (CPU), 0 = use default 30s
}

// ProfileResult holds the raw pprof output.
type ProfileResult struct {
	Data []byte
}

func (s *ScanRPC) Profile(args *ProfileArgs, result *ProfileResult) error {
	var buf bytes.Buffer

	if args.Name == "profile" {
		// CPU profile: collect for N seconds.
		dur := time.Duration(args.Seconds) * time.Second
		if dur <= 0 {
			dur = 30 * time.Second
		}
		if err := pprof.StartCPUProfile(&buf); err != nil {
			return fmt.Errorf("start cpu profile: %w", err)
		}
		time.Sleep(dur)
		pprof.StopCPUProfile()
		result.Data = buf.Bytes()
		return nil
	}

	p := pprof.Lookup(args.Name)
	if p == nil {
		return fmt.Errorf("unknown profile: %s", args.Name)
	}
	if err := p.WriteTo(&buf, args.Debug); err != nil {
		return err
	}
	result.Data = buf.Bytes()
	return nil
}

// MemStatsResult holds runtime memory statistics from the worker.
type MemStatsResult struct {
	HeapInuse  uint64
	HeapSys    uint64
	HeapAlloc  uint64
	Sys        uint64
	NumGC      uint32
	Goroutines int
}

func (s *ScanRPC) MemStats(_ *struct{}, result *MemStatsResult) error {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	result.HeapInuse = m.HeapInuse
	result.HeapSys = m.HeapSys
	result.HeapAlloc = m.HeapAlloc
	result.Sys = m.Sys
	result.NumGC = m.NumGC
	result.Goroutines = runtime.NumGoroutine()
	return nil
}

// runScanWorker starts the RPC server on stdin/stdout.
// Called when the subprocess is launched via `cog gallery scan worker`.
func (p *Plugin) runScanWorker(ctx context.Context) error {
	p.isWorker = true

	// Clear stale "running" flag from a previous crashed worker.
	if db, err := p.SystemDB(); err == nil {
		SettingScanRunning.Set(db, false)
	}

	svc := &ScanRPC{plugin: p, ctx: ctx}
	server := rpc.NewServer()
	if err := server.Register(svc); err != nil {
		return err
	}
	// Serve RPC over stdin (reads) + stdout (writes).
	server.ServeConn(readWriteCloser{os.Stdin, os.Stdout})
	return nil
}

// --- RPC client (runs in server process) ---

// ScanClient wraps an *rpc.Client for type-safe scan operations.
type ScanClient struct {
	mu     sync.Mutex
	client *rpc.Client
}

func newScanClient(rwc io.ReadWriteCloser) *ScanClient {
	return &ScanClient{client: rpc.NewClient(rwc)}
}

func (c *ScanClient) ScanUser(username string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result ScanResult
	return c.client.Call("ScanRPC.ScanUser", &ScanUserArgs{Username: username}, &result)
}

func (c *ScanClient) ScanAllUsers() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result ScanResult
	return c.client.Call("ScanRPC.ScanAllUsers", &ScanAllArgs{}, &result)
}

func (c *ScanClient) GetStatus() (ScanStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result ScanStatusResult
	if err := c.client.Call("ScanRPC.GetStatus", &ScanStatusArgs{}, &result); err != nil {
		return ScanStatus{}, err
	}
	var s ScanStatus
	s.Running = result.Running
	s.LastRun = result.LastRun
	s.Warnings = result.Warnings
	if result.LastError != "" {
		s.LastError = fmt.Errorf("%s", result.LastError)
	}
	return s, nil
}

func (c *ScanClient) Profile(name string, debug, seconds int) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result ProfileResult
	if err := c.client.Call("ScanRPC.Profile", &ProfileArgs{Name: name, Debug: debug, Seconds: seconds}, &result); err != nil {
		return nil, err
	}
	return result.Data, nil
}

func (c *ScanClient) ReleaseMemory() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client.Call("ScanRPC.ReleaseMemory", &struct{}{}, &struct{}{})
}

func (c *ScanClient) MemStats() (*MemStatsResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result MemStatsResult
	if err := c.client.Call("ScanRPC.MemStats", &struct{}{}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *ScanClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client.Close()
}

// --- Helpers ---

// readWriteCloser combines a reader and writer into a ReadWriteCloser.
type readWriteCloser struct {
	io.Reader
	io.Writer
}

func (readWriteCloser) Close() error { return nil }
