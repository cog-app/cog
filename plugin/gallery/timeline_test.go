package gallery

import (
	"database/sql"
	"math"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func at(t *testing.T, s string) int64 {
	t.Helper()
	tm, err := time.Parse(time.DateTime, s)
	require.NoError(t, err)
	return tm.Unix()
}

func whens(media []Media) []int64 {
	out := make([]int64, len(media))
	for i := range media {
		out[i] = mediaWhen(&media[i])
	}
	return out
}

func TestDayStart(t *testing.T) {
	assert.Equal(t, at(t, "2024-03-05 00:00:00"), dayStart(at(t, "2024-03-05 13:14:15")))
	assert.Equal(t, at(t, "2024-03-05 00:00:00"), dayStart(at(t, "2024-03-05 00:00:00")))
	assert.Equal(t, at(t, "1969-12-31 00:00:00"), dayStart(at(t, "1969-12-31 23:00:00")))
}

// Pages end on day boundaries: once a page has its photos it takes the rest
// of the last day, so no day is split between pages, and walking the pages
// either way visits every photo exactly once.
func TestTimelinePagesKeepDaysWhole(t *testing.T) {
	db := testDB(t)
	var all []int64
	for _, s := range []string{
		"2024-03-07 09:00:00",
		"2024-03-06 18:00:00", "2024-03-06 12:00:00", "2024-03-06 08:00:00",
		"2024-02-28 10:00:00",
		"1969-12-31 10:00:00",
	} {
		insertTestMedia(t, db, at(t, s))
		all = append(all, at(t, s))
	}

	media, next, err := OlderMedia(db, math.MaxInt64, 2)
	require.NoError(t, err)
	assert.Equal(t, all[:4], whens(media)) // all of March 6, not just its first photo
	assert.Equal(t, at(t, "2024-03-06 00:00:00"), next)

	media, next, err = OlderMedia(db, next, 2)
	require.NoError(t, err)
	assert.Equal(t, all[4:], whens(media))
	assert.Equal(t, int64(0), next) // nothing older

	media, next, err = NewerMedia(db, at(t, "1969-01-01 00:00:00"), 2)
	require.NoError(t, err)
	assert.Equal(t, all[4:], whens(media)) // newest first, like OlderMedia
	assert.Equal(t, at(t, "2024-02-29 00:00:00"), next)

	media, next, err = NewerMedia(db, next, 2)
	require.NoError(t, err)
	assert.Equal(t, all[1:4], whens(media)) // all of March 6
	assert.Equal(t, at(t, "2024-03-07 00:00:00"), next)

	media, next, err = NewerMedia(db, next, 2)
	require.NoError(t, err)
	assert.Equal(t, all[:1], whens(media))
	assert.Equal(t, int64(0), next)

	ok, err := HasMediaSince(db, at(t, "2024-03-07 09:00:00"))
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = HasMediaSince(db, at(t, "2024-03-07 09:00:01"))
	require.NoError(t, err)
	assert.False(t, ok)

	months, err := TimelineMonths(db)
	require.NoError(t, err)
	assert.Equal(t, []TimelineMonth{
		{Year: 2024, Month: time.March, Count: 4},
		{Year: 2024, Month: time.February, Count: 1},
		{Year: 1969, Month: time.December, Count: 1},
	}, months)
	assert.Equal(t, "1969-12", months[2].Key())
	assert.Equal(t, at(t, "2024-04-01 00:00:00"), months[0].End())
}

// The timeline reads its pages by streaming the index and stopping early. A
// query that stops matching idx_media_when would sort the whole table on
// every page instead.
func TestTimelineQueriesUseIndex(t *testing.T) {
	db := testDB(t)
	for _, q := range []string{
		`SELECT m.id FROM media m WHERE COALESCE(m.date_taken, m.date_indexed) < 1
			ORDER BY COALESCE(m.date_taken, m.date_indexed) DESC, m.id DESC`,
		`SELECT m.id FROM media m WHERE COALESCE(m.date_taken, m.date_indexed) >= 1
			ORDER BY COALESCE(m.date_taken, m.date_indexed), m.id`,
		`SELECT EXISTS (SELECT 1 FROM media WHERE COALESCE(date_taken, date_indexed) >= 1)`,
	} {
		plan := queryPlan(t, db, q)
		assert.True(t, strings.Contains(plan, "idx_media_when"), "plan for %s:\n%s", q, plan)
		assert.False(t, strings.Contains(plan, "TEMP B-TREE"), "plan for %s:\n%s", q, plan)
	}
}

func queryPlan(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN " + q)
	require.NoError(t, err)
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		b.WriteString(detail + "\n")
	}
	require.NoError(t, rows.Err())
	return b.String()
}

func TestScrubMarks(t *testing.T) {
	marks := scrubMarks([]TimelineMonth{
		{Year: 2024, Month: time.March, Count: 50},
		{Year: 2024, Month: time.February, Count: 25},
		{Year: 2023, Month: time.December, Count: 1},
		{Year: 2022, Month: time.June, Count: 24},
	})
	require.Equal(t, 4, len(marks))
	assert.Equal(t, "March 2024", marks[0].Label)
	assert.Equal(t, "2024-03", marks[0].Key)
	assert.Equal(t, "2024", marks[0].Year)
	assert.Equal(t, "", marks[1].Year) // same year
	assert.Equal(t, 75.0, marks[2].Top)
	assert.Equal(t, "2023", marks[2].Year)
	assert.Equal(t, "", marks[3].Year) // 1% after 2023's label: no room
	assert.Equal(t, 76.0, marks[3].Top)
	assert.Equal(t, 24.0, marks[3].Height)
	assert.Equal(t, 0, len(scrubMarks(nil)))
}
