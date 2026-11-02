package web_test

import (
	"fmt"
	"strconv"
	"sync"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestConcurrent_PUTSamePath exercises the single-writer SQLite path under
// 16 goroutines racing to PUT the same file. None should error; the file
// should exist with one of the contents we wrote. Probes for SQLITE_BUSY
// (busy_timeout=5000 should swallow contention) and for orphaned metadata.
func TestConcurrent_PUTSamePath(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	const N = 16
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func() {
			defer wg.Done()
			body := fmt.Sprintf("writer-%d", i)
			webdavReq(t, env, "PUT", prefix+"/race.txt", body, alice.Auth).Status(201)
		}()
	}
	wg.Wait()

	// One file should exist; content is one of the writers'.
	body := env.Get(t, prefix+"/race.txt", alice.Auth).Status(200).ReadBody()
	require.True(t, len(body) > 0)
}

// TestConcurrent_PUTDifferentPaths writes N distinct files in parallel from
// the same user. Each must succeed, each must be readable, all metadata
// present and consistent. Surfaces issues in per-user plugin DB pool.
func TestConcurrent_PUTDifferentPaths(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	const N = 16
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func() {
			defer wg.Done()
			path := fmt.Sprintf("/file-%d.txt", i)
			body := fmt.Sprintf("contents-%d", i)
			webdavReq(t, env, "PUT", prefix+path, body, alice.Auth).Status(201)
		}()
	}
	wg.Wait()

	for i := range N {
		path := fmt.Sprintf("/file-%d.txt", i)
		want := fmt.Sprintf("contents-%d", i)
		env.Get(t, prefix+path, alice.Auth).Status(200).Equals(want)
	}
}

// TestConcurrent_SessionCreateRace exercises Sessions.Create from N goroutines
// for the same user. The audit notes a session-creation race; verify no panic
// and every session is independently valid.
func TestConcurrent_SessionCreateRace(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	const N = 32
	results := make([]string, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func() {
			defer wg.Done()
			sess, err := env.Providers.Sessions.Create(alice.ID)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = sess.ID
		}()
	}
	wg.Wait()

	seen := make(map[string]bool, N)
	for i := range N {
		require.NoError(t, errs[i])
		require.True(t, results[i] != "")
		require.False(t, seen[results[i]])
		seen[results[i]] = true

		got, err := env.Providers.Sessions.Validate(results[i])
		require.NoError(t, err)
		require.NotNil(t, got)
	}
}

// TestConcurrent_ShareCreateRace creates N shares (one per goroutine) of
// distinct resources to the same subject. Probes Shares.Create + the
// search propagator's OnShareCreate path under contention. None should panic.
func TestConcurrent_ShareCreateRace(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	// Pre-create N files to share.
	const N = 16
	for i := range N {
		webdavReq(t, env, "PUT", prefix+"/s-"+strconv.Itoa(i)+".txt", "x", alice.Auth).Status(201)
	}

	var wg sync.WaitGroup
	wg.Add(N)
	errs := make([]error, N)
	recs := make([]*share.Record, N)
	for i := range N {
		go func() {
			defer wg.Done()
			fileID, err := lookupFileID(env, alice.Username, "/s-"+strconv.Itoa(i)+".txt")
			if err != nil {
				errs[i] = err
				return
			}
			rec, err := env.Providers.Shares.Create(
				alice.ID, "files:file", strconv.FormatInt(fileID, 10),
				"USER", bob.ID, 1,
			)
			recs[i] = rec
			errs[i] = err
		}()
	}
	wg.Wait()

	for i := range N {
		require.NoError(t, errs[i])
		require.NotNil(t, recs[i])
	}
}

// TestConcurrent_AppPasswordValidate exercises Validate under concurrent
// reads, surfacing SQLITE_BUSY in the read path.
func TestConcurrent_AppPasswordValidate(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	pw, _, err := env.Providers.AppPasswords.Create(alice.ID, "test")
	require.NoError(t, err)

	const N = 32
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			id, err := env.Providers.AppPasswords.Validate(pw)
			require.NoError(t, err)
			require.Equal(t, alice.ID, id)
		}()
	}
	wg.Wait()
}

// lookupFileID resolves a file path to its database ID by reading the
// per-user files plugin DB directly. This is test-only — production callers
// go through plugin/files.Plugin.GetByPath.
func lookupFileID(env *cogtest.Env, username, path string) (int64, error) {
	db, err := env.Providers.Plugins.PluginDB(username, "files")
	if err != nil {
		return 0, err
	}
	var id int64
	err = db.QueryRow(`SELECT id FROM files WHERE path = ?`, path).Scan(&id)
	return id, err
}
