package web_test

import (
	"bytes"
	"image"
	"image/png"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/user"
)

func TestAvatarServeInitials(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	resp := env.Get(t, "/avatar/"+alice.ID+"/initials").Status(200)
	ct := resp.Header.Get("Content-Type")
	require.Equal(t, "image/svg+xml", ct)
}

func TestAvatarServeDefault(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	resp := env.Get(t, "/avatar/"+alice.ID).Status(200)
	ct := resp.Header.Get("Content-Type")
	require.Equal(t, "image/svg+xml", ct)
}

func TestAvatarUpload(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// Create a small valid PNG.
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var imgBuf bytes.Buffer
	require.NoError(t, png.Encode(&imgBuf, img))

	// Build multipart form with correct Content-Type for the image part.
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("csrf_token", token)
	part, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="avatar"; filename="avatar.png"`},
		"Content-Type":        {"image/png"},
	})
	require.NoError(t, err)
	part.Write(imgBuf.Bytes())
	w.Close()

	req := cogtest.NewRequest(t, "POST", env.Providers.Config.BaseURL+"/settings/avatar", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	env.Do(t, req, cookie)

	// Verify avatar was set.
	u, err := env.Providers.Users.GetByID(alice.ID)
	require.NoError(t, err)
	require.NotEqual(t, "", u.Avatar)
}

func TestAvatarDelete(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// Set avatar directly via store.
	avatar := alice.ID + ".png"
	require.NoError(t, env.Providers.Users.Update(alice.ID, user.UpdateOpts{Avatar: &avatar}))

	env.Delete(t, "/settings/avatar",
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	u, err := env.Providers.Users.GetByID(alice.ID)
	require.NoError(t, err)
	require.Equal(t, "", u.Avatar)
}

func TestAvatarUploadInvalidFormat(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("csrf_token", token)
	part, err := w.CreateFormFile("avatar", "file.txt")
	require.NoError(t, err)
	part.Write([]byte("not an image"))
	w.Close()

	req := cogtest.NewRequest(t, "POST", env.Providers.Config.BaseURL+"/settings/avatar", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	env.Do(t, req, cookie).Status(400)
}

// TestAvatar_PathTraversalDefense plants a malformed Avatar column value
// containing ".." and confirms the Serve handler cannot be tricked into
// reading a file outside avatarsDir (audit security.md #42).
func TestAvatar_PathTraversalDefense(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Plant a sibling outside the avatars dir.
	dataDir := env.Providers.Config.DataDir
	secret := filepath.Join(dataDir, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("S3CR3T"), 0o644))
	t.Cleanup(func() { os.Remove(secret) })

	// Plant the malformed Avatar column directly.
	bad := "../secret.txt"
	require.NoError(t, env.Providers.Users.Update(alice.ID, user.UpdateOpts{Avatar: &bad}))

	// Serve — filepath.Base strips the traversal; handler falls back to
	// initials SVG. The secret content must not appear.
	resp := env.Get(t, "/avatar/"+alice.ID).Status(200)
	body := resp.ReadBody()
	if strings.Contains(body, "S3CR3T") {
		t.Fatal("avatar leaked file outside avatars dir")
	}
}
