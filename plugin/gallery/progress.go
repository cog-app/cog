package gallery

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// progressWindow is how far back the rate the estimate is drawn from goes:
// long enough to smooth over a scan's rounds, short enough to follow a
// worker coming or going.
const (
	progressWindow = 10 * time.Minute
	progressSample = 30 * time.Second
)

// ScanProgress is how far the photos are through analysis.
type ScanProgress struct {
	Total, Remaining int
	Elapsed          time.Duration // since there was work waiting
	ETA              time.Duration // 0 until there is a rate to go by
}

func (s ScanProgress) Active() bool { return s.Remaining > 0 }

func (s ScanProgress) Done() int { return s.Total - s.Remaining }

func (s ScanProgress) Percent() float64 {
	if s.Total == 0 {
		return 100
	}
	return 100 * float64(s.Done()) / float64(s.Total)
}

type progressPoint struct {
	at        time.Time
	remaining int
}

type progressTracker struct {
	mu     sync.Mutex
	points []progressPoint
}

func (t *progressTracker) add(at time.Time, remaining int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := len(t.points); n > 0 && at.Sub(t.points[n-1].at) < progressSample/3 {
		return
	}
	t.points = append(t.points, progressPoint{at, remaining})
	cut := 0
	for cut < len(t.points)-1 && at.Sub(t.points[cut].at) > progressWindow {
		cut++
	}
	t.points = t.points[cut:]
}

// eta divides what remains by the rate it has been going down at. New
// photos arriving faster than they are analysed give no estimate.
func (t *progressTracker) eta(now time.Time, remaining int) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.points) == 0 {
		return 0
	}
	first := t.points[0]
	elapsed := now.Sub(first.at)
	done := first.remaining - remaining
	if elapsed < progressSample || done <= 0 {
		return 0
	}
	return time.Duration(float64(remaining) / float64(done) * float64(elapsed))
}

// backlog counts the photos with work left, by each user's settings, and
// all there are.
func (p *Plugin) backlog(sysDB *sql.DB) (remaining, total int, err error) {
	usernames, err := p.ctx.UsersOf("files")
	if err != nil {
		return 0, 0, fmt.Errorf("list users: %w", err)
	}
	for _, username := range usernames {
		db, err := p.DB(username)
		if err != nil {
			return 0, 0, fmt.Errorf("gallery db of %s: %w", username, err)
		}
		cfg, err := p.stageConfig(sysDB, db)
		if err != nil {
			return 0, 0, fmt.Errorf("analysis config of %s: %w", username, err)
		}
		var r, t int
		if err := db.QueryRow(`SELECT
			COALESCE(SUM(m.width = 0 OR (`+cfg.pendingClause()+`)), 0),
			COUNT(*)
			FROM media m WHERE m.shared_from = '' AND (m.width = 0 OR m.mime_type LIKE 'image/%')`).Scan(&r, &t); err != nil {
			return 0, 0, fmt.Errorf("count work of %s: %w", username, err)
		}
		remaining += r
		total += t
	}
	return remaining, total, nil
}

// Progress measures the analysis backlog and records it for the estimate.
// When work starts waiting is kept in the settings, so a restart does not
// reset how long it has been going.
func (p *Plugin) Progress() (ScanProgress, error) {
	sysDB, err := p.SystemDB()
	if err != nil {
		return ScanProgress{}, err
	}
	remaining, total, err := p.backlog(sysDB)
	if err != nil {
		return ScanProgress{}, err
	}
	now := p.ctx.Now()
	p.progress.add(now, remaining)
	prog := ScanProgress{Total: total, Remaining: remaining}

	since, err := strconv.ParseInt(GetSetting(sysDB, "backlog_since", "0"), 10, 64)
	if err != nil {
		p.log.Warn("backlog start unreadable; starting again", "err", err)
		since = 0
	}
	switch {
	case remaining == 0 && since != 0:
		err = SetSettings(sysDB, [][2]string{{"backlog_since", "0"}})
	case remaining > 0 && since == 0:
		since = now.Unix()
		err = SetSettings(sysDB, [][2]string{{"backlog_since", strconv.FormatInt(since, 10)}})
	}
	if err != nil {
		return prog, fmt.Errorf("record backlog start: %w", err)
	}
	if remaining > 0 {
		prog.Elapsed = now.Sub(time.Unix(since, 0))
		prog.ETA = p.progress.eta(now, remaining)
	}
	return prog, nil
}

// trackProgress samples the backlog while nobody is looking, so the
// estimate is ready when someone does.
func (p *Plugin) trackProgress(ctx context.Context) {
	t := time.NewTicker(progressSample)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := p.Progress(); err != nil {
			p.log.Warn("scan progress", "err", err)
		}
	}
}

// shortDuration renders a duration as "2h 5m", "14m" or "under a minute".
func shortDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h >= 24:
		return fmt.Sprintf("%dd %dh", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return "under a minute"
	}
}
