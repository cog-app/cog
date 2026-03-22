package gallery

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	s.status.Running = true
	s.mu.Unlock()

	// Run scan in a goroutine so it doesn't block the RPC connection.
	// net/rpc.ServeConn is single-threaded — if we block here, GetStatus
	// can't be served until the scan finishes.
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
	s.status.Running = true
	s.mu.Unlock()

	go s.runScan(func() ([]string, error) {
		return s.plugin.scanAllUsersErr(s.ctx)
	})
	return nil
}

func (s *ScanRPC) runScan(fn func() ([]string, error)) {
	warnings, err := fn()

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
}

func (s *ScanRPC) GetStatus(args *ScanStatusArgs, result *ScanStatusResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	*result = s.status
	return nil
}

// ProfileArgs specifies which pprof profile to retrieve.
type ProfileArgs struct {
	Name     string // "heap", "goroutine", "allocs", "block", "mutex", "threadcreate"
	Debug    int
	Seconds  int // for "profile" (CPU), 0 = use default 30s
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
