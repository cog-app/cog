package web_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// --- PROPFIND Depth edge cases ---

func TestWebDAV_PROPFIND_Depth0_DirReturnsOnlySelf(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	// Put a file so root has children.
	webdavReq(t, env, "PUT", prefix+"/visible.txt", "data", alice.Auth).Status(201)

	resp := webdavReq(t, env, "PROPFIND", prefix+"/", "",
		alice.Auth, cogtest.Header("Depth", "0"))
	resp.Status(207)
	body := resp.ReadBody()
	// Depth=0 must not include children.
	require.NotContains(t, body, "visible.txt")
}

func TestWebDAV_PROPFIND_DepthDefaultIs1(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "PUT", prefix+"/child.txt", "data", alice.Auth).Status(201)

	// No Depth header.
	resp := webdavReq(t, env, "PROPFIND", prefix+"/", "", alice.Auth)
	resp.Status(207)
	require.Contains(t, resp.ReadBody(), "child.txt")
}

func TestWebDAV_PROPFIND_DepthInfinity_DocumentedBehavior(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "MKCOL", prefix+"/sub", "", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", prefix+"/sub/deep.txt", "deep", alice.Auth).Status(201)

	// "infinity" is not "1" → handler treats it like depth=0 (no children).
	// This test pins current behavior so a future infinity implementation
	// surfaces here.
	resp := webdavReq(t, env, "PROPFIND", prefix+"/", "",
		alice.Auth, cogtest.Header("Depth", "infinity"))
	resp.Status(207)
	require.NotContains(t, resp.ReadBody(), "deep.txt")
}

func TestWebDAV_PROPFIND_NotFound(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "PROPFIND", prefix+"/nope.txt", "", alice.Auth).Status(404)
}

// --- COPY Overwrite=F ---

func TestWebDAV_COPY_OverwriteF_PreconditionFailed(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "PUT", prefix+"/src.txt", "src", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", prefix+"/dst.txt", "dst", alice.Auth).Status(201)

	dest := env.Providers.Config.BaseURL + prefix + "/dst.txt"
	webdavReq(t, env, "COPY", prefix+"/src.txt", "",
		alice.Auth,
		cogtest.Header("Destination", dest),
		cogtest.Header("Overwrite", "F")).
		Status(412)

	// Destination unchanged.
	env.Get(t, prefix+"/dst.txt", alice.Auth).Status(200).Equals("dst")
}

func TestWebDAV_COPY_OverwriteT_Default_NoContent(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "PUT", prefix+"/src.txt", "newcontent", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", prefix+"/dst.txt", "oldcontent", alice.Auth).Status(201)

	dest := env.Providers.Config.BaseURL + prefix + "/dst.txt"
	// Overwrite header absent → defaults to T. Overwriting existing → 204.
	webdavReq(t, env, "COPY", prefix+"/src.txt", "",
		alice.Auth, cogtest.Header("Destination", dest)).
		Status(204)

	env.Get(t, prefix+"/dst.txt", alice.Auth).Status(200).Equals("newcontent")
}

func TestWebDAV_COPY_NewDestination_Created(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "PUT", prefix+"/src.txt", "abc", alice.Auth).Status(201)

	dest := env.Providers.Config.BaseURL + prefix + "/dst.txt"
	webdavReq(t, env, "COPY", prefix+"/src.txt", "",
		alice.Auth, cogtest.Header("Destination", dest)).
		Status(201)

	env.Get(t, prefix+"/dst.txt", alice.Auth).Status(200).Equals("abc")
}

func TestWebDAV_COPY_Directory_Recursive(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "MKCOL", prefix+"/srcdir", "", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", prefix+"/srcdir/a.txt", "A", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", prefix+"/srcdir/b.txt", "B", alice.Auth).Status(201)

	dest := env.Providers.Config.BaseURL + prefix + "/dstdir"
	webdavReq(t, env, "COPY", prefix+"/srcdir", "",
		alice.Auth, cogtest.Header("Destination", dest)).
		Status(201)

	env.Get(t, prefix+"/dstdir/a.txt", alice.Auth).Status(200).Equals("A")
	env.Get(t, prefix+"/dstdir/b.txt", alice.Auth).Status(200).Equals("B")
}

func TestWebDAV_COPY_MissingDestination(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "PUT", prefix+"/src.txt", "x", alice.Auth).Status(201)

	webdavReq(t, env, "COPY", prefix+"/src.txt", "", alice.Auth).
		Status(400).Contains("Destination")
}

