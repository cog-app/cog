package web_test

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// sharingPrefix returns the OCS sharing API prefix.
const sharingPrefix = "/ocs/v2.php/apps/files_sharing/api/v1"

func putFile(t testing.TB, env *cogtest.Env, user *cogtest.User, path, content string) {
	t.Helper()
	req := cogtest.NewRequest(t, "PUT",
		env.Providers.Config.BaseURL+"/remote.php/dav/files/"+user.ID+"/"+path,
		strings.NewReader(content))
	env.Do(t, req, user.Auth).Status(201)
}

func ocsGet(t testing.TB, env *cogtest.Env, path string, opts ...cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	return env.Get(t, path+"?format=json", opts...)
}

func ocsPost(t testing.TB, env *cogtest.Env, path string, form url.Values, opts ...cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	form.Set("format", "json")
	return env.PostForm(t, path+"?format=json", form, opts...)
}

func ocsPut(t testing.TB, env *cogtest.Env, path string, form url.Values, opts ...cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := cogtest.NewRequest(t, "PUT", env.Providers.Config.BaseURL+path+"?format=json", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return env.Do(t, req, opts...)
}

func ocsDelete(t testing.TB, env *cogtest.Env, path string, opts ...cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	return env.Delete(t, path+"?format=json", opts...)
}

// --- OCS Sharing API ---

func TestOCSShareCreateAndList(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	putFile(t, env, alice, "doc.txt", "hello")

	// Create a share with bob.
	resp := ocsPost(t, env, sharingPrefix+"/shares", url.Values{
		"path":        {"/doc.txt"},
		"shareType":   {"0"}, // USER
		"shareWith":   {"bob"},
		"permissions": {"1"}, // read
	}, alice.Auth)
	resp.Status(200)

	var createResp struct {
		OCS struct {
			Data struct {
				ID        int64  `json:"id"`
				ShareType int    `json:"share_type"`
				ShareWith string `json:"share_with"`
				Path      string `json:"path"`
			} `json:"data"`
		} `json:"ocs"`
	}
	resp.JSON(&createResp)
	require.True(t, createResp.OCS.Data.ID > 0)
	require.Equal(t, 0, createResp.OCS.Data.ShareType)
	require.Equal(t, "bob", createResp.OCS.Data.ShareWith)

	// List shares owned by alice.
	var listResp struct {
		OCS struct {
			Data []struct {
				ID   int64  `json:"id"`
				Path string `json:"path"`
			} `json:"data"`
		} `json:"ocs"`
	}
	ocsGet(t, env, sharingPrefix+"/shares", alice.Auth).
		Status(200).JSON(&listResp)
	require.Equal(t, 1, len(listResp.OCS.Data))

	// List shares shared with bob.
	var sharedWithMe struct {
		OCS struct {
			Data []struct {
				ID int64 `json:"id"`
			} `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, sharingPrefix+"/shares?format=json&shared_with_me=true", bob.Auth).
		Status(200).JSON(&sharedWithMe)
	require.Equal(t, 1, len(sharedWithMe.OCS.Data))
}

func TestOCSShareGetByID(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	_ = bob

	putFile(t, env, alice, "file.txt", "data")

	var created struct {
		OCS struct {
			Data struct {
				ID int64 `json:"id"`
			} `json:"data"`
		} `json:"ocs"`
	}
	ocsPost(t, env, sharingPrefix+"/shares", url.Values{
		"path":      {"/file.txt"},
		"shareType": {"0"},
		"shareWith": {"bob"},
	}, alice.Auth).Status(200).JSON(&created)

	// Get by NC integer ID.
	var got struct {
		OCS struct {
			Data struct {
				ID        int64  `json:"id"`
				ShareWith string `json:"share_with"`
			} `json:"data"`
		} `json:"ocs"`
	}
	ocsGet(t, env, sharingPrefix+"/shares/"+itoa(created.OCS.Data.ID), alice.Auth).
		Status(200).JSON(&got)
	require.Equal(t, created.OCS.Data.ID, got.OCS.Data.ID)
	require.Equal(t, "bob", got.OCS.Data.ShareWith)
}

func TestOCSShareUpdate(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	_ = bob

	putFile(t, env, alice, "edit.txt", "content")

	var created struct {
		OCS struct {
			Data struct {
				ID          int64 `json:"id"`
				Permissions int   `json:"permissions"`
			} `json:"data"`
		} `json:"ocs"`
	}
	ocsPost(t, env, sharingPrefix+"/shares", url.Values{
		"path":        {"/edit.txt"},
		"shareType":   {"0"},
		"shareWith":   {"bob"},
		"permissions": {"1"}, // read only
	}, alice.Auth).Status(200).JSON(&created)
	require.Equal(t, 1, created.OCS.Data.Permissions) // NC read=1

	// Update permissions to read+write (NC: 1|2|4|8 = 15).
	var updated struct {
		OCS struct {
			Data struct {
				Permissions int `json:"permissions"`
			} `json:"data"`
		} `json:"ocs"`
	}
	ocsPut(t, env, sharingPrefix+"/shares/"+itoa(created.OCS.Data.ID),
		url.Values{"permissions": {"15"}}, alice.Auth).
		Status(200).JSON(&updated)
	require.Equal(t, 15, updated.OCS.Data.Permissions)
}

func TestOCSShareUpdateForbidden(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	putFile(t, env, alice, "owned.txt", "mine")

	var created struct {
		OCS struct {
			Data struct {
				ID int64 `json:"id"`
			} `json:"data"`
		} `json:"ocs"`
	}
	ocsPost(t, env, sharingPrefix+"/shares", url.Values{
		"path":      {"/owned.txt"},
		"shareType": {"0"},
		"shareWith": {"bob"},
	}, alice.Auth).Status(200).JSON(&created)

	// Bob tries to update alice's share — should get 403.
	ocsPut(t, env, sharingPrefix+"/shares/"+itoa(created.OCS.Data.ID),
		url.Values{"permissions": {"15"}}, bob.Auth).Status(http.StatusForbidden)
}

func TestOCSShareDelete(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	_ = bob

	putFile(t, env, alice, "del.txt", "bye")

	var created struct {
		OCS struct {
			Data struct {
				ID int64 `json:"id"`
			} `json:"data"`
		} `json:"ocs"`
	}
	ocsPost(t, env, sharingPrefix+"/shares", url.Values{
		"path":      {"/del.txt"},
		"shareType": {"0"},
		"shareWith": {"bob"},
	}, alice.Auth).Status(200).JSON(&created)

	ocsDelete(t, env, sharingPrefix+"/shares/"+itoa(created.OCS.Data.ID), alice.Auth).
		Status(200)

	// Should be gone.
	ocsGet(t, env, sharingPrefix+"/shares/"+itoa(created.OCS.Data.ID), alice.Auth).
		Status(http.StatusNotFound)
}

func TestOCSShareDeleteForbidden(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	putFile(t, env, alice, "keep.txt", "keep")

	var created struct {
		OCS struct {
			Data struct {
				ID int64 `json:"id"`
			} `json:"data"`
		} `json:"ocs"`
	}
	ocsPost(t, env, sharingPrefix+"/shares", url.Values{
		"path":      {"/keep.txt"},
		"shareType": {"0"},
		"shareWith": {"bob"},
	}, alice.Auth).Status(200).JSON(&created)

	// Bob cannot delete alice's share.
	ocsDelete(t, env, sharingPrefix+"/shares/"+itoa(created.OCS.Data.ID), bob.Auth).
		Status(http.StatusForbidden)
}

func TestOCSSharePublicLink(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	putFile(t, env, alice, "public.txt", "public content")

	var created struct {
		OCS struct {
			Data struct {
				ID        int64   `json:"id"`
				ShareType int     `json:"share_type"`
				Token     *string `json:"token"`
				URL       string  `json:"url"`
			} `json:"data"`
		} `json:"ocs"`
	}
	ocsPost(t, env, sharingPrefix+"/shares", url.Values{
		"path":      {"/public.txt"},
		"shareType": {"3"}, // PUBLIC
	}, alice.Auth).Status(200).JSON(&created)

	require.Equal(t, 3, created.OCS.Data.ShareType)
	require.NotNil(t, created.OCS.Data.Token)
	require.True(t, len(*created.OCS.Data.Token) > 0)
	require.True(t, strings.Contains(created.OCS.Data.URL, "/s/"))
}

func TestOCSSharees(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	_ = env.CreateUser(t, "bob", "pass")

	var resp struct {
		OCS struct {
			Data struct {
				Users []struct {
					Label string `json:"label"`
				} `json:"users"`
				Exact struct {
					Users []struct {
						Label string `json:"label"`
					} `json:"users"`
				} `json:"exact"`
			} `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, sharingPrefix+"/sharees?format=json&search=bob", alice.Auth).
		Status(200).JSON(&resp)

	// bob should be in exact match (exact username match).
	require.Equal(t, 1, len(resp.OCS.Data.Exact.Users))
	require.Equal(t, "bob", resp.OCS.Data.Exact.Users[0].Label)
}

func TestOCSShareesRecommended(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	_ = env.CreateUser(t, "bob", "pass")

	var resp struct {
		OCS struct {
			Data struct {
				Exact struct {
					Users []struct {
						Label string `json:"label"`
					} `json:"users"`
				} `json:"exact"`
			} `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, sharingPrefix+"/sharees_recommended?format=json", alice.Auth).
		Status(200).JSON(&resp)

	// No recent shares, so falls back to listing users. bob should appear.
	require.True(t, len(resp.OCS.Data.Exact.Users) >= 1)
}

func TestOCSShareCreateMissingPath(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	ocsPost(t, env, sharingPrefix+"/shares", url.Values{
		"shareType": {"0"},
		"shareWith": {"bob"},
	}, alice.Auth).Status(http.StatusBadRequest)
}

func TestOCSShareCreateFileNotFound(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	_ = env.CreateUser(t, "bob", "pass")

	ocsPost(t, env, sharingPrefix+"/shares", url.Values{
		"path":      {"/nonexistent.txt"},
		"shareType": {"0"},
		"shareWith": {"bob"},
	}, alice.Auth).Status(http.StatusNotFound)
}

func TestOCSShareUnauthenticated(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	ocsGet(t, env, sharingPrefix+"/shares").Status(401)
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }

// TestOCSGroupShare creates a group share the way a NextCloud client does:
// it finds the group through sharees and shares with the identifier sharees
// offered. A member of the group sees the share; someone outside it doesn't.
func TestOCSGroupShare(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	carol := env.CreateUser(t, "carol", "pass")
	dave := env.CreateUser(t, "dave", "pass")

	grp, err := env.Providers.Groups.Create("editors", nil, &alice.ID)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, carol.ID, group.User))

	putFile(t, env, alice, "plan.txt", "draft")
	putFile(t, env, alice, "notes.txt", "draft")

	var sharees struct {
		OCS struct {
			Data struct {
				Exact struct {
					Groups []struct {
						Label string `json:"label"`
						Value struct {
							ShareType int    `json:"shareType"`
							ShareWith string `json:"shareWith"`
						} `json:"value"`
					} `json:"groups"`
				} `json:"exact"`
			} `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, sharingPrefix+"/sharees?format=json&search=editors", alice.Auth).
		Status(200).JSON(&sharees)
	require.Equal(t, 1, len(sharees.OCS.Data.Exact.Groups))
	offered := sharees.OCS.Data.Exact.Groups[0].Value
	require.Equal(t, 1, offered.ShareType)

	type ocsShare struct {
		ID                   int64  `json:"id"`
		ShareType            int    `json:"share_type"`
		ShareWith            string `json:"share_with"`
		ShareWithDisplayname string `json:"share_with_displayname"`
		Path                 string `json:"path"`
	}
	var created struct {
		OCS struct {
			Data ocsShare `json:"data"`
		} `json:"ocs"`
	}
	ocsPost(t, env, sharingPrefix+"/shares", url.Values{
		"path":      {"/plan.txt"},
		"shareType": {fmt.Sprint(offered.ShareType)},
		"shareWith": {offered.ShareWith},
	}, alice.Auth).Status(200).JSON(&created)
	require.Equal(t, 1, created.OCS.Data.ShareType)
	require.Equal(t, grp.ID, created.OCS.Data.ShareWith)
	require.Equal(t, "editors", created.OCS.Data.ShareWithDisplayname)

	// A client that sends the group's name, as NextCloud's gid, works too.
	ocsPost(t, env, sharingPrefix+"/shares", url.Values{
		"path":      {"/notes.txt"},
		"shareType": {"1"},
		"shareWith": {"editors"},
	}, alice.Auth).Status(200)

	// An unknown group is a 404, not a share with nobody.
	ocsPost(t, env, sharingPrefix+"/shares", url.Values{
		"path":      {"/plan.txt"},
		"shareType": {"1"},
		"shareWith": {"no-such-group"},
	}, alice.Auth).Status(404)

	var carolList struct {
		OCS struct {
			Data []ocsShare `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, sharingPrefix+"/shares?format=json&shared_with_me=true", carol.Auth).
		Status(200).JSON(&carolList)
	require.Equal(t, 2, len(carolList.OCS.Data))
	paths := map[string]bool{}
	for _, s := range carolList.OCS.Data {
		require.Equal(t, 1, s.ShareType)
		require.Equal(t, "editors", s.ShareWithDisplayname)
		paths[s.Path] = true
	}
	require.True(t, paths["/plan.txt"] && paths["/notes.txt"], paths)

	ocsGet(t, env, fmt.Sprintf("%s/shares/%d", sharingPrefix, created.OCS.Data.ID), carol.Auth).Status(200)

	var daveList struct {
		OCS struct {
			Data []ocsShare `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, sharingPrefix+"/shares?format=json&shared_with_me=true", dave.Auth).
		Status(200).JSON(&daveList)
	require.Equal(t, 0, len(daveList.OCS.Data))
	ocsGet(t, env, fmt.Sprintf("%s/shares/%d", sharingPrefix, created.OCS.Data.ID), dave.Auth).Status(404)
}
