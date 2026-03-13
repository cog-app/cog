package web_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestSetQuota(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")

	vol, err := env.Providers.Volumes.Create("testvol", "", "file:///data/test", nil, "{}")
	require.NoError(t, err)

	sess, err := env.Providers.Sessions.Create(admin.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	env.PostForm(t, "/admin/volumes/"+vol.ID+"/quotas", url.Values{
		"csrf_token":   {csrf},
		"subject_type": {"DEFAULT"},
		"subject_id":   {""},
		"amount":       {"10"},
		"unit":         {"MB"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	q, err := env.Providers.Quotas.Get(vol.ID, "DEFAULT", "")
	require.NoError(t, err)
	require.NotNil(t, q)
	require.Equal(t, int64(10*1024*1024), q.QuotaBytes)
}

func TestDeleteQuota(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")

	vol, err := env.Providers.Volumes.Create("testvol", "", "file:///data/test", nil, "{}")
	require.NoError(t, err)

	require.NoError(t, env.Providers.Quotas.Set(vol.ID, "DEFAULT", "", 2048))
	q, err := env.Providers.Quotas.Get(vol.ID, "DEFAULT", "")
	require.NoError(t, err)

	sess, err := env.Providers.Sessions.Create(admin.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	env.Delete(t, "/admin/volumes/"+vol.ID+"/quotas/"+q.ID, cookie, cogtest.Header("X-CSRF-Token", csrf)).
		Status(http.StatusOK)

	q, err = env.Providers.Quotas.Get(vol.ID, "DEFAULT", "")
	require.NoError(t, err)
	require.Nil(t, q)
}

func TestOCSQuotaAPI(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	// Without any quota set, should return unlimited (-3 in NC terms).
	body := env.Get(t, "/ocs/v1.php/cloud/user?format=json", u.Auth).
		Status(http.StatusOK).ReadBody()

	var resp struct {
		OCS struct {
			Data struct {
				ID    string `json:"id"`
				Quota struct {
					Quota int64 `json:"quota"`
				} `json:"quota"`
			} `json:"data"`
		} `json:"ocs"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	require.Equal(t, "alice", resp.OCS.Data.ID)
	// quota=-3 means unlimited in NC API.
	require.Equal(t, int64(-3), resp.OCS.Data.Quota.Quota)
}
