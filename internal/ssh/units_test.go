package ssh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// --- hostkey ---

func TestLoadOrGenerateHostKey_GeneratesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	signer, err := LoadOrGenerateHostKey("", dir)
	require.NoError(t, err)
	require.NotNil(t, signer)

	// File now exists on disk.
	keyPath := filepath.Join(dir, "ssh_host_ed25519_key")
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("expected key file at %s: %v", keyPath, err)
	}

	// Second call reads existing file (path arg explicit).
	signer2, err := LoadOrGenerateHostKey(keyPath, dir)
	require.NoError(t, err)
	require.NotNil(t, signer2)

	// Same key material.
	if !bytes.Equal(signer.PublicKey().Marshal(), signer2.PublicKey().Marshal()) {
		t.Fatal("expected stable host key across loads")
	}
}

func TestLoadOrGenerateHostKey_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deeper", "host_key")
	signer, err := LoadOrGenerateHostKey(path, dir)
	require.NoError(t, err)
	require.NotNil(t, signer)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected key at %s: %v", path, err)
	}
}

func TestLoadOrGenerateHostKey_BadPathBubbles(t *testing.T) {
	// Path that already exists as a directory → ReadFile fails with "is a
	// directory", which isn't IsNotExist; LoadOrGenerateHostKey surfaces it.
	dir := t.TempDir()
	_, err := LoadOrGenerateHostKey(dir, dir)
	require.True(t, err != nil)
}

// --- devfiles ---

func TestDevNull_ReadEOF(t *testing.T) {
	h := newDevHandler(devNull, "null")
	f, err := h.Open(os.O_RDONLY, 0)
	require.NoError(t, err)
	defer f.Close()

	buf := make([]byte, 8)
	n, err := f.Read(buf)
	require.Equal(t, 0, n)
	require.True(t, err == io.EOF)
}

func TestDevZero_ReadFillsBuffer(t *testing.T) {
	h := newDevHandler(devZero, "zero")
	f, _ := h.Open(os.O_RDONLY, 0)
	defer f.Close()

	buf := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	n, err := f.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 8, n)
	for i, b := range buf {
		if b != 0 {
			t.Fatalf("byte %d = %d, want 0", i, b)
		}
	}
}

func TestDevURandom_ReadFillsBuffer(t *testing.T) {
	h := newDevHandler(devURandom, "urandom")
	f, _ := h.Open(os.O_RDONLY, 0)
	defer f.Close()

	buf := make([]byte, 32)
	n, err := f.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 32, n)
}

func TestDevFile_NoopOps(t *testing.T) {
	h := newDevHandler(devNull, "null")
	f, _ := h.Open(os.O_RDWR, 0)
	defer f.Close()

	// Write/WriteAt swallow data and return len.
	n, err := f.Write([]byte("hi"))
	require.NoError(t, err)
	require.Equal(t, 2, n)
	n, err = f.WriteAt([]byte("ab"), 0)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	// Seek/Truncate are no-ops.
	off, err := f.Seek(10, 0)
	require.NoError(t, err)
	require.Equal(t, int64(0), off)
	require.NoError(t, f.Truncate(99))
}

func TestDevHandler_Stat(t *testing.T) {
	h := newDevHandler(devNull, "null")
	fi, err := h.Stat()
	require.NoError(t, err)
	require.Equal(t, "null", fi.Name())
	require.False(t, fi.IsDir())
	require.Equal(t, int64(0), fi.Size())
	require.True(t, fi.Mode() != 0)
}

func TestDevFile_ReadAt(t *testing.T) {
	h := newDevHandler(devZero, "zero")
	f, _ := h.Open(os.O_RDONLY, 0)
	defer f.Close()

	buf := make([]byte, 4)
	n, err := f.ReadAt(buf, 100) // offset ignored for /dev/zero
	require.NoError(t, err)
	require.Equal(t, 4, n)
}

// --- authkeysfile ---

func genAuthKeyLine(t *testing.T, comment string) (line, fp string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := gossh.NewPublicKey(pub)
	require.NoError(t, err)
	line = strings.TrimSpace(string(gossh.MarshalAuthorizedKey(sshPub))) + " " + comment
	fp = gossh.FingerprintSHA256(sshPub)
	return
}

func TestAuthKeysFile_ReadEmpty(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "pass")
	h := newAuthKeysFileHandler(env.keys, "alice")

	f, err := h.Open(os.O_RDONLY, 0)
	require.NoError(t, err)
	defer f.Close()

	data, _ := io.ReadAll(readSeekReader{f.(*authKeysFile)})
	require.Equal(t, "", string(data))

	fi, _ := f.Stat()
	require.Equal(t, int64(0), fi.Size())
}

