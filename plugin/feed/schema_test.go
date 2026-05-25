package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestSchema(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")

	// Open the user's plugin DB via the plugin's public DB() helper.
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	if !ok {
		t.Fatal("feed plugin not registered")
	}
	p := raw.(*feed.Plugin)
	db, err := p.DB(u.User.Username)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	want := []string{
		"folders", "subscriptions", "items",
		"tags", "item_tags",
		"filters", "downloads", "playback", "playlists",
	}
	for _, name := range want {
		var got string
		row := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name)
		if err := row.Scan(&got); err != nil {
			t.Errorf("table %s missing: %v", name, err)
		}
	}
}
