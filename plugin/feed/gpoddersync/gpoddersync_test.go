package gpoddersync_test

import (
	"database/sql"
	"slices"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

const api = "/index.php/apps/gpoddersync"

type subscriptionChanges struct {
	Add       []string `json:"add"`
	Remove    []string `json:"remove"`
	Timestamp int64    `json:"timestamp"`
}

type episodeAction struct {
	Podcast   string `json:"podcast"`
	Episode   string `json:"episode"`
	GUID      string `json:"guid,omitempty"`
	Action    string `json:"action"`
	Timestamp string `json:"timestamp"`
	Started   *int64 `json:"started,omitempty"`
	Position  *int64 `json:"position,omitempty"`
	Total     *int64 `json:"total,omitempty"`
}

type episodeActions struct {
	Actions   []episodeAction `json:"actions"`
	Timestamp int64           `json:"timestamp"`
}

func setup(t *testing.T) (*cogtest.Env, cogtest.HTTPOpt, *sql.DB) {
	t.Helper()
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	plug, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok)
	db, err := plug.(*feed.Plugin).DB(alice.Username)
	require.NoError(t, err)
	// AntennaPod authenticates every request with Basic auth.
	return env, cogtest.BasicAuth(alice.Username, "pass"), db
}

func insertSub(t *testing.T, db *sql.DB, url string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(`INSERT INTO subscriptions (kind, url) VALUES ('rss', ?) RETURNING id`, url).Scan(&id))
	return id
}

func insertItem(t *testing.T, db *sql.DB, subID int64, guid, typ, mediaURL string, duration int) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(`
		INSERT INTO items (subscription_id, guid, type, media_url, duration_sec) VALUES (?, ?, ?, ?, ?)
		RETURNING id`, subID, guid, typ, mediaURL, duration).Scan(&id))
	return id
}

func i64(v int64) *int64 { return &v }

