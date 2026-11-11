package feed

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/migrate"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// A subscription from before titles could be the user's keeps following its
// feed's title after the migration.
func TestSubscriptionTitleMigration(t *testing.T) {
	s := newStoreInternal(t)
	_, err := s.db.Exec(`ALTER TABLE subscriptions DROP COLUMN title_locked`)
	require.NoError(t, err)
	id, err := s.CreateSubscription(Subscription{Kind: "rss", URL: "https://x.test/f", Title: "Old"})
	require.NoError(t, err)

	require.NoError(t, migrate.Migrate(s.db, sqlFS))
	require.NoError(t, migrate.Migrate(s.db, sqlFS)) // and again, as every open does

	require.NoError(t, s.UpdateSubscriptionMetadata(id, map[string]string{"title": "New"}))
	sub, err := s.GetSubscription(id)
	require.NoError(t, err)
	assert.Equal(t, "New", sub.Title)
}
