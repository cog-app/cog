package bookmarks_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"mime/multipart"
	"strconv"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/bookmarks"
)

// These tests follow what Floccus does when it syncs with NextCloud Bookmarks.

type itemResp struct {
	Status string         `json:"status"`
	Item   map[string]any `json:"item"`
}

type dataResp struct {
	Status string           `json:"status"`
	Data   []map[string]any `json:"data"`
}

func idOf(item map[string]any) string { return itoa(int64(item["id"].(float64))) }

func createFolder(t *testing.T, env *cogtest.Env, u cogtest.HTTPOpt, title string, parent any) string {
	t.Helper()
	var resp itemResp
	doReq(t, env, "POST", bmAPI+"/folder", map[string]any{"title": title, "parent_folder": parent}, u).Status(200).JSON(&resp)
	return idOf(resp.Item)
}

func createBookmark(t *testing.T, env *cogtest.Env, u cogtest.HTTPOpt, url, title string, folders ...any) string {
	t.Helper()
	var resp itemResp
	doReq(t, env, "POST", bmAPI+"/bookmark", map[string]any{"url": url, "title": title, "folders": folders}, u).Status(200).JSON(&resp)
	return idOf(resp.Item)
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestFloccusCapabilities(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")
	var resp struct {
		OCS struct {
			Data struct {
				Capabilities struct {
					Bookmarks struct {
						HashFunctions []string `json:"hash-functions"`
						JS            bool     `json:"javascript-bookmarks"`
					} `json:"bookmarks"`
				} `json:"capabilities"`
			} `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, "/ocs/v2.php/cloud/capabilities?format=json", alice.Auth).Status(200).JSON(&resp)
	require.Equal(t, bookmarks.HashFunctions, resp.OCS.Data.Capabilities.Bookmarks.HashFunctions)
	require.True(t, resp.OCS.Data.Capabilities.Bookmarks.JS)
}

// The lock was missing, and Floccus stopped at it with a 404.
func TestFloccusLock(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	doReq(t, env, "POST", bmAPI+"/lock", nil, alice.Auth).Status(200)
	doReq(t, env, "POST", bmAPI+"/lock", nil, alice.Auth).Status(423)
	// Each user has their own.
	doReq(t, env, "POST", bmAPI+"/lock", nil, bob.Auth).Status(200)

	doReq(t, env, "DELETE", bmAPI+"/lock", nil, alice.Auth).Status(200)
	doReq(t, env, "POST", bmAPI+"/lock", nil, alice.Auth).Status(200)

	// A client that never released it does not hold it for good.
	env.Clock.Advance(bookmarks.SyncLockTTL)
	doReq(t, env, "POST", bmAPI+"/lock", nil, alice.Auth).Status(200)
}

func TestFloccusChildren(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Floccus sends the root as -1, which once failed the parent's foreign key.
	work := createFolder(t, env, alice.Auth, "Work", -1)
	docs := createFolder(t, env, alice.Auth, "Docs", work)
	createBookmark(t, env, alice.Auth, "https://top.example", "Top", -1)
	inWork := createBookmark(t, env, alice.Auth, "https://work.example", "Work Link", work)
	createBookmark(t, env, alice.Auth, "https://docs.example", "Docs Link", docs)

	var one dataResp
	env.Get(t, bmAPI+"/folder/-1/children?layers=1", alice.Auth).Status(200).JSON(&one)
	require.Equal(t, 2, len(one.Data))
	require.Equal(t, "folder", one.Data[0]["type"])
	require.Equal(t, "Work", one.Data[0]["title"])
	// No children field: Floccus reads that as "not loaded yet".
	_, has := one.Data[0]["children"]
	require.False(t, has)
	require.Equal(t, "bookmark", one.Data[1]["type"])
	require.Equal(t, "https://top.example", one.Data[1]["url"])

	var all dataResp
	env.Get(t, bmAPI+"/folder/-1/children?layers=-1", alice.Auth).Status(200).JSON(&all)
	workChildren := all.Data[0]["children"].([]any)
	require.Equal(t, 2, len(workChildren))
	docsItem := workChildren[0].(map[string]any)
	require.Equal(t, "Docs", docsItem["title"])
	require.Equal(t, 1, len(docsItem["children"].([]any)))
	require.Equal(t, inWork, idOf(workChildren[1].(map[string]any)))

	// Floccus sends ids as strings, and reorders folders and bookmarks
	// together.
	doReq(t, env, "PATCH", bmAPI+"/folder/"+work+"/childorder", map[string]any{
		"data": []map[string]any{{"type": "bookmark", "id": inWork}, {"type": "folder", "id": docs}},
	}, alice.Auth).Status(200)
	var order dataResp
	env.Get(t, bmAPI+"/folder/"+work+"/childorder", alice.Auth).Status(200).JSON(&order)
	require.Equal(t, "bookmark", order.Data[0]["type"])
	require.Equal(t, "folder", order.Data[1]["type"])

	// An order that leaves a child out is refused.
	doReq(t, env, "PATCH", bmAPI+"/folder/"+work+"/childorder", map[string]any{
		"data": []map[string]any{{"type": "folder", "id": docs}},
	}, alice.Auth).Status(400)

	// A new child goes last.
	createBookmark(t, env, alice.Auth, "https://last.example", "Last", work)
	env.Get(t, bmAPI+"/folder/"+work+"/children", alice.Auth).Status(200).JSON(&order)
	require.Equal(t, 3, len(order.Data))
	require.Equal(t, "Last", order.Data[2]["title"])

	// The folder list is a hierarchy.
	var folders dataResp
	env.Get(t, bmAPI+"/folder", alice.Auth).Status(200).JSON(&folders)
	require.Equal(t, 1, len(folders.Data))
	require.Equal(t, "Docs", folders.Data[0]["children"].([]any)[0].(map[string]any)["title"])
}

func TestFloccusHash(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	f := createFolder(t, env, alice.Auth, "F/ü", -1)
	createBookmark(t, env, alice.Auth, "https://a.example/x?y=<1>", `A "b"`, f)
	doReq(t, env, "PUT", bmAPI+"/bookmark/"+createBookmark(t, env, alice.Auth, "https://t.example", "T", f), map[string]any{"tags": []string{"z", "a"}}, alice.Auth).Status(200)

	// As Floccus computes it: JSON.stringify, slashes and non-ASCII as is.
	b1 := sha(`{"title":"A \"b\"","url":"https://a.example/x?y=<1>"}`)
	b2 := sha(`{"title":"T","url":"https://t.example"}`)
	folder := sha(`{"title":"F/ü","children":["` + b1 + `","` + b2 + `"]}`)
	root := sha(`{"children":["` + folder + `"]}`)

	var resp struct {
		Data string `json:"data"`
	}
	env.Get(t, bmAPI+"/folder/"+f+"/hash?hashFn=sha256", alice.Auth).Status(200).JSON(&resp)
	require.Equal(t, folder, resp.Data)
	env.Get(t, bmAPI+"/folder/-1/hash", alice.Auth).Status(200).JSON(&resp)
	require.Equal(t, root, resp.Data)

	b2tags := sha(`{"title":"T","url":"https://t.example","tags":["a","z"]}`)
	b1tags := sha(`{"title":"A \"b\"","url":"https://a.example/x?y=<1>","tags":[]}`)
	env.Get(t, bmAPI+"/folder/"+f+"/hash?hashFn=sha256&fields[]=title&fields[]=url&fields[]=tags", alice.Auth).Status(200).JSON(&resp)
	require.Equal(t, sha(`{"title":"F/ü","children":["`+b1tags+`","`+b2tags+`"]}`), resp.Data)

	env.Get(t, bmAPI+"/folder/"+f+"/hash?hashFn=md5", alice.Auth).Status(400)
	env.Get(t, bmAPI+"/folder/999/hash", alice.Auth).Status(404)
}

// A link Floccus has in two folders is created once per folder, and is deleted
// by taking it out of each.
func TestFloccusBookmarkInTwoFolders(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	a := createFolder(t, env, alice.Auth, "A", -1)
	b := createFolder(t, env, alice.Auth, "B", -1)
	id := createBookmark(t, env, alice.Auth, "https://same.example", "Same", a)
	require.Equal(t, id, createBookmark(t, env, alice.Auth, "https://same.example", "Same", b))

	var got itemResp
	env.Get(t, bmAPI+"/bookmark/"+id, alice.Auth).Status(200).JSON(&got)
	require.Equal(t, 2, len(got.Item["folders"].([]any)))

	doReq(t, env, "DELETE", bmAPI+"/folder/"+a+"/bookmarks/"+id, nil, alice.Auth).Status(200)
	env.Get(t, bmAPI+"/bookmark/"+id, alice.Auth).Status(200)
	// Floccus treats a 404 here as already gone.
	doReq(t, env, "DELETE", bmAPI+"/folder/"+a+"/bookmarks/"+id, nil, alice.Auth).Status(404)
	doReq(t, env, "DELETE", bmAPI+"/folder/"+b+"/bookmarks/"+id, nil, alice.Auth).Status(200)
	env.Get(t, bmAPI+"/bookmark/"+id, alice.Auth).Status(404)

	id = createBookmark(t, env, alice.Auth, "https://add.example", "Add", a)
	doReq(t, env, "POST", bmAPI+"/folder/"+b+"/bookmarks/"+id, nil, alice.Auth).Status(200)
	doReq(t, env, "POST", bmAPI+"/folder/999/bookmarks/"+id, nil, alice.Auth).Status(404)
	env.Get(t, bmAPI+"/bookmark/"+id, alice.Auth).Status(200).JSON(&got)
	require.Equal(t, 2, len(got.Item["folders"].([]any)))
}

func TestFloccusUpdates(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	a := createFolder(t, env, alice.Auth, "A", -1)
	b := createFolder(t, env, alice.Auth, "B", a)

	// The URL changes too, which it once silently did not.
	id := createBookmark(t, env, alice.Auth, "https://old.example", "Old", a)
	var got itemResp
	doReq(t, env, "PUT", bmAPI+"/bookmark/"+id, map[string]any{"url": "https://new.example", "title": "New", "folders": []any{b}}, alice.Auth).Status(200).JSON(&got)
	require.Equal(t, "https://new.example", got.Item["url"])
	require.Equal[any](t, []any{float64(mustInt(t, b))}, got.Item["folders"])
	other := createBookmark(t, env, alice.Auth, "https://other.example", "Other", a)
	doReq(t, env, "PUT", bmAPI+"/bookmark/"+other, map[string]any{"url": "https://new.example"}, alice.Auth).Status(400)
	doReq(t, env, "PUT", bmAPI+"/bookmark/999", map[string]any{"title": "x"}, alice.Auth).Status(404)

	// A rename leaves the folder where it is, which it once did not.
	doReq(t, env, "PUT", bmAPI+"/folder/"+b, map[string]any{"title": "B2"}, alice.Auth).Status(200).JSON(&got)
	require.Equal[any](t, float64(mustInt(t, a)), got.Item["parent_folder"])
	// A folder cannot go inside itself.
	doReq(t, env, "PUT", bmAPI+"/folder/"+a, map[string]any{"parent_folder": mustInt(t, b)}, alice.Auth).Status(400)
	doReq(t, env, "PUT", bmAPI+"/folder/"+b, map[string]any{"parent_folder": -1}, alice.Auth).Status(200).JSON(&got)
	require.Equal[any](t, float64(-1), got.Item["parent_folder"])

	// Deleting a folder deletes what was only in it.
	shared := createBookmark(t, env, alice.Auth, "https://shared.example", "Shared", a, b)
	doReq(t, env, "DELETE", bmAPI+"/folder/"+a, nil, alice.Auth).Status(200)
	env.Get(t, bmAPI+"/bookmark/"+other, alice.Auth).Status(404)
	env.Get(t, bmAPI+"/bookmark/"+shared, alice.Auth).Status(200)
}

func TestFloccusImport(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")
	target := createFolder(t, env, alice.Auth, "Target", -1)

	file := `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<DL><p>
<DT><A HREF="https://one.example" TAGS="x,y">One &amp; only</A>
<DT><H3>Sub</H3>
<DL><p>
<DT><A HREF="https://two.example">Two</A>
<DT><H3>Empty</H3>
<DL><p>
</DL><p>
</DL><p>
<DT><A HREF="https://three.example">Three</A>
</DL><p>`
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("bm_import", "bookmarks.html")
	require.NoError(t, err)
	_, err = fw.Write([]byte(file))
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	req := cogtest.NewRequest(t, "POST", env.Providers.Config.BaseURL+bmAPI+"/folder/"+target+"/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	var resp dataResp
	env.Do(t, req, alice.Auth).Status(200).JSON(&resp)
	require.Equal(t, 3, len(resp.Data))
	require.Equal(t, "One & only", resp.Data[0]["title"])
	require.Equal[any](t, []any{"x", "y"}, resp.Data[0]["tags"])
	require.Equal(t, "folder", resp.Data[1]["type"])
	sub := resp.Data[1]["children"].([]any)
	require.Equal(t, 2, len(sub))
	require.Equal(t, "Two", sub[0].(map[string]any)["title"])
	require.Equal(t, "Empty", sub[1].(map[string]any)["title"])
	require.Equal(t, 0, len(sub[1].(map[string]any)["children"].([]any)))
	require.Equal(t, "Three", resp.Data[2]["title"])

	var children dataResp
	env.Get(t, bmAPI+"/folder/"+target+"/children", alice.Auth).Status(200).JSON(&children)
	require.Equal(t, 3, len(children.Data))
}

// The web UI keeps a deleted folder's bookmarks, at the root, rather than
// leaving them in a folder that is gone.
func TestDeleteFolderKeepsBookmarks(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")
	plug, _ := env.Providers.Plugins.Plugin("bookmarks")
	db, err := plug.(*bookmarks.Plugin).DB(alice.Username)
	require.NoError(t, err)

	f, err := bookmarks.CreateFolder(db, "F", nil)
	require.NoError(t, err)
	sub, err := bookmarks.CreateFolder(db, "Sub", &f.ID)
	require.NoError(t, err)
	bm, err := bookmarks.CreateBookmark(db, "https://kept.example", "Kept", "", nil, []int64{sub.ID})
	require.NoError(t, err)
	require.NoError(t, bookmarks.DeleteFolder(db, f.ID))

	got, err := bookmarks.GetBookmark(db, bm.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{bookmarks.RootFolder}, got.Folders)
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	require.NoError(t, err)
	return n
}
