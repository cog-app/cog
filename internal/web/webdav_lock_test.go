package web_test

import (
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

const exclusiveLock = `<?xml version="1.0" encoding="utf-8"?>
<D:lockinfo xmlns:D="DAV:">
  <D:lockscope><D:exclusive/></D:lockscope>
  <D:locktype><D:write/></D:locktype>
  <D:owner><D:href>alice's laptop</D:href></D:owner>
</D:lockinfo>`

const sharedLock = `<?xml version="1.0" encoding="utf-8"?>
<lockinfo xmlns="DAV:"><lockscope><shared/></lockscope><locktype><write/></locktype></lockinfo>`

// lock takes a lock and returns its token, without the angle brackets.
func lock(t *testing.T, env *cogtest.Env, u cogtest.HTTPOpt, path, body string, want int, opts ...cogtest.HTTPOpt) string {
	t.Helper()
	resp := webdavReq(t, env, "LOCK", path, body, append([]cogtest.HTTPOpt{u}, opts...)...).Status(want)
	return strings.Trim(resp.Header.Get("Lock-Token"), "<>")
}

func ifToken(token string) cogtest.HTTPOpt {
	return cogtest.Header("If", "(<"+token+">)")
}

func etagOf(t *testing.T, env *cogtest.Env, u cogtest.HTTPOpt, path string) string {
	t.Helper()
	etag := webdavReq(t, env, "HEAD", path, "", u).Status(200).Header.Get("ETag")
	require.NotEmpty(t, etag)
	return etag
}

// If-Match is how a sync client avoids overwriting an edit it has not seen;
// it was ignored, so the last upload won.
func TestWebDAV_ConditionalWrites(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	f := "/remote.php/dav/files/" + alice.ID + "/doc.txt"

	webdavReq(t, env, "PUT", f, "v1", alice.Auth).Status(201)
	v1 := etagOf(t, env, alice.Auth, f)
	webdavReq(t, env, "PUT", f, "v2", alice.Auth, cogtest.Header("If-Match", v1)).Status(201)

	// A client still holding v1's tag is refused, and v2 is kept.
	webdavReq(t, env, "PUT", f, "stale", alice.Auth, cogtest.Header("If-Match", v1)).Status(412)
	env.Get(t, f, alice.Auth).Status(200).Equals("v2")
	webdavReq(t, env, "DELETE", f, "", alice.Auth, cogtest.Header("If-Match", v1)).Status(412)
	webdavReq(t, env, "MOVE", f, "", alice.Auth, cogtest.Header("If-Match", v1),
		cogtest.Header("Destination", "/remote.php/dav/files/"+alice.ID+"/moved.txt")).Status(412)

	// If-None-Match: * creates only.
	webdavReq(t, env, "PUT", f, "clobber", alice.Auth, cogtest.Header("If-None-Match", "*")).Status(412)
	webdavReq(t, env, "PUT", f+".new", "new", alice.Auth, cogtest.Header("If-None-Match", "*")).Status(201)
	webdavReq(t, env, "PUT", f+".gone", "x", alice.Auth, cogtest.Header("If-Match", "*")).Status(412)

	// The WebDAV If header, as the desktop client sends it: an entity tag
	// tagged with the file's URL.
	v2 := etagOf(t, env, alice.Auth, f)
	tagged := func(etag string) cogtest.HTTPOpt {
		return cogtest.Header("If", "<"+env.Providers.Config.BaseURL+f+"> (["+etag+"])")
	}
	webdavReq(t, env, "PUT", f, "stale", alice.Auth, tagged(v1)).Status(412)
	webdavReq(t, env, "PUT", f, "v3", alice.Auth, tagged(v2)).Status(201)
	webdavReq(t, env, "PUT", f, "x", alice.Auth, cogtest.Header("If", "(<broken")).Status(400)

	webdavReq(t, env, "DELETE", f, "", alice.Auth, cogtest.Header("If-Match", etagOf(t, env, alice.Auth, f))).Status(204)
}

func TestWebDAV_ChunkedUploadIfHeader(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	f := "/remote.php/dav/files/" + alice.ID + "/big.bin"
	webdavReq(t, env, "PUT", f, "old", alice.Auth).Status(201)
	stale := etagOf(t, env, alice.Auth, f)
	webdavReq(t, env, "PUT", f, "edited elsewhere", alice.Auth).Status(201)

	up := "/remote.php/dav/uploads/" + alice.ID + "/t1"
	webdavReq(t, env, "MKCOL", up, "", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", up+"/1", "new", alice.Auth).Status(201)
	dest := cogtest.Header("Destination", env.Providers.Config.BaseURL+f)
	webdavReq(t, env, "MOVE", up+"/.file", "", alice.Auth, dest,
		cogtest.Header("If", "<"+env.Providers.Config.BaseURL+f+"> (["+stale+"])")).Status(412)
	env.Get(t, f, alice.Auth).Status(200).Equals("edited elsewhere")

	current := etagOf(t, env, alice.Auth, f)
	webdavReq(t, env, "MOVE", up+"/.file", "", alice.Auth, dest,
		cogtest.Header("If", "<"+env.Providers.Config.BaseURL+f+"> (["+current+"])")).Status(201)
	env.Get(t, f, alice.Auth).Status(200).Equals("new")
}

func TestWebDAV_Lock(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID
	f := prefix + "/report.docx"
	webdavReq(t, env, "PUT", f, "draft", alice.Auth).Status(201)

	resp := webdavReq(t, env, "LOCK", f, exclusiveLock, alice.Auth, cogtest.Header("Timeout", "Second-600")).Status(200)
	token := strings.Trim(resp.Header.Get("Lock-Token"), "<>")
	require.True(t, strings.HasPrefix(token, "opaquelocktoken:"))
	body := resp.ReadBody()
	require.Contains(t, body, "<D:exclusive></D:exclusive>")
	require.Contains(t, body, "Second-600")
	require.Contains(t, body, `<href xmlns="DAV:">alice&#39;s laptop</href>`)
	require.Contains(t, body, "<D:lockroot><D:href>"+prefix+"/report.docx</D:href></D:lockroot>")

	// Without the token, even its owner's other client may not write.
	locked := webdavReq(t, env, "PUT", f, "clobber", alice.Auth).Status(423)
	require.Contains(t, locked.ReadBody(), "<d:lock-token-submitted><d:href>"+prefix+"/report.docx</d:href>")
	webdavReq(t, env, "DELETE", f, "", alice.Auth).Status(423)
	webdavReq(t, env, "MOVE", f, "", alice.Auth, cogtest.Header("Destination", prefix+"/x.docx")).Status(423)
	webdavReq(t, env, "PROPPATCH", f, `<propertyupdate xmlns="DAV:" xmlns:oc="http://owncloud.org/ns"><set><prop><oc:favorite>1</oc:favorite></prop></set></propertyupdate>`, alice.Auth).Status(423)
	webdavReq(t, env, "PUT", f, "wrong", alice.Auth, ifToken("opaquelocktoken:nope")).Status(412)
	lock(t, env, alice.Auth, f, exclusiveLock, 423)
	lock(t, env, alice.Auth, f, sharedLock, 423)
	env.Get(t, f, alice.Auth).Status(200).Equals("draft")

	// With it, it may.
	webdavReq(t, env, "PUT", f, "saved", alice.Auth, ifToken(token)).Status(201)
	env.Get(t, f, alice.Auth).Status(200).Equals("saved")

	// PROPFIND shows the lock, and what locks are supported.
	pf := webdavReq(t, env, "PROPFIND", prefix+"/", "", alice.Auth, cogtest.Header("Depth", "1")).Status(207).ReadBody()
	require.Contains(t, pf, "<D:supportedlock>")
	require.Contains(t, pf, "<D:href>"+token+"</D:href>")

	// A refresh restarts the timeout.
	env.Clock.Advance(9 * time.Minute)
	webdavReq(t, env, "LOCK", f, "", alice.Auth, ifToken(token), cogtest.Header("Timeout", "Second-600")).Status(200).Contains("Second-600")
	env.Clock.Advance(9 * time.Minute)
	webdavReq(t, env, "PUT", f, "clobber", alice.Auth).Status(423)
	webdavReq(t, env, "LOCK", f, "", alice.Auth, ifToken("opaquelocktoken:nope")).Status(412)

	// UNLOCK needs the token, and frees the file.
	webdavReq(t, env, "UNLOCK", f, "", alice.Auth, cogtest.Header("Lock-Token", "<opaquelocktoken:nope>")).Status(409)
	webdavReq(t, env, "UNLOCK", f, "", alice.Auth, cogtest.Header("Lock-Token", "<"+token+">")).Status(204)
	webdavReq(t, env, "PUT", f, "free", alice.Auth).Status(201)

	// A forgotten lock lapses.
	lock(t, env, alice.Auth, f, exclusiveLock, 200, cogtest.Header("Timeout", "Second-60"))
	env.Clock.Advance(61 * time.Second)
	webdavReq(t, env, "PUT", f, "after", alice.Auth).Status(201)

	// Shared locks share; each one's token is enough.
	t1 := lock(t, env, alice.Auth, f, sharedLock, 200)
	lock(t, env, alice.Auth, f, sharedLock, 200)
	lock(t, env, alice.Auth, f, exclusiveLock, 423)
	webdavReq(t, env, "PUT", f, "shared", alice.Auth, ifToken(t1)).Status(201)

	// Lock tokens are a user's own.
	bob := env.CreateUser(t, "bob", "pass")
	webdavReq(t, env, "PUT", "/remote.php/dav/files/"+bob.ID+"/report.docx", "bob's", bob.Auth).Status(201)
}

// Editors lock a name before they first save to it.
func TestWebDAV_LockNewFile(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	token := lock(t, env, alice.Auth, prefix+"/new.odt", exclusiveLock, 201)
	env.Get(t, prefix+"/new.odt", alice.Auth).Status(200).Equals("")
	webdavReq(t, env, "PUT", prefix+"/new.odt", "x", alice.Auth).Status(423)
	webdavReq(t, env, "PUT", prefix+"/new.odt", "content", alice.Auth, ifToken(token)).Status(201)

	lock(t, env, alice.Auth, prefix+"/no/such/folder.odt", exclusiveLock, 409)
}

func TestWebDAV_LockFolder(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID
	webdavReq(t, env, "MKCOL", prefix+"/proj", "", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", prefix+"/proj/a.txt", "a", alice.Auth).Status(201)

	// Depth infinity covers everything inside.
	deep := lock(t, env, alice.Auth, prefix+"/proj", exclusiveLock, 200)
	webdavReq(t, env, "PUT", prefix+"/proj/a.txt", "x", alice.Auth).Status(423)
	webdavReq(t, env, "PUT", prefix+"/proj/b.txt", "x", alice.Auth).Status(423)
	webdavReq(t, env, "MKCOL", prefix+"/proj/sub", "", alice.Auth).Status(423)
	webdavReq(t, env, "PUT", prefix+"/proj/a.txt", "x", alice.Auth, ifToken(deep)).Status(201)
	// Its children cannot be locked apart from it.
	lock(t, env, alice.Auth, prefix+"/proj/a.txt", exclusiveLock, 423)
	webdavReq(t, env, "UNLOCK", prefix+"/proj/a.txt", "", alice.Auth, cogtest.Header("Lock-Token", "<"+deep+">")).Status(204)

	// Depth 0 protects what the folder holds, not the files themselves.
	shallow := lock(t, env, alice.Auth, prefix+"/proj", exclusiveLock, 200, cogtest.Header("Depth", "0"))
	webdavReq(t, env, "PUT", prefix+"/proj/a.txt", "edit", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", prefix+"/proj/c.txt", "new", alice.Auth).Status(423)
	webdavReq(t, env, "DELETE", prefix+"/proj/a.txt", "", alice.Auth).Status(423)
	webdavReq(t, env, "PUT", prefix+"/proj/c.txt", "new", alice.Auth, ifToken(shallow)).Status(201)
	webdavReq(t, env, "UNLOCK", prefix+"/proj", "", alice.Auth, cogtest.Header("Lock-Token", "<"+shallow+">")).Status(204)

	// A folder holding a locked file is deleted or moved only with its token.
	child := lock(t, env, alice.Auth, prefix+"/proj/a.txt", exclusiveLock, 200)
	webdavReq(t, env, "DELETE", prefix+"/proj", "", alice.Auth).Status(423)
	webdavReq(t, env, "MOVE", prefix+"/proj", "", alice.Auth, cogtest.Header("Destination", prefix+"/proj2")).Status(423)
	webdavReq(t, env, "MOVE", prefix+"/proj", "", alice.Auth, ifToken(child),
		cogtest.Header("Destination", prefix+"/proj2")).Status(201)

	// The lock does not move with the file, and is gone from where it was.
	webdavReq(t, env, "PUT", prefix+"/proj2/a.txt", "x", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", prefix+"/proj/a.txt", "x", alice.Auth).Status(201)
}

func TestWebDAV_LockOptions(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	resp := webdavReq(t, env, "OPTIONS", "/remote.php/dav/files/"+alice.ID+"/", "", alice.Auth).Status(200)
	require.Contains(t, resp.Header.Get("DAV"), "2")
	require.Contains(t, resp.Header.Get("Allow"), "LOCK")
	require.Equal(t, "DAV", resp.Header.Get("MS-Author-Via"))

	// A request that says it holds no lock is fine where there is none.
	webdavReq(t, env, "PUT", "/remote.php/dav/files/"+alice.ID+"/x", "x", alice.Auth,
		cogtest.Header("If", "(Not <DAV:no-lock>)")).Status(201)
}

// A MOVE onto a locked file needs both locks' tokens, which clients send
// together in one untagged list.
func TestWebDAV_MoveOntoLocked(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID
	webdavReq(t, env, "PUT", prefix+"/a.txt", "a", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", prefix+"/b.txt", "b", alice.Auth).Status(201)
	ta := lock(t, env, alice.Auth, prefix+"/a.txt", exclusiveLock, 200)
	tb := lock(t, env, alice.Auth, prefix+"/b.txt", exclusiveLock, 200)
	dest := cogtest.Header("Destination", prefix+"/b.txt")

	webdavReq(t, env, "MOVE", prefix+"/a.txt", "", alice.Auth, dest, ifToken(ta)).Status(423)
	webdavReq(t, env, "MOVE", prefix+"/a.txt", "", alice.Auth, dest,
		cogtest.Header("If", "(<"+ta+"> <"+tb+">)")).Status(204)
	env.Get(t, prefix+"/b.txt", alice.Auth).Status(200).Equals("a")
	// Both locks went with what they covered.
	webdavReq(t, env, "PUT", prefix+"/b.txt", "free", alice.Auth).Status(201)
}
