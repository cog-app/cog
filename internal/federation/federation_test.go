package federation_test

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/federation"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestFederationOCMDiscovery(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	resp := env.Get(t, "/.well-known/ocm-provider")
	resp.Status(200)

	var provider federation.OCMProviderResponse
	resp.JSON(&provider)

	assert.Equal(t, true, provider.Enabled)
	assert.Equal(t, "1.1", provider.APIVersion)
	require.Equal(t, 1, len(provider.ResourceTypes))
	assert.Equal(t, "file", provider.ResourceTypes[0].Name)
}

func TestFederationInboundShare(t *testing.T) {
	// Set up two Cog instances.
	sender := cogtest.New(t)
	receiver := cogtest.New(t)

	alice := sender.CreateUser(t, "alice", "pass")
	_ = alice
	bob := receiver.CreateUser(t, "bob", "pass")

	// Simulate: sender's server sends an OCM share to receiver's /ocm/shares.
	ocmPayload := federation.OCMShare{
		ShareWith:        "bob",
		Name:             "SharedFolder",
		ProviderID:       "test-provider-123",
		Owner:            "alice@" + stripScheme(sender.Providers.Config.BaseURL),
		OwnerDisplayName: "Alice",
		ShareType:        "user",
		ResourceType:     "file",
		Protocol: federation.OCMProtocol{
			Name: "webdav",
			Options: federation.OCMProtocolOptions{
				SharedSecret: "secret-token-abc",
				Permissions:  "1",
				WebDAVURL:    sender.Providers.Config.BaseURL + "/public.php/webdav/",
			},
		},
	}

	// POST to receiver's OCM endpoint.
	resp := receiver.PostJSON(t, "/ocm/shares", ocmPayload)
	resp.Status(201)

	var result map[string]string
	resp.JSON(&result)
	require.NotEqual(t, "", result["id"])

	// Verify the share appears as pending for bob.
	var pending []federation.FederatedShare
	for fs, err := range receiver.Providers.Federation.ListInbound(bob.ID, "pending") {
		require.NoError(t, err)
		pending = append(pending, fs)
	}
	require.Equal(t, 1, len(pending))
	assert.Equal(t, "SharedFolder", pending[0].ResourceName)
	assert.Equal(t, "alice", pending[0].RemoteUser)
	assert.Equal(t, "pending", pending[0].State)
	assert.Equal(t, "secret-token-abc", pending[0].Token)
}

func TestFederationAcceptReject(t *testing.T) {
	env := cogtest.New(t)
	bob := env.CreateUser(t, "bob", "pass")

	// Create a pending inbound share directly.
	fs := &federation.FederatedShare{
		Direction:    "inbound",
		State:        "pending",
		RemoteHost:   "remote.example.com",
		RemoteUser:   "alice",
		LocalUserID:  bob.ID,
		Token:        "test-token",
		ResourceName: "Photos",
		ResourceType: "file",
		OwnerName:    "Alice",
		OwnerHost:    "remote.example.com",
		Permissions:  1,
	}
	require.NoError(t, env.Providers.Federation.Create(fs))

	// List pending via OCS API.
	resp := env.Get(t, "/ocs/v2.php/apps/files_sharing/api/v1/remote_shares/pending?format=json", bob.Auth)
	resp.Status(200)

	var ocsResp struct {
		OCS struct {
			Data []json.RawMessage `json:"data"`
		} `json:"ocs"`
	}
	resp.JSON(&ocsResp)
	require.Equal(t, 1, len(ocsResp.OCS.Data))

	// Accept the share via OCS API.
	ncID, _ := env.Providers.Federation.NCIDFor(fs.ID)
	// Accept uses the internal ID since we don't have the NC ID easily here.
	resp = env.Post(t, "/ocs/v2.php/apps/files_sharing/api/v1/remote_shares/pending/"+fs.ID+"?format=json", bob.Auth)
	resp.Status(200)

	// Verify it's now accepted.
	updated, err := env.Providers.Federation.Get(fs.ID)
	require.NoError(t, err)
	assert.Equal(t, "accepted", updated.State)
	assert.Equal(t, "/remote-shares/Photos", updated.MountPath)

	// List accepted shares.
	resp = env.Get(t, "/ocs/v2.php/apps/files_sharing/api/v1/remote_shares?format=json", bob.Auth)
	resp.Status(200)
	resp.JSON(&ocsResp)
	require.Equal(t, 1, len(ocsResp.OCS.Data))

	_ = ncID
}

