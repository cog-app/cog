package gallery

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// errRenameRunning refuses a rename or undo while one is going: two at once
// race each other for every file and number names neither needed.
var errRenameRunning = errors.New("a rename is already running")

// jobFailuresShown is how many failures a finished job keeps to show.
const jobFailuresShown = 10

// FileJob is a job over many files — a rename, its undo, or deleting
// identical copies — running, or the last one that finished.
type FileJob struct {
	Undo           bool
	Started, Ended time.Time
	Total          int // 0 while the rename is still being planned
	Done, Failed   int
	Failures       []string // the first jobFailuresShown
	Err            string   // why it stopped before or after the files
}

func (j FileJob) Running() bool { return !j.Started.IsZero() && j.Ended.IsZero() }

func (j FileJob) Percent() int {
	if j.Total == 0 {
		return 0
	}
	return 100 * (j.Done + j.Failed) / j.Total
}

// fileJob is a FileJob being updated as it runs. Its methods do nothing on
// nil, so work can run without one.
type fileJob struct {
	mu  sync.Mutex
	job FileJob
}

func (j *fileJob) snapshot() FileJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.job
	s.Failures = append([]string(nil), j.job.Failures...)
	return s
}

func (j *fileJob) begin(total int) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.job.Total = total
}

func (j *fileJob) step() {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.job.Done++
}

func (j *fileJob) fail(err error) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.job.Failed++
	if len(j.job.Failures) < jobFailuresShown {
		j.job.Failures = append(j.job.Failures, err.Error())
	}
}

// jobSet holds each user's one job of a kind: the one running, or the
// last to finish since the server started.
type jobSet struct {
	mu      sync.Mutex
	jobs    map[string]*fileJob
	running error // refuses a second job while one runs
}

func (s *jobSet) status(username string) (FileJob, bool) {
	s.mu.Lock()
	j := s.jobs[username]
	s.mu.Unlock()
	if j == nil {
		return FileJob{}, false
	}
	return j.snapshot(), true
}

// start runs work in the background as the user's job. work's error is kept
// for the page when the files did not already say what went wrong.
func (s *jobSet) start(username string, undo bool, work func(*fileJob) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.jobs[username]; j != nil && j.snapshot().Running() {
		return s.running
	}
	if s.jobs == nil {
		s.jobs = map[string]*fileJob{}
	}
	j := &fileJob{job: FileJob{Undo: undo, Started: time.Now()}}
	s.jobs[username] = j
	go func() {
		err := work(j)
		j.mu.Lock()
		defer j.mu.Unlock()
		if err != nil && j.job.Failed == 0 {
			j.job.Err = err.Error()
		}
		j.job.Ended = time.Now()
	}()
	return nil
}

// RenameStatus is the user's rename running, or the last one since the
// server started.
func (p *Plugin) RenameStatus(username string) (FileJob, bool) {
	return p.renames.status(username)
}

func (p *Plugin) startRenameJob(username string, undo bool, work func(*fileJob) error) error {
	return p.renames.start(username, undo, work)
}

// StartRename plans the rename of the user's photos by pattern and applies
// it in the background.
func (p *Plugin) StartRename(username, pattern string, policy ConflictPolicy) error {
	return p.startRenameJob(username, false, func(j *fileJob) error {
		db, err := p.DB(username)
		if err != nil {
			return err
		}
		filesDB, err := p.ctx.PluginDB(username, plugin.OfPlugin("files"))
		if err != nil {
			return err
		}
		plan, err := PlanRename(db, filesDB, pattern, policy)
		if err != nil {
			return fmt.Errorf("plan: %w", err)
		}
		if len(plan.Renames) == 0 {
			return errors.New("nothing to rename: every file already has the name this pattern gives it")
		}
		return p.ApplyRename(username, plan.Renames, j)
	})
}

// StartUndo undoes the user's last rename in the background.
func (p *Plugin) StartUndo(username string) error {
	return p.startRenameJob(username, true, func(j *fileJob) error {
		return p.UndoLastRename(username, j)
	})
}
