package gallery_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
)

// seedTimeline inserts n photos for username, one every six hours going back
// from start, so they cover n/4 days.
func seedTimeline(t *testing.T, db *sql.DB, start time.Time, n int) {
	t.Helper()
	for i := range n {
		when := start.Add(-time.Duration(i) * 6 * time.Hour).Unix()
		_, err := db.Exec(`INSERT INTO media (file_id, file_path, file_etag, mime_type, width, height, date_taken, date_indexed)
			VALUES (?, ?, 'etag', 'image/jpeg', 100, 100, ?, ?)`,
			i+1, fmt.Sprintf("/Photos/%03d.jpg", i), when, when)
		require.NoError(t, err)
	}
}

func TestTimelinePaging(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")
	db, err := env.Providers.Plugins.PluginDB(alice.Username, "gallery")
	require.NoError(t, err)
	// 300 photos, 75 days back from noon on March 31: into mid-January.
	seedTimeline(t, db, time.Date(2024, 3, 31, 12, 0, 0, 0, time.UTC), 300)

	// The first page ends on a day boundary and says where to carry on.
	page := env.Get(t, "/apps/gallery/", alice.Auth).Status(200).ReadBody()
	tiles := strings.Count(page, `class="gallery-thumb"`)
	assert.True(t, tiles >= 100 && tiles < 104, "first page has %d photos", tiles)
	older := time.Date(2024, 3, 6, 0, 0, 0, 0, time.UTC).Unix() // 3 photos on the 31st, 4 a day after: the 100th is on the 6th
	assert.True(t, strings.Contains(page, fmt.Sprintf(`data-src="/apps/gallery/api/timeline/page?before=%d"`, older)), "first page links the next")
	assert.False(t, strings.Contains(page, `api/timeline/page?after=`), "the newest page has nothing above it")

	// The scrubber has every month, newest first, linked to the timeline
	// starting at its end.
	for _, m := range []string{"March 2024", "February 2024", "January 2024"} {
		assert.True(t, strings.Contains(page, `aria-label="`+m+`"`), "scrubber has %s", m)
	}
	feb := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).Unix()
	assert.True(t, strings.Contains(page, fmt.Sprintf(`href="/apps/gallery/?before=%d"`, feb)), "February links to its end")

	// Walking the older pages reaches every photo once.
	seen := tiles
	next := fmt.Sprintf("/apps/gallery/api/timeline/page?before=%d", older)
	for next != "" {
		body := env.Get(t, next, alice.Auth).Status(200).ReadBody()
		seen += strings.Count(body, `class="gallery-thumb"`)
		next = ""
		if _, rest, ok := strings.Cut(body, `data-src="`); ok {
			next, _, _ = strings.Cut(rest, `"`)
		}
	}
	assert.Equal(t, 300, seen)

	// Jumping to February starts there, with March loadable above.
	page = env.Get(t, fmt.Sprintf("/apps/gallery/?before=%d", feb), alice.Auth).Status(200).ReadBody()
	assert.True(t, strings.Contains(page, "Thursday, February 29, 2024"), "jump starts at the end of February")
	assert.False(t, strings.Contains(page, "March 1, 2024"), "jump leaves March out")
	assert.True(t, strings.Contains(page, fmt.Sprintf(`data-src="/apps/gallery/api/timeline/page?after=%d"`, feb)), "jump links newer photos")

	newer := env.Get(t, fmt.Sprintf("/apps/gallery/api/timeline/page?after=%d", feb), alice.Auth).Status(200).ReadBody()
	assert.True(t, strings.Contains(newer, "Friday, March 1, 2024"), "newer page starts at March 1")
	first, _, _ := strings.Cut(newer, "gallery-day-title")
	assert.True(t, strings.Contains(first, `data-timeline-more="after"`), "newer page's marker comes before its days")

	env.Get(t, "/apps/gallery/?before=soon", alice.Auth).Status(400)
	env.Get(t, "/apps/gallery/api/timeline/page", alice.Auth).Status(400)
}
