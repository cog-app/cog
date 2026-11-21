package gallery_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// Progress counts the photos with work left, estimates the time left from
// how fast that count went down, and starts its clock again once nothing
// is waiting.
func TestScanProgress(t *testing.T) {
	e := newWorkerEnv(t, "/Photos/a.jpg", "/Photos/b.jpg", "/Photos/c.jpg")

	p, err := e.g.Progress()
	require.NoError(t, err)
	require.Equal(t, 3, p.Total)
	require.Equal(t, 3, p.Remaining)
	require.Equal(t, time.Duration(0), p.ETA)

	_, err = e.db.Exec(`UPDATE media SET quality_score = 1, fingerprint = 'x', content_key = 'k', recognition_state = 1
		WHERE file_path = '/Photos/a.jpg'`)
	require.NoError(t, err)
	e.Clock.Advance(2 * time.Minute)
	p, err = e.g.Progress()
	require.NoError(t, err)
	require.Equal(t, 2, p.Remaining)
	require.Equal(t, 2*time.Minute, p.Elapsed)
	require.Equal(t, 4*time.Minute, p.ETA)
	e.Get(t, "/admin/plugins/gallery", e.alice.Auth).Status(200).Contains("1 of 3 photos analysed").Contains("about 4m left")

	e.scan(t)
	p, err = e.g.Progress()
	require.NoError(t, err)
	require.Equal(t, 0, p.Remaining)
	require.False(t, p.Active())

	e.Clock.Advance(time.Hour)
	_, err = e.db.Exec(`UPDATE media SET quality_score = -1 WHERE file_path = '/Photos/b.jpg'`)
	require.NoError(t, err)
	p, err = e.g.Progress()
	require.NoError(t, err)
	require.Equal(t, time.Duration(0), p.Elapsed)
}