func TestFederationCrossInstance(t *testing.T) {
	// Set up two Cog instances with real httptest.Servers that can talk to each other.
	sender := cogtest.New(t)
	receiver := cogtest.New(t)

	alice := sender.CreateUser(t, "alice", "pass")
	bob := receiver.CreateUser(t, "bob", "pass")

	// Upload a file on sender via WebDAV.
	sender.Put(t, "/remote.php/dav/files/"+alice.ID+"/hello.txt",
		strings.NewReader("hello federation"),
		alice.Auth, cogtest.Header("Content-Type", "text/plain"))

	// Use the receiver's full BaseURL (http://127.0.0.1:PORT) as the remote host.
	// In production this would be a domain like "cloud.example.com", but in tests
	// we need the scheme since httptest uses plain HTTP.
	receiverHost := receiver.Providers.Config.BaseURL

	// Create a federated share via OCS API (shareType=6).
	form := url.Values{
		"path":        {"/hello.txt"},
		"shareType":   {"6"},
		"shareWith":   {"bob@" + receiverHost},
		"permissions": {"1"},
	}
	resp := sender.PostForm(t, "/ocs/v2.php/apps/files_sharing/api/v1/shares?format=json", form, alice.Auth)
	resp.Status(200)

	// Verify the share was created on sender side.
	var outbound []federation.FederatedShare
	for fs, err := range sender.Providers.Federation.ListOutbound(alice.ID) {
		require.NoError(t, err)
		outbound = append(outbound, fs)
	}
	require.Equal(t, 1, len(outbound))
	assert.Equal(t, "outbound", outbound[0].Direction)
	assert.Equal(t, receiverHost, outbound[0].RemoteHost)

	// Verify the share arrived as pending on receiver side.
	var pending []federation.FederatedShare
	for fs, err := range receiver.Providers.Federation.ListInbound(bob.ID, "pending") {
		require.NoError(t, err)
		pending = append(pending, fs)
	}
	require.Equal(t, 1, len(pending))
	assert.Equal(t, "hello.txt", pending[0].ResourceName)
	assert.Equal(t, "pending", pending[0].State)

	// Bob accepts the share via OCS API.
	resp = receiver.Post(t, "/ocs/v2.php/apps/files_sharing/api/v1/remote_shares/pending/"+pending[0].ID+"?format=json", bob.Auth)
	resp.Status(200)

	// Verify it's now accepted.
	accepted, err := receiver.Providers.Federation.Get(pending[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "accepted", accepted.State)
	assert.Equal(t, "/remote-shares/hello.txt", accepted.MountPath)
}

func TestFederationNotification(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Create an outbound share.
	fs := &federation.FederatedShare{
		Direction:   "outbound",
		State:       "pending",
		RemoteHost:  "remote.example.com",
		RemoteUser:  "bob",
		LocalUserID: alice.ID,
		Token:       "out-token",
		Permissions: 1,
	}
	require.NoError(t, env.Providers.Federation.Create(fs))

	// Remote sends SHARE_ACCEPTED notification.
	notif := federation.OCMNotification{
		NotificationType: federation.NotifyShareAccepted,
		ResourceType:     "file",
		ProviderID:       fs.ID,
	}
	resp := env.PostJSON(t, "/ocm/notifications", notif)
	resp.Status(200)

	// Verify state changed.
	updated, err := env.Providers.Federation.Get(fs.ID)
	require.NoError(t, err)
	assert.Equal(t, "accepted", updated.State)
}

func stripScheme(u string) string {
	if parsed, err := url.Parse(u); err == nil {
		return parsed.Host
	}
	return u
}