func TestAuthKeysFile_ReadRendersStoredKeys(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "pass")

	line1, _ := genAuthKeyLine(t, "alice@a")
	line2, _ := genAuthKeyLine(t, "alice@b")
	_, err := env.keys.Add("alice", "a", line1)
	require.NoError(t, err)
	_, err = env.keys.Add("alice", "b", line2)
	require.NoError(t, err)

	h := newAuthKeysFileHandler(env.keys, "alice")
	f, _ := h.Open(os.O_RDONLY, 0)
	defer f.Close()

	data, _ := io.ReadAll(readSeekReader{f.(*authKeysFile)})
	body := string(data)
	require.True(t, strings.Contains(body, strings.SplitN(line1, " ", 3)[1]))
	require.True(t, strings.Contains(body, strings.SplitN(line2, " ", 3)[1]))
}

func TestAuthKeysFile_WriteAddsNewKey(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "pass")
	h := newAuthKeysFileHandler(env.keys, "alice")

	f, err := h.Open(os.O_WRONLY, 0)
	require.NoError(t, err)

	line, fp := genAuthKeyLine(t, "via-ssh-copy-id")
	n, err := f.Write([]byte(line + "\n"))
	require.NoError(t, err)
	require.Equal(t, len(line)+1, n)
	require.NoError(t, f.Close())

	// Verify the key was added.
	stored, err := env.keys.List("alice")
	require.NoError(t, err)
	var found bool
	for _, k := range stored {
		if k.Fingerprint == fp {
			found = true
		}
	}
	require.True(t, found)
}

func TestAuthKeysFile_WriteSkipsDuplicateFingerprint(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "pass")

	line, _ := genAuthKeyLine(t, "first")
	_, err := env.keys.Add("alice", "first", line)
	require.NoError(t, err)
	before, _ := env.keys.List("alice")

	h := newAuthKeysFileHandler(env.keys, "alice")
	f, _ := h.Open(os.O_WRONLY, 0)
	_, err = f.Write([]byte(line + "\n"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	after, _ := env.keys.List("alice")
	require.Equal(t, len(before), len(after))
}

func TestAuthKeysFile_WriteAt_NotSupported(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "pass")
	h := newAuthKeysFileHandler(env.keys, "alice")
	f, _ := h.Open(os.O_WRONLY, 0)
	defer f.Close()

	_, err := f.WriteAt([]byte("x"), 5)
	require.True(t, err != nil)
}

func TestAuthKeysFile_ReadOnly_WriteRejected(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "pass")
	h := newAuthKeysFileHandler(env.keys, "alice")

	f, _ := h.Open(os.O_RDONLY, 0)
	defer f.Close()

	_, err := f.Write([]byte("x"))
	require.True(t, err != nil)
}

func TestAuthKeysFile_Seek(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "pass")
	line, _ := genAuthKeyLine(t, "k")
	_, err := env.keys.Add("alice", "k", line)
	require.NoError(t, err)

	h := newAuthKeysFileHandler(env.keys, "alice")
	f, _ := h.Open(os.O_RDONLY, 0)
	defer f.Close()

	pos, err := f.Seek(0, io.SeekStart)
	require.NoError(t, err)
	require.Equal(t, int64(0), pos)
}

func TestAuthKeysFile_TruncateNoop(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "pass")
	h := newAuthKeysFileHandler(env.keys, "alice")
	f, _ := h.Open(os.O_RDWR, 0)
	defer f.Close()
	require.NoError(t, f.Truncate(42))
}

func TestAuthKeysFileInfo_Metadata(t *testing.T) {
	info := &authKeysFileInfo{size: 100}
	require.Equal(t, "authorized_keys", info.Name())
	require.Equal(t, int64(100), info.Size())
	require.False(t, info.IsDir())
	require.True(t, info.Sys() == nil)
	require.True(t, info.Mode() != 0)
	_ = info.ModTime()
}

// --- auth callbacks ---

// stubConnMeta minimally implements gossh.ConnMetadata for unit tests.
type stubConnMeta struct {
	user       string
	sessionID  []byte
	clientVers []byte
	serverVers []byte
	remoteAddr net.Addr
	localAddr  net.Addr
}

