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

// renameFailuresShown is how many failures a finished job keeps to show.
const renameFailuresShown = 10

// RenameJob is a rename or undo running, or the last one that finished.
type RenameJob struct {
	Undo           bool
	Started, Ended time.Time
	Total          int // 0 while the rename is still being planned
	Done, Failed   int
	Failures       []string // the first renameFailuresShown
	Err            string   // why it stopped before or after the files
}

func (j RenameJob) Running() bool { return !j.Started.IsZero() && j.Ended.IsZero() }

func (j RenameJob) Percent() int {
	if j.Total == 0 {
		return 0
	}
	return 100 * (j.Done + j.Failed) / j.Total
}

// renameJob is a RenameJob being updated as it runs.
type renameJob struct {
	mu  sync.Mutex
	job RenameJob
}

func (j *renameJob) snapshot() RenameJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.job
	s.Failures = append([]string(nil), j.job.Failures...)
	return s
}

func (j *renameJob) begin(total int) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.job.Total = total
}

func (j *renameJob) step() {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.job.Done++
}

func (j *renameJob) fail(err error) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.job.Failed++
	if len(j.job.Failures) < renameFailuresShown {
		j.job.Failures = append(j.job.Failures, err.Error())
	}
}

// RenameStatus is the user's rename running, or the last one since the
// server started.
func (p *Plugin) RenameStatus(username string) (RenameJob, bool) {
	p.renamesMu.Lock()
	j := p.renames[username]
	p.renamesMu.Unlock()
	if j == nil {
		return RenameJob{}, false
	}
	return j.snapshot(), true
}

// startRenameJob runs work in the background as the user's one rename job.
// work's error is kept for the page when the files did not already say
// what went wrong.
func (p *Plugin) startRenameJob(username string, undo bool, work func(*renameJob) error) error {
	p.renamesMu.Lock()
	defer p.renamesMu.Unlock()
	if j := p.renames[username]; j != nil && j.snapshot().Running() {
		return errRenameRunning
	}
	if p.renames == nil {
		p.renames = map[string]*renameJob{}
	}
	j := &renameJob{job: RenameJob{Undo: undo, Started: time.Now()}}
	p.renames[username] = j
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

// StartRename plans the rename of the user's photos by pattern and applies
// it in the background.
func (p *Plugin) StartRename(username, pattern string, policy ConflictPolicy) error {
	return p.startRenameJob(username, false, func(j *renameJob) error {
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
	return p.startRenameJob(username, true, func(j *renameJob) error {
		return p.UndoLastRename(username, j)
	})
}