func TestWebDAV_COPY_SourceNotFound(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	dest := env.Providers.Config.BaseURL + prefix + "/dst.txt"
	webdavReq(t, env, "COPY", prefix+"/missing.txt", "",
		alice.Auth, cogtest.Header("Destination", dest)).
		Status(404)
}

// --- MOVE Overwrite ---

// TestWebDAV_MOVE_Overwrite: Overwrite: F refuses an existing destination
// and leaves both files as they were; without it the destination is
// replaced (RFC 4918 §10.6).
func TestWebDAV_MOVE_Overwrite(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "PUT", prefix+"/src.txt", "src", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", prefix+"/dst.txt", "dst", alice.Auth).Status(201)

	dest := env.Providers.Config.BaseURL + prefix + "/dst.txt"
	webdavReq(t, env, "MOVE", prefix+"/src.txt", "",
		alice.Auth,
		cogtest.Header("Destination", dest),
		cogtest.Header("Overwrite", "F")).Status(412)
	webdavReq(t, env, "GET", prefix+"/dst.txt", "", alice.Auth).Status(200).Equals("dst")
	webdavReq(t, env, "GET", prefix+"/src.txt", "", alice.Auth).Status(200).Equals("src")

	webdavReq(t, env, "MOVE", prefix+"/src.txt", "",
		alice.Auth,
		cogtest.Header("Destination", dest)).Status(204)
	webdavReq(t, env, "GET", prefix+"/dst.txt", "", alice.Auth).Status(200).Equals("src")
	webdavReq(t, env, "GET", prefix+"/src.txt", "", alice.Auth).Status(404)
}

func TestWebDAV_MOVE_MissingDestination(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "PUT", prefix+"/m.txt", "x", alice.Auth).Status(201)
	webdavReq(t, env, "MOVE", prefix+"/m.txt", "", alice.Auth).
		Status(400).Contains("Destination")
}

// --- PUT edge cases ---

func TestWebDAV_PUT_ZeroLength(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "PUT", prefix+"/empty.txt", "", alice.Auth).Status(201)
	env.Get(t, prefix+"/empty.txt", alice.Auth).Status(200).Equals("")
}

// --- Chunked upload edge cases ---

func TestChunkedUpload_OutOfOrderChunks(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	uploadPrefix := "/remote.php/dav/uploads/" + alice.ID
	filePrefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "MKCOL", uploadPrefix+"/ooo/", "", alice.Auth).Status(201)

	// Upload chunk 2 before chunk 1 — the server sorts by name on assemble.
	webdavReq(t, env, "PUT", uploadPrefix+"/ooo/00002", "world", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", uploadPrefix+"/ooo/00001", "hello ", alice.Auth).Status(201)

	dest := env.Providers.Config.BaseURL + filePrefix + "/oo.txt"
	webdavReq(t, env, "MOVE", uploadPrefix+"/ooo/.file", "",
		alice.Auth, cogtest.Header("Destination", dest)).
		Status(201)

	env.Get(t, filePrefix+"/oo.txt", alice.Auth).Status(200).Equals("hello world")
}

func TestChunkedUpload_DuplicateChunkOverwrites(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	uploadPrefix := "/remote.php/dav/uploads/" + alice.ID
	filePrefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "MKCOL", uploadPrefix+"/dup/", "", alice.Auth).Status(201)

	// First version of chunk 00001.
	webdavReq(t, env, "PUT", uploadPrefix+"/dup/00001", "stale ", alice.Auth).Status(201)
	// Replay chunk 00001 — the latest write should win on assemble.
	webdavReq(t, env, "PUT", uploadPrefix+"/dup/00001", "fresh ", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", uploadPrefix+"/dup/00002", "value", alice.Auth).Status(201)

	dest := env.Providers.Config.BaseURL + filePrefix + "/dup.txt"
	webdavReq(t, env, "MOVE", uploadPrefix+"/dup/.file", "",
		alice.Auth, cogtest.Header("Destination", dest)).
		Status(201)

	env.Get(t, filePrefix+"/dup.txt", alice.Auth).Status(200).Equals("fresh value")
}

func TestChunkedUpload_AssembleWithoutChunks_Rejected(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	uploadPrefix := "/remote.php/dav/uploads/" + alice.ID
	filePrefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "MKCOL", uploadPrefix+"/empty/", "", alice.Auth).Status(201)

	dest := env.Providers.Config.BaseURL + filePrefix + "/empty.txt"
	// Assembling with zero chunks must NOT silently produce an empty file —
	// the server should reject the MOVE.
	webdavReq(t, env, "MOVE", uploadPrefix+"/empty/.file", "",
		alice.Auth, cogtest.Header("Destination", dest)).
		Status(400)
}
