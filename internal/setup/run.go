package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"git.duckfam.us/jj-software/cog/internal/service"
)

// Run carries out p, in a process with administrator rights when p needs
// them and this one has none, copying what it does to out as it goes. gui
// says whether to ask for those rights with a dialog rather than at the
// terminal.
func Run(ctx context.Context, p Plan, out io.Writer, gui bool) (Result, error) {
	if p.Scope != service.System || ops.IsAdmin() {
		return Apply(ctx, p, out)
	}
	dir, err := os.MkdirTemp("", "cog-setup-*")
	if err != nil {
		return Result{}, fmt.Errorf("setup files: %w", err)
	}
	defer os.RemoveAll(dir)
	planFile := filepath.Join(dir, "plan.json")
	logFile := filepath.Join(dir, "log.txt")
	resultFile := filepath.Join(dir, "result.json")
	data, err := json.Marshal(p)
	if err != nil {
		return Result{}, fmt.Errorf("encode plan: %w", err)
	}
	// The plan holds the admin's password: readable only by this user
	// and the administrator the elevated process runs as, and removed
	// by that process as soon as it has read it.
	if err := os.WriteFile(planFile, data, 0o600); err != nil {
		return Result{}, fmt.Errorf("write plan: %w", err)
	}
	if err := os.WriteFile(logFile, nil, 0o600); err != nil {
		return Result{}, fmt.Errorf("write log: %w", err)
	}

	stop := make(chan struct{})
	tailed := make(chan error, 1)
	go func() { tailed <- tail(logFile, out, stop) }()
	runErr := elevate(p.Source, []string{"setup", "apply", "--plan", planFile, "--log", logFile, "--result", resultFile}, gui)
	close(stop)
	if err := <-tailed; err != nil {
		return Result{}, errors.Join(runErr, err)
	}

	var r appliedResult
	data, err = os.ReadFile(resultFile)
	if err != nil {
		if runErr != nil {
			return Result{}, runErr
		}
		return Result{}, fmt.Errorf("setup as administrator left no result: %w", err)
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{}, fmt.Errorf("setup result: %w", err)
	}
	if r.Error != "" {
		return r.Result, errors.New(r.Error)
	}
	return r.Result, runErr
}

// appliedResult is what the elevated process reports.
type appliedResult struct {
	Result
	Error string `json:"error,omitempty"`
}

// tail copies what is appended to path to out until stop closes, and then
// whatever is left.
func tail(path string, out io.Writer, stop <-chan struct{}) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read setup log: %w", err)
	}
	defer f.Close()
	for {
		if _, err := io.Copy(out, f); err != nil {
			return fmt.Errorf("read setup log: %w", err)
		}
		select {
		case <-stop:
			if _, err := io.Copy(out, f); err != nil {
				return fmt.Errorf("read setup log: %w", err)
			}
			return nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ApplyFiles is the elevated side of Run: it reads the plan from planFile
// (and removes it), applies it writing to logFile, and reports to
// resultFile.
func ApplyFiles(ctx context.Context, planFile, logFile, resultFile string) error {
	data, err := os.ReadFile(planFile)
	if err != nil {
		return fmt.Errorf("read plan: %w", err)
	}
	if err := os.Remove(planFile); err != nil {
		return fmt.Errorf("remove plan: %w", err)
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("parse plan: %w", err)
	}
	log, err := os.OpenFile(logFile, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	res, applyErr := Apply(ctx, p, log)
	r := appliedResult{Result: res}
	if applyErr != nil {
		r.Error = applyErr.Error()
	}
	out, err := json.Marshal(r)
	if err != nil {
		return errors.Join(fmt.Errorf("encode result: %w", err), log.Close())
	}
	if err := os.WriteFile(resultFile, out, 0o644); err != nil {
		return errors.Join(fmt.Errorf("write result: %w", err), log.Close())
	}
	return log.Close()
}
