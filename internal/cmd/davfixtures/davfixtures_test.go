package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// The old tasks schema, as master had it before the DAV storage rewrite.
const oldTasksSchema = `
CREATE TABLE lists (id INTEGER PRIMARY KEY AUTOINCREMENT, uid TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL DEFAULT 'Tasks', color TEXT NOT NULL DEFAULT '#0082c9', sort_order INTEGER NOT NULL DEFAULT 0);
CREATE TABLE tasks (id INTEGER PRIMARY KEY AUTOINCREMENT, list_id INTEGER NOT NULL, uid TEXT NOT NULL,
	parent_id INTEGER, summary TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '',
	priority INTEGER NOT NULL DEFAULT 0, percent_complete INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'NEEDS-ACTION', completed_at INTEGER, due_at INTEGER, start_at INTEGER,
	sort_order INTEGER NOT NULL DEFAULT 0, ics_data TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
INSERT INTO lists (id, uid, name) VALUES (1, 'personal', 'Personal'), (2, 'groceries-uid', 'Groceries');
INSERT INTO tasks (id, list_id, uid, parent_id, summary, description, priority, status, due_at, sort_order, ics_data, created_at, updated_at)
VALUES (1, 1, 'parent', NULL, 'Parent', 'Two, lines;
here', 1, 'IN-PROCESS', 1795167000, 0, 'BEGIN:VCALENDAR', 1793000000, 1793000100),
       (2, 1, 'child', 1, 'Child', '', 0, 'NEEDS-ACTION', NULL, 3, '', 1793000000, 1793000000),
       (3, 2, 'milk', NULL, 'Milk', '', 0, 'COMPLETED', NULL, 0, '', 1793000000, 1793000000);
UPDATE tasks SET completed_at = 1793000200, percent_complete = 100 WHERE id = 3;
`

func TestTasksFromOldDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = db.Exec(oldTasksSchema)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	out := filepath.Join(dir, "import")
	require.NoError(t, runTasks([]string{"-db", path, "-import", out}))

	personal, err := os.ReadFile(filepath.Join(out, "tasks", "personal.ics"))
	require.NoError(t, err)
	text := string(personal)
	assert.Contains(t, text, "X-WR-CALNAME:Personal\r\n")
	assert.Contains(t, text, "DESCRIPTION:Two\\, lines\\;\\nhere\r\n")
	assert.Contains(t, text, "STATUS:IN-PROCESS\r\n")
	assert.Contains(t, text, "DUE:20261120T093000Z\r\n")
	assert.Contains(t, text, "RELATED-TO;RELTYPE=PARENT:parent\r\n")
	assert.Contains(t, text, "X-APPLE-SORT-ORDER:3\r\n")
	assert.Equal(t, 2, strings.Count(text, "BEGIN:VTODO"))

	groceries, err := os.ReadFile(filepath.Join(out, "tasks", "groceries-uid.ics"))
	require.NoError(t, err)
	assert.Contains(t, string(groceries), "COMPLETED:20261026T073640Z\r\n")
	assert.Contains(t, string(groceries), "PERCENT-COMPLETE:100\r\n")
}

func lines(t *testing.T, text string) []string {
	t.Helper()
	comps, err := contentline.Parse(strings.NewReader(text))
	require.NoError(t, err)
	return propLines(comps[0])
}

// Spellings that mean the same compare equal; a change of meaning does not.
func TestCompareNormalises(t *testing.T) {
	same := [][2]string{
		{"DTSTART;VALUE=DATE-TIME:20260101T090000Z", "DTSTART:20260101T090000Z"},
		{"DTSTART:20260101", "DTSTART;VALUE=DATE:20260101"},
		{"DTSTART;TZID=US/Pacific:20260101", "DTSTART;VALUE=DATE:20260101"},
		{"DTSTART;TZID=W. Europe Standard Time:20260101T090000", "DTSTART;TZID=Europe/Berlin:20260101T090000"},
		{"EXDATE:20260101T090000Z\r\nEXDATE:20260108T090000Z", "EXDATE:20260101T090000Z,20260108T090000Z"},
		{"SUMMARY:a, b", "SUMMARY:a\\, b"},
		{"X-A;TYPE=WORK;TYPE=pref:x", "X-A;TYPE=WORK,pref:x"},
	}
	for _, c := range same {
		a := lines(t, "BEGIN:VEVENT\r\n"+c[0]+"\r\nEND:VEVENT\r\n")
		b := lines(t, "BEGIN:VEVENT\r\n"+c[1]+"\r\nEND:VEVENT\r\n")
		assert.Equal(t, a, b, c[0])
	}
	a := lines(t, "BEGIN:VEVENT\r\nDTSTART;TZID=Europe/Berlin:20260101T090000\r\nEND:VEVENT\r\n")
	b := lines(t, "BEGIN:VEVENT\r\nDTSTART:20260101T090000Z\r\nEND:VEVENT\r\n")
	assert.NotEqual(t, a, b)

	exact = true
	t.Cleanup(func() { exact = false })
	a = lines(t, "BEGIN:VEVENT\r\nSUMMARY:a, b\r\nEND:VEVENT\r\n")
	b = lines(t, "BEGIN:VEVENT\r\nSUMMARY:a\\, b\r\nEND:VEVENT\r\n")
	assert.NotEqual(t, a, b)
}

