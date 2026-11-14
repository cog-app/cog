package feed_test

import (
	"database/sql"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestDetectImportFormat(t *testing.T) {
	assert.Equal(t, feed.ImportOPML, feed.DetectImportFormat([]byte(sampleOPML)))
	assert.Equal(t, feed.ImportOPML, feed.DetectImportFormat([]byte("\xef\xbb\xbf\n  <opml/>")))
	assert.Equal(t, feed.ImportNewPipe, feed.DetectImportFormat(makeNewPipeZip(t)))
	assert.Equal(t, feed.ImportAntennaPod, feed.DetectImportFormat(makeAntennaPodDB(t)))
	assert.Equal(t, feed.ImportYouTubeCSV, feed.DetectImportFormat([]byte(sampleYouTubeCSV)))
}

// TestImportIntoFolder checks an import puts what its export does not file
// into the folder it is given, and what it does file into the folder of that
// name, reusing one that exists.
func TestImportIntoFolder(t *testing.T) {
	s := newStore(t)
	target, err := s.CreateFolder("Imported")
	require.NoError(t, err)
	tech, err := s.CreateFolder("Tech")
	require.NoError(t, err)

	res, err := s.Import("", []byte(sampleOPML), sql.NullInt64{Int64: target, Valid: true})
	require.NoError(t, err)
	assert.Equal(t, feed.Imported{Added: 3}, res)

	folders, err := s.ListFolders()
	require.NoError(t, err)
	assert.Equal(t, 2, len(folders))
	subs, err := s.ListSubscriptions()
	require.NoError(t, err)
	in := map[string]int64{}
	for _, sub := range subs {
		in[sub.URL] = sub.FolderID.Int64
	}
	assert.Equal(t, map[string]int64{
		"https://go.dev/blog/feed.atom": tech,
		"https://hnrss.org/frontpage":   tech,
		"https://example.com/feed.xml":  target,
	}, in)
}
