package compat_test

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// webdavReq is local to this file because the helper in internal/web/webdav_test.go
// isn't exported.
func webdavReq(t testing.TB, env *cogtest.Env, method, path string, body string, opts ...cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	req := cogtest.NewRequest(t, method, env.Providers.Config.BaseURL+path, strings.NewReader(body))
	return env.Do(t, req, opts...)
}

// TestPROPPATCH_EscapesUserControlledHref verifies audit security.md #14:
// the PROPPATCH response writes the file path into a <d:href> element via
// fmt.Fprintf. A filename containing "<" or "&" must be XML-escaped, not
// inserted raw.
func TestPROPPATCH_EscapesUserControlledHref(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	// Create a file whose name contains characters that would break XML.
	name := "weird<&\".txt"
	webdavReq(t, env, "PUT", prefix+"/"+url.PathEscape(name), "x", alice.Auth).Status(201)

	body := `<?xml version="1.0" encoding="UTF-8"?>
<propertyupdate xmlns="DAV:" xmlns:oc="http://owncloud.org/ns">
  <set><prop><oc:favorite>1</oc:favorite></prop></set>
</propertyupdate>`

	resp := webdavReq(t, env, "PROPPATCH", prefix+"/"+url.PathEscape(name), body, alice.Auth)
	resp.Status(207)
	out := resp.ReadBody()

	// The literal "<&" must NOT appear unescaped inside the response (only
	// inside XML attribute values it's fine, but in element content it'd
	// break parsing). Assert the escaped form is present.
	require.True(t, strings.Contains(out, "weird&lt;&amp;"))
	// And no raw "weird<&" sequence.
	require.False(t, strings.Contains(out, "weird<&"))
}

// A LOCK response names the lock root, a path the client chose; it must come
// back as an escaped href, not raw markup.
func TestLOCK_EscapesLockRoot(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	name := "lock<&me.txt"
	webdavReq(t, env, "PUT", prefix+"/"+url.PathEscape(name), "x", alice.Auth).Status(201)

	lockBody := `<?xml version="1.0"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`
	resp := webdavReq(t, env, "LOCK", prefix+"/"+url.PathEscape(name), lockBody, alice.Auth)
	resp.Status(http.StatusOK)
	out := resp.ReadBody()

	require.True(t, strings.Contains(out, "lock%3C&amp;me.txt"))
	require.False(t, strings.Contains(out, "lock<&me.txt"))
	require.NoError(t, xml.Unmarshal([]byte(out), new(struct{})))
}