// An import that keeps everything compares clean; one that loses a resource
// or a component does not, and copies of a UID in one collection count once.
func TestCompareResources(t *testing.T) {
	dir := t.TempDir()
	write := func(p, text string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte(text), 0o600))
	}
	ev := func(uid, summary string) string {
		return "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nSUMMARY:" + summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	}
	write(filepath.Join(dir, "want/cal/a.ics"), ev("a", "A"))
	write(filepath.Join(dir, "want/cal/a2.ics"), ev("a", "A again"))
	write(filepath.Join(dir, "want/cal/b.ics"), "BEGIN:VEVENT\r\nUID:b\r\nSUMMARY:Bare\r\nEND:VEVENT\r\n")
	write(filepath.Join(dir, "got/cal/a.ics"), ev("a", "A again"))
	write(filepath.Join(dir, "got/cal/b.ics"), ev("b", "Bare"))

	var buf bytes.Buffer
	stdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	err = runCompare([]string{"-got", filepath.Join(dir, "got"), filepath.Join(dir, "want")})
	os.Stdout = stdout
	require.NoError(t, w.Close())
	_, rerr := buf.ReadFrom(r)
	require.NoError(t, rerr)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "0 missing, 0 extra, 0 differ, 1 duplicates in one collection")

	require.NoError(t, os.Remove(filepath.Join(dir, "got/cal/b.ics")))
	err = runCompare([]string{"-got", filepath.Join(dir, "got"), filepath.Join(dir, "want")})
	assert.NotNil(t, err)
}

func TestPhotosFromOldDatabase(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")
	require.NoError(t, os.MkdirAll(filepath.Join(data, "contacts", "u", "photos"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(data, "contacts", "u", "photos", "a.png"), png, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(data, "contacts", "u", "photos", "b.png"), png, 0o600))

	path := filepath.Join(dir, "plugin.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE cards (uid TEXT NOT NULL, photo_path TEXT NOT NULL DEFAULT '');
		INSERT INTO cards VALUES ('a', '/data/contacts/u/photos/a.png'), ('b', '/data/contacts/u/photos/b.png'),
			('own', '/data/contacts/u/photos/a.png'), ('none', '')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	vcf := filepath.Join(dir, "contacts.vcf")
	require.NoError(t, os.WriteFile(vcf, []byte(
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:a\r\nFN:A\r\nEND:VCARD\r\n"+
			"BEGIN:VCARD\r\nVERSION:4.0\r\nUID:b\r\nFN:B\r\nEND:VCARD\r\n"+
			"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:own\r\nFN:Own\r\nPHOTO;VALUE=uri:https://example.com/own.jpg\r\nEND:VCARD\r\n"+
			"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:none\r\nFN:None\r\nEND:VCARD\r\n"), 0o600))
	require.NoError(t, runPhotos([]string{"-db", path, "-data", data, "-vcf", vcf}))

	out, err := os.ReadFile(vcf)
	require.NoError(t, err)
	comps, err := contentline.Parse(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, 4, len(comps))
	enc := base64.StdEncoding.EncodeToString(png)
	a := comps[0].Prop("PHOTO")
	require.True(t, a != nil)
	assert.Equal(t, "b", a.Param("ENCODING"))
	assert.Equal(t, "PNG", a.Param("TYPE"))
	assert.Equal(t, enc, a.Value)
	b := comps[1].Prop("PHOTO")
	require.True(t, b != nil)
	assert.Equal(t, "data:image/png;base64,"+enc, b.Value)
	assert.Equal(t, "https://example.com/own.jpg", comps[2].Prop("PHOTO").Value) // a client's own photo wins
	assert.Nil(t, comps[3].Prop("PHOTO"))
}