func (s *stubConnMeta) User() string          { return s.user }
func (s *stubConnMeta) SessionID() []byte     { return s.sessionID }
func (s *stubConnMeta) ClientVersion() []byte { return s.clientVers }
func (s *stubConnMeta) ServerVersion() []byte { return s.serverVers }
func (s *stubConnMeta) RemoteAddr() net.Addr  { return s.remoteAddr }
func (s *stubConnMeta) LocalAddr() net.Addr   { return s.localAddr }

func TestPasswordCallback_UserPassword(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")

	ac := &authCallbacks{
		users:        env.providers.Users,
		appPasswords: env.providers.AppPasswords,
		keys:         env.keys,
	}

	perms, err := ac.passwordCallback(&stubConnMeta{user: "alice"}, []byte("secret"))
	require.NoError(t, err)
	require.NotNil(t, perms)
	require.Equal(t, "alice", perms.Extensions["username"])
}

func TestPasswordCallback_WrongPassword(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")

	ac := &authCallbacks{
		users:        env.providers.Users,
		appPasswords: env.providers.AppPasswords,
		keys:         env.keys,
	}
	_, err := ac.passwordCallback(&stubConnMeta{user: "alice"}, []byte("wrong"))
	require.True(t, err != nil)
}

func TestPasswordCallback_AppPasswordHappyPath(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")

	alice, err := env.providers.Users.GetByUsername("alice")
	require.NoError(t, err)
	appPW, _, err := env.providers.AppPasswords.Create(alice.ID, "test")
	require.NoError(t, err)

	ac := &authCallbacks{
		users:        env.providers.Users,
		appPasswords: env.providers.AppPasswords,
		keys:         env.keys,
	}
	perms, err := ac.passwordCallback(&stubConnMeta{user: "alice"}, []byte(appPW))
	require.NoError(t, err)
	require.Equal(t, alice.ID, perms.Extensions["user_id"])
}

func TestPasswordCallback_AppPasswordWrongUserRejected(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")
	env.createUser(t, "bob", "secret")

	alice, _ := env.providers.Users.GetByUsername("alice")
	appPW, _, err := env.providers.AppPasswords.Create(alice.ID, "test")
	require.NoError(t, err)

	ac := &authCallbacks{
		users:        env.providers.Users,
		appPasswords: env.providers.AppPasswords,
		keys:         env.keys,
	}
	// Alice's app password presented as bob — must be refused.
	_, err = ac.passwordCallback(&stubConnMeta{user: "bob"}, []byte(appPW))
	require.True(t, err != nil)
	require.True(t, strings.Contains(err.Error(), "does not belong"))
}

func TestPublicKeyCallback_Happy(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")

	line, _ := genAuthKeyLine(t, "alice@laptop")
	_, err := env.keys.Add("alice", "laptop", line)
	require.NoError(t, err)

	pk, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
	require.NoError(t, err)

	ac := &authCallbacks{
		users:        env.providers.Users,
		appPasswords: env.providers.AppPasswords,
		keys:         env.keys,
	}
	perms, err := ac.publicKeyCallback(&stubConnMeta{user: "alice"}, pk)
	require.NoError(t, err)
	require.Equal(t, "alice", perms.Extensions["username"])
}

func TestPublicKeyCallback_WrongUser(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")
	env.createUser(t, "bob", "secret")

	line, _ := genAuthKeyLine(t, "alice@laptop")
	_, err := env.keys.Add("alice", "laptop", line)
	require.NoError(t, err)

	pk, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
	require.NoError(t, err)

	ac := &authCallbacks{
		users:        env.providers.Users,
		appPasswords: env.providers.AppPasswords,
		keys:         env.keys,
	}
	// Alice's key presented as bob — must be refused.
	_, err = ac.publicKeyCallback(&stubConnMeta{user: "bob"}, pk)
	require.True(t, err != nil)
	require.True(t, strings.Contains(err.Error(), "belongs to"))
}

func TestPublicKeyCallback_UnknownKey(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")

	line, _ := genAuthKeyLine(t, "stranger")
	pk, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
	require.NoError(t, err)

	ac := &authCallbacks{
		users:        env.providers.Users,
		appPasswords: env.providers.AppPasswords,
		keys:         env.keys,
	}
	_, err = ac.publicKeyCallback(&stubConnMeta{user: "alice"}, pk)
	require.True(t, err != nil)
}

// readSeekReader exposes io.Reader from a vfs.File whose Read takes []byte.
// Only used to drain authKeysFile via io.ReadAll.
type readSeekReader struct{ f *authKeysFile }

func (r readSeekReader) Read(p []byte) (int, error) { return r.f.Read(p) }
