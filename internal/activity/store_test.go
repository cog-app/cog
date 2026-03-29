package activity_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestActivityLog(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	err := env.Providers.Activities.Log("alice", "alice", "files", "file_created", "alice uploaded test.txt", "files", "1", "/test.txt")
	require.NoError(t, err)

	err = env.Providers.Activities.Log("alice", "bob", "sharing", "share_created", "bob shared a file with alice", "files", "2", "/shared.txt")
	require.NoError(t, err)

	// List all for alice.
	activities, err := env.Providers.Activities.List("alice", 50, 0)
	require.NoError(t, err)
	require.Equal(t, 2, len(activities))
	// Newest first.
	assert.Equal(t, "share_created", activities[0].Type)
	assert.Equal(t, "file_created", activities[1].Type)
}

func TestActivityListByObject(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	env.Providers.Activities.Log("alice", "alice", "files", "file_created", "created", "files", "42", "/doc.txt")
	env.Providers.Activities.Log("alice", "alice", "files", "file_changed", "renamed", "files", "42", "/doc-renamed.txt")
	env.Providers.Activities.Log("alice", "alice", "files", "file_created", "other file", "files", "99", "/other.txt")

	activities, err := env.Providers.Activities.ListByObject("files", "42", 50)
	require.NoError(t, err)
	require.Equal(t, 2, len(activities))
}

func TestActivityNCAPI(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Log some activities.
	env.Providers.Activities.Log("alice", "alice", "files", "file_created", "alice uploaded report.pdf", "files", "10", "/report.pdf")
	env.Providers.Activities.Log("alice", "bob", "sharing", "share_created", "bob shared with alice", "files", "10", "/report.pdf")

	// Query via NC Activity API.
	resp := env.Get(t, "/ocs/v2.php/apps/activity/api/v2/activity?format=json", alice.Auth)
	resp.Status(200)

	var ocsResp struct {
		OCS struct {
			Data []map[string]any `json:"data"`
		} `json:"ocs"`
	}
	resp.JSON(&ocsResp)
	require.Equal(t, 2, len(ocsResp.OCS.Data))
	assert.Equal(t, "sharing", ocsResp.OCS.Data[0]["app"])
	assert.Equal(t, "files", ocsResp.OCS.Data[1]["app"])
}

func TestActivityFromFileMutations(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Upload a file via WebDAV.
	putReq, _ := http.NewRequest(http.MethodPut,
		env.Providers.Config.BaseURL+"/remote.php/dav/files/"+alice.ID+"/hello.txt",
		strings.NewReader("hello world"))
	putReq.Header.Set("Content-Type", "text/plain")
	env.Do(t, putReq, alice.Auth)

	// Check that an activity was logged.
	activities, err := env.Providers.Activities.List("alice", 50, 0)
	require.NoError(t, err)
	// Should have at least one file_created activity.
	found := false
	for _, a := range activities {
		if a.Type == "file_created" && strings.Contains(a.ObjectName, "hello.txt") {
			found = true
			break
		}
	}
	assert.Equal(t, true, found)
}

func TestActivityRich(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	params := map[string]any{
		"user": map[string]any{"type": "user", "id": "alice", "name": "Alice"},
		"file": map[string]any{"type": "file", "id": "1", "name": "doc.pdf"},
	}
	err := env.Providers.Activities.LogRich("alice", "alice", "files", "file_created",
		"Alice uploaded doc.pdf", "{user} uploaded {file}", params, "files", "1", "/doc.pdf")
	require.NoError(t, err)

	// Verify via NC API.
	resp := env.Get(t, "/ocs/v2.php/apps/activity/api/v2/activity?format=json", alice.Auth)
	resp.Status(200)

	var ocsResp struct {
		OCS struct {
			Data []map[string]any `json:"data"`
		} `json:"ocs"`
	}
	resp.JSON(&ocsResp)
	require.Equal(t, 1, len(ocsResp.OCS.Data))

	// Check subject_rich is an array with template and params.
	rich, ok := ocsResp.OCS.Data[0]["subject_rich"].([]any)
	require.Equal(t, true, ok)
	require.Equal(t, 2, len(rich))
	assert.Equal(t, "{user} uploaded {file}", rich[0])
	// Params should be a map.
	paramsMap, ok := rich[1].(map[string]any)
	require.Equal(t, true, ok)
	_, hasUser := paramsMap["user"]
	assert.Equal(t, true, hasUser)
	_ = json.Marshal // ensure json is imported
}
