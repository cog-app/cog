package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// ExitRestart is the exit status that asks a service manager to start the
// server again after an update.
const ExitRestart = 75

// checkEvery is how often the server looks for a release.
const checkEvery = 24 * time.Hour

// Checker looks for new releases while the server runs, and installs one
// when an admin asks or, with Auto, when it finds one.
type Checker struct {
	URL      string
	Current  string // this build's version
	Platform string
	Exe      string // the binary to replace
	Keys     []ed25519.PublicKey
	Client   *http.Client
	Log      *slog.Logger
	// Auto installs a release as soon as it is found.
	Auto bool
	// Restart ends the server so that it starts again on the new binary.
	Restart func()

	mu         sync.Mutex
	status     Status
	installing bool
}

// Status is what the checker last found.
type Status struct {
	Current  string
	Latest   *Release // newer than Current, or nil
	Checked  time.Time
	Err      error // of the last check or install
	Replace  error // why the binary cannot be replaced, or nil
	Updating bool  // an install is in progress
}

// Status returns what the checker last found.
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.status
	s.Current = c.Current
	s.Updating = c.installing
	s.Replace = CanReplace(c.Exe)
	return s
}

// Run checks a minute after start, then once a day, until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	if err := Cleanup(c.Exe); err != nil {
		c.Log.Warn("update cleanup", "err", err)
	}
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = checkEvery
		rel, err := c.Check(ctx)
		if err != nil {
			c.Log.Warn("check for updates", "err", err)
			continue
		}
		if rel == nil {
			continue
		}
		c.Log.Info("update available", "current", c.Current, "latest", rel.Version)
		if c.Auto {
			if err := c.Install(ctx); err != nil {
				c.Log.Error("install update", "version", rel.Version, "err", err)
			}
		}
	}
}

// Check fetches the release list now and returns the release newer than
// this build, or nil.
func (c *Checker) Check(ctx context.Context) (*Release, error) {
	m, err := Fetch(ctx, c.Client, c.URL, c.Keys)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Checked = time.Now()
	c.status.Err = err
	if err != nil {
		return nil, err
	}
	c.status.Latest = m.Newer(c.Current, c.Platform)
	return c.status.Latest, nil
}

// errBusy is an install asked for while one runs.
var errBusy = errors.New("an update is already being installed")

// errNone is an install asked for with no newer release known.
var errNone = errors.New("there is no newer release to install")

// Install installs the newest release found by the last check and
// restarts the server on it.
func (c *Checker) Install(ctx context.Context) error {
	c.mu.Lock()
	if c.installing {
		c.mu.Unlock()
		return errBusy
	}
	rel := c.status.Latest
	if rel == nil {
		c.mu.Unlock()
		return errNone
	}
	c.installing = true
	c.mu.Unlock()

	err := Install(ctx, c.Client, rel, c.Platform, c.Exe)
	c.mu.Lock()
	c.installing = false
	c.status.Err = err
	c.mu.Unlock()
	if err != nil {
		return err
	}
	c.Log.Info("installed update, restarting", "version", rel.Version)
	c.Restart()
	return nil
}