func TestSubscriptions(t *testing.T) {
	env, auth, db := setup(t)

	pod := insertSub(t, db, "https://pod.example/feed.xml")
	insertItem(t, db, pod, "ep1", "audio", "https://pod.example/ep1.mp3", 600)
	blog := insertSub(t, db, "https://blog.example/rss")
	insertItem(t, db, blog, "post1", "text", "", 0)
	insertSub(t, db, "https://unfetched.example/feed.xml")
	_, err := db.Exec(`INSERT INTO subscriptions (kind, url) VALUES ('youtube', 'https://youtube.com/channel/x')`)
	require.NoError(t, err)

	// A blog is not a podcast; a feed not yet fetched may be one.
	var got subscriptionChanges
	env.Get(t, api+"/subscriptions?since=0", auth).Status(200).JSON(&got)
	require.Equal(t, []string{"https://pod.example/feed.xml", "https://unfetched.example/feed.xml"}, got.Add)
	require.Equal(t, []string{}, got.Remove)
	require.True(t, got.Timestamp > 0)

	env.PostJSON(t, api+"/subscription_change/create", map[string]any{
		"add":    []string{"https://new.example/feed.xml", "antennapod_local:/storage/x", "https://pod.example/feed.xml"},
		"remove": []string{"https://unfetched.example/feed.xml"},
	}, auth).Status(200)

	var kind string
	require.NoError(t, db.QueryRow(`SELECT kind FROM subscriptions WHERE url = 'https://new.example/feed.xml'`).Scan(&kind))
	require.Equal(t, "rss", kind)
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE url LIKE 'antennapod_local:%' OR url = 'https://unfetched.example/feed.xml'`).Scan(&n))
	require.Equal(t, 0, n)

	// Deleting in Cog's own UI reaches the client as a removal too.
	require.NoError(t, feed.NewStore(db).DeleteSubscription(pod))

	got = subscriptionChanges{}
	env.Get(t, api+"/subscriptions?since=0", auth).Status(200).JSON(&got)
	require.Equal(t, []string{"https://new.example/feed.xml"}, got.Add)
	require.Equal(t, []string{"https://pod.example/feed.xml", "https://unfetched.example/feed.xml"}, got.Remove)

	// Subscribing again withdraws the removal.
	env.PostJSON(t, api+"/subscription_change/create", map[string]any{
		"add": []string{"https://pod.example/feed.xml"},
	}, auth).Status(200)
	got = subscriptionChanges{}
	env.Get(t, api+"/subscriptions?since=0", auth).Status(200).JSON(&got)
	require.Equal(t, []string{"https://unfetched.example/feed.xml"}, got.Remove)

	// Changes are reported since a time, not all over again.
	got = subscriptionChanges{}
	env.Get(t, api+"/subscriptions?since=4000000000", auth).Status(200).JSON(&got)
	require.Equal(t, []string{}, got.Add)
	require.Equal(t, []string{}, got.Remove)
}

func TestSubscriptions_BadRequests(t *testing.T) {
	env, auth, _ := setup(t)
	env.PostJSON(t, api+"/subscription_change/create", map[string]any{
		"add":    []string{"https://a.example/feed.xml"},
		"remove": []string{"https://a.example/feed.xml"},
	}, auth).Status(400)
	env.Get(t, api+"/subscriptions?since=yesterday", auth).Status(400)
	env.Get(t, api+"/nonsense", auth).Status(404)
	env.Get(t, api+"/subscriptions").Status(401)
}

func TestEpisodeActions(t *testing.T) {
	env, auth, db := setup(t)
	const feedURL = "https://pod.example/feed.xml"
	sub := insertSub(t, db, feedURL)
	ep1 := insertItem(t, db, sub, "ep1", "audio", "https://pod.example/ep1.mp3", 600)
	ep2 := insertItem(t, db, sub, "ep2", "audio", "https://pod.example/ep2.mp3", 300)

	playback := func(id int64) (progress float64, completed bool, read bool) {
		t.Helper()
		err := db.QueryRow(`
			SELECT COALESCE(pb.progress_sec, -1), COALESCE(pb.completed, 0), i.is_read
			FROM items i LEFT JOIN playback pb ON pb.item_id = i.id WHERE i.id = ?`, id).Scan(&progress, &completed, &read)
		require.NoError(t, err)
		return
	}

	// A play matched by GUID, though the client's episode URL differs.
	env.PostJSON(t, api+"/episode_action/create", []episodeAction{{
		Podcast: feedURL, Episode: "https://cdn.example/ep1.mp3?track=1", GUID: "ep1", Action: "PLAY",
		Timestamp: "2026-11-01T10:00:00", Started: i64(0), Position: i64(120), Total: i64(600),
	}}, auth).Status(200)
	progress, completed, _ := playback(ep1)
	require.Equal(t, 120.0, progress)
	require.False(t, completed)

	// An older play from another device does not rewind it.
	env.PostJSON(t, api+"/episode_action/create", []episodeAction{{
		Podcast: feedURL, Episode: "https://pod.example/ep1.mp3", Action: "play",
		Timestamp: "2026-10-01T10:00:00", Position: i64(30), Total: i64(600),
	}}, auth).Status(200)
	progress, _, _ = playback(ep1)
	require.Equal(t, 120.0, progress)

	// Finished: position == total.
	env.PostJSON(t, api+"/episode_action/create", []episodeAction{{
		Podcast: feedURL, Episode: "https://pod.example/ep2.mp3", Action: "play",
		Timestamp: "2026-11-01T11:00:00", Position: i64(300), Total: i64(300),
	}}, auth).Status(200)
	_, completed, read := playback(ep2)
	require.True(t, completed)
	require.True(t, read)

	// Cog's own player is reported back as a play.
	require.NoError(t, feed.NewStore(db).SetPlayback(ep1, 200, false))

	var got episodeActions
	env.Get(t, api+"/episode_action?since=0", auth).Status(200).JSON(&got)
	plays := map[string]int64{}
	for _, a := range got.Actions {
		require.Equal(t, "play", a.Action)
		require.NotNil(t, a.Position)
		plays[a.Episode] = *a.Position
	}
	require.Equal(t, int64(200), plays["https://pod.example/ep1.mp3"])
	require.Equal(t, int64(300), plays["https://pod.example/ep2.mp3"])
	require.Equal(t, int64(120), plays["https://cdn.example/ep1.mp3?track=1"])
	// Oldest first, so a client applying them in order ends on the latest.
	require.True(t, slices.IsSortedFunc(got.Actions, func(a, b episodeAction) int {
		return strings.Compare(a.Timestamp, b.Timestamp)
	}))

	// Marked unplayed on the phone.
	env.PostJSON(t, api+"/episode_action/create", []episodeAction{{
		Podcast: feedURL, Episode: "https://pod.example/ep2.mp3", Action: "new", Timestamp: "2026-11-02T00:00:00",
	}}, auth).Status(200)
	progress, _, read = playback(ep2)
	require.Equal(t, -1.0, progress)
	require.False(t, read)

	got = episodeActions{}
	env.Get(t, api+"/episode_action?since=0", auth).Status(200).JSON(&got)
	var ep2Actions []string
	for _, a := range got.Actions {
		if a.Episode == "https://pod.example/ep2.mp3" {
			ep2Actions = append(ep2Actions, a.Action)
		}
	}
	require.Equal(t, []string{"new"}, ep2Actions)

	got = episodeActions{}
	env.Get(t, api+"/episode_action?since=4000000000", auth).Status(200).JSON(&got)
	require.Equal(t, 0, len(got.Actions))
}

func TestEpisodeActions_BadRequests(t *testing.T) {
	env, auth, _ := setup(t)
	for _, body := range [][]episodeAction{
		{{Podcast: "https://p.example/f", Action: "play", Position: i64(1)}},
		{{Podcast: "https://p.example/f", Episode: "https://p.example/e", Action: "skip"}},
		{{Podcast: "https://p.example/f", Episode: "https://p.example/e", Action: "play"}},
		{{Podcast: "https://p.example/f", Episode: "https://p.example/e", Action: "new", Timestamp: "yesterday"}},
	} {
		env.PostJSON(t, api+"/episode_action/create", body, auth).Status(400)
	}
}
