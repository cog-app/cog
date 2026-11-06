package web_test

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/internal/user"

	gossh "golang.org/x/crypto/ssh"
)

func TestBrowserLoginFlow(t *testing.T) {
	env := cogtest.New(t)
	_ = env.CreateUser(t, "alice", "secret")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Navigate(t, "/login")

	// Fill in credentials and submit.
	b.Type(t, "#username", "alice")
	b.Type(t, "#password", "secret")
	b.Click(t, `button[type="submit"]`)

	// After login we should be redirected away from /login.
	b.WaitNotVisible(t, "#password")
	url := b.URL(t)
	require.True(t, !strings.Contains(url, "/login"), "expected redirect away from /login, got %s", url)
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

func TestBrowserLoginBadPassword(t *testing.T) {
	env := cogtest.New(t)
	_ = env.CreateUser(t, "alice", "secret")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Navigate(t, "/login")

	b.Type(t, "#username", "alice")
	b.Type(t, "#password", "wrong")
	b.Click(t, `button[type="submit"]`)

	// Should stay on login page with an error message.
	b.WaitVisible(t, "#username")
	url := b.URL(t)
	require.True(t, strings.Contains(url, "/login"), "expected to stay on /login, got %s", url)
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// --- Admin User CRUD ---

func TestBrowserAdminCreateUser(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")

	// Navigate to new user form.
	b.Navigate(t, "/admin/users/new")
	b.WaitVisible(t, `input[name="username"]`)

	// Fill in form and submit.
	b.Type(t, "#username", "newuser")
	b.Type(t, "#password", "newpass")
	b.Click(t, `.btn.btn-primary`)

	// Should redirect to users list showing the new user.
	b.WaitText(t, "newuser")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)

	// Verify user was created.
	_, err := env.Providers.Users.GetByUsername("newuser")
	require.NoError(t, err)
}

func TestBrowserAdminEditUser(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	target := env.CreateUser(t, "target", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")

	// Navigate to edit user form.
	b.Navigate(t, "/admin/users/"+target.ID)
	b.WaitVisible(t, `input[name="display_name"]`)

	// Update display name.
	b.Type(t, `input[name="display_name"]`, "Target User")
	b.Click(t, `form[action*="/admin/users/"] button[type="submit"]`)

	// Should see success flash.
	b.WaitText(t, "User updated")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)

	u, err := env.Providers.Users.GetByID(target.ID)
	require.NoError(t, err)
	require.Equal(t, "Target User", u.DisplayName)
}

func TestBrowserAdminResetPassword(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	target := env.CreateUser(t, "target", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")

	b.Navigate(t, "/admin/users/"+target.ID)
	b.WaitVisible(t, `input[name="new_password"]`)

	b.Type(t, `input[name="new_password"]`, "changed123")
	b.Click(t, `form[action*="/password"] button[type="submit"]`)

	b.WaitText(t, "Password reset")

	_, err := env.Providers.Users.Authenticate("target", "changed123")
	require.NoError(t, err)
}

// --- Admin Groups ---

func TestBrowserAdminCreateGroup(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")

	b.Navigate(t, "/admin/groups")
	b.WaitVisible(t, `input[name="name"]`)

	b.Type(t, `input[name="name"]`, "browser-group")
	b.Submit(t, `input[name="name"]`)

	b.WaitText(t, "browser-group")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// --- Admin Volumes ---

func TestBrowserAdminCreateVolume(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")

	b.Navigate(t, "/admin/volumes/new")
	b.WaitVisible(t, `input[name="name"]`)

	dir := t.TempDir()
	b.Type(t, `input[name="name"]`, "testvol")
	b.Type(t, `input[name="file_path"]`, dir)
	b.Submit(t, `input[name="file_path"]`)

	// Should redirect to volumes list.
	b.WaitText(t, "testvol")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)

	_, err := env.Providers.Volumes.GetByName("testvol")
	require.NoError(t, err)
}

func TestBrowserAdminEditVolume(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	dir := t.TempDir()
	v, err := env.Providers.Volumes.Create("editme", "old desc", "file://"+dir, nil, "{}")
	require.NoError(t, err)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")

	b.Navigate(t, "/admin/volumes/"+v.ID)
	b.WaitVisible(t, `input[name="name"]`)

	b.Type(t, `input[name="name"]`, "renamed")
	b.Type(t, `input[name="description"]`, "new desc")
	b.Click(t, `form[action*="/admin/volumes/"] button[type="submit"]`)

	b.WaitText(t, "Volume updated")

	got, err := env.Providers.Volumes.GetByID(v.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Name)
}

// --- Admin Settings ---

func TestBrowserAdminSettings(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")

	b.Navigate(t, "/admin/email")
	b.WaitVisible(t, `input[name="smtp_host"]`)

	b.Type(t, `input[name="smtp_host"]`, "mail.example.com")
	b.Type(t, `input[name="smtp_port"]`, "465")
	b.Type(t, `input[name="smtp_from"]`, "noreply@example.com")
	b.Click(t, `form[action="/admin/email"] button[type="submit"]`)

	b.WaitText(t, "Email settings saved")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

func TestBrowserAdminLogs(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")

	b.Navigate(t, "/admin/logs")
	b.WaitText(t, "Server logs")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

func TestBrowserAdminPlugins(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")

	b.Navigate(t, "/admin/plugins")
	b.WaitText(t, "files")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// --- Settings Profile ---

func TestBrowserSettingsProfile(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "alice", "pass")

	b.Navigate(t, "/settings/profile")
	b.WaitVisible(t, `input[name="display_name"]`)

	b.Type(t, `input[name="display_name"]`, "Alice Wonder")
	b.Type(t, `input[name="email"]`, "alice@example.com")
	b.Type(t, `input[name="current_password"]`, "pass")
	b.Click(t, `form[action="/settings/profile"] button[type="submit"]`)

	b.WaitText(t, "We sent a link to alice@example.com")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)

	u, err := env.Providers.Users.GetByUsername("alice")
	require.NoError(t, err)
	require.Equal(t, "Alice Wonder", u.DisplayName)
}

// --- Settings Security ---

func TestBrowserSettingsChangePassword(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "oldpass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "alice", "oldpass")

	b.Navigate(t, "/settings/security")
	b.WaitVisible(t, `#current_password`)

	b.Type(t, "#current_password", "oldpass")
	b.Type(t, "#new_password", "newpass")
	b.Type(t, "#confirm_password", "newpass")
	b.Click(t, `form[action="/settings/password"] button[type="submit"]`)

	b.WaitText(t, "Password changed")

	_, err := env.Providers.Users.Authenticate("alice", "newpass")
	require.NoError(t, err)
}

func TestBrowserSettingsCreateAppPassword(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "alice", "pass")

	b.Navigate(t, "/settings/security")
	b.WaitVisible(t, `form[hx-post="/settings/app-passwords"] input[name="name"]`)

	b.Type(t, `form[hx-post="/settings/app-passwords"] input[name="name"]`, "my-app")
	b.Click(t, `form[hx-post="/settings/app-passwords"] button[type="submit"]`)

	// Should show the created token.
	b.WaitVisible(t, ".alert-success")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)

	passwords, err := env.Providers.AppPasswords.ListByUser(alice.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(passwords))
	require.Equal(t, "my-app", passwords[0].Name)
}

// --- Settings Storage ---

func TestBrowserSettingsStorage(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "alice", "pass")

	b.Navigate(t, "/settings/storage")
	b.WaitVisible(t, `input[name="user_path"]`)
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// --- Groups ---

func TestBrowserCreateGroup(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "alice", "pass")

	b.Navigate(t, "/settings/groups")
	b.WaitVisible(t, `input[name="name"]`)

	b.Type(t, `input[name="name"]`, "my-team")
	b.Click(t, `form[action="/settings/groups"] button[type="submit"]`)

	b.WaitText(t, "my-team")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

func TestBrowserGroupDetail(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.CreateUser(t, "bob", "pass")

	grp, err := env.Providers.Groups.Create("team", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, alice.ID, group.Owner))

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "alice", "pass")

	b.Navigate(t, "/settings/groups/"+grp.ID)
	b.WaitText(t, "team")
	b.WaitText(t, "alice")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// --- Volume Browser ---

func TestBrowserVolumeBrowse(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	// Create some files in the volume.
	dir := strings.TrimPrefix(vol.URI, "file://")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644))

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")

	// The volume is the data directory. The server's own files in it (the
	// system database, its journal, the search index) are not listed.

	// Modification times are shown; fix them, after logging in has written
	// whatever it writes, so the golden is stable.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.NoError(t, os.Chtimes(filepath.Join(dir, e.Name()), env.Now(), env.Now()))
	}

	b.Navigate(t, "/volumes/"+vol.ID+"/browse")
	b.WaitText(t, "docs")
	b.WaitText(t, "hello.txt")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

func TestBrowserVolumeMkdir(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)
	dir := strings.TrimPrefix(vol.URI, "file://")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")

	// Modification times are shown; fix them, after logging in has written
	// whatever it writes, so the golden is stable.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.NoError(t, os.Chtimes(filepath.Join(dir, e.Name()), env.Now(), env.Now()))
	}

	b.Navigate(t, "/volumes/"+vol.ID+"/browse")
	b.WaitVisible(t, `input[name="name"]`)

	b.Type(t, `input[name="name"]`, "newdir")
	b.Click(t, `form[action*="/mkdir"] button[type="submit"]`)

	b.WaitText(t, "newdir")

	info, err := os.Stat(filepath.Join(dir, "newdir"))
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

// --- Invite Flow ---

func TestBrowserAcceptInvite(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	grp, err := env.Providers.Groups.Create("cool-team", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, alice.ID, group.Owner))

	inv, err := env.Providers.Invitations.Create(grp.ID, "bob@example.com", alice.ID, 24, group.User)
	require.NoError(t, err)

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	// Unauthenticated user registers via invite.
	b := eng.Start(t)
	b.Navigate(t, "/invite/"+inv.Token)
	b.WaitVisible(t, `#username`)

	b.Type(t, "#username", "bob")
	b.Type(t, "#password", "secret")
	b.Type(t, "#confirm", "secret")
	b.Click(t, `button[type="submit"]`)

	// Should end up on group page as new member.
	b.WaitText(t, "cool-team")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)

	_, err = env.Providers.Users.GetByUsername("bob")
	require.NoError(t, err)
	got, err := env.Providers.Groups.Get(grp.ID)
	require.NoError(t, err)
	require.Equal(t, 2, got.MemberCount)
}

// The log table shows newest first, which prints a stack trace backwards. The
// copy button reverses that, so what lands on the clipboard reads in the order
// the events happened.
func TestBrowserAdminLogsCopy(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")

	env.Providers.Log.Info("copytest first entry")
	env.Providers.Log.Info("copytest second entry")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")
	b.Navigate(t, "/admin/logs")
	b.WaitVisible(t, "#log-copy")

	// The button calls the shared helper from cog-share.js, which the admin
	// layout has to be loading for any of this to work.
	require.Equal(t, "function", b.Eval(t, `typeof cogCopy`),
		"cogCopy is not defined on the admin logs page")

	// On screen, newest is on top.
	firstRow, ok := b.Eval(t, `document.querySelector('#log-tbody tr').innerText`).(string)
	require.True(t, ok, "row text is not a string")
	require.True(t, strings.Contains(firstRow, "copytest second entry"),
		"top row = %q, want the newest entry", firstRow)

	// Copied, the same rows read oldest first.
	copied, ok := b.Eval(t, `logEntriesText()`).(string)
	require.True(t, ok, "copied text is not a string")

	first := strings.Index(copied, "copytest first entry")
	second := strings.Index(copied, "copytest second entry")
	require.True(t, first >= 0 && second >= 0,
		"copied text is missing an entry: %q", copied)
	require.True(t, first < second,
		"copied text has the entries newest first:\n%s", copied)

	// Each line carries the time and level beside the message, tab separated.
	for line := range strings.SplitSeq(copied, "\n") {
		if strings.Contains(line, "copytest first entry") {
			require.Equal(t, 3, len(strings.Split(line, "\t")),
				"line is not time/level/message: %q", line)
		}
	}
}

// --- UX review coverage: states no other browser test reaches ---

// The General admin page is the admin landing page and had no snapshot.
func TestBrowserAdminGeneralSettings(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")
	b.Navigate(t, "/admin/settings")
	b.WaitVisible(t, `input[name="invite_expiry_hours"]`)
	// Uptimes follow the wall clock.
	b.Eval(t, `() => document.querySelectorAll("[data-uptime]").forEach(d => { d.textContent = "«uptime»" })`)
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// A user list with long names and empty display names, the way a real
// instance looks, on both widths.
func TestBrowserAdminUsersLongNames(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	long := env.CreateUser(t, "a.very.long.username.from.ldap@example-corporation.com", "pass")
	name := "Maximiliana Alexandrina Featherstonehaugh-Cholmondeley"
	require.NoError(t, env.Providers.Users.Update(long.ID, user.UpdateOpts{DisplayName: &name}))
	env.CreateUser(t, "bob", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")
	b.Navigate(t, "/admin/users")
	b.WaitText(t, "Featherstonehaugh")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// Creating a user whose name is taken: the error path of the new-user form.
func TestBrowserAdminNewUserDuplicate(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	env.CreateUser(t, "taken", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")
	b.Navigate(t, "/admin/users/new")
	b.Type(t, "#username", "taken")
	b.Type(t, "#password", "x")
	b.Click(t, `form[action="/admin/users"] button[type="submit"]`)
	b.WaitVisible(t, ".alert")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

func TestBrowserAdminVolumeEditPage(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	dir := t.TempDir()
	v, err := env.Providers.Volumes.Create("archive", "Cold storage for old projects", "file://"+dir, nil, "{}")
	require.NoError(t, err)

	b := eng.Start(t)
	b.Login(t, "admin", "pass")
	b.Navigate(t, "/admin/volumes/"+v.ID)
	b.WaitVisible(t, `input[name="name"]`)
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// The groups list itself: creating a group redirects to the group, so no
// other test sees this page with groups in it.
func TestBrowserSettingsGroupsList(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	own, err := env.Providers.Groups.Create("book-club-with-a-surprisingly-long-name", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(own.ID, alice.ID, group.Owner))
	other, err := env.Providers.Groups.Create("family", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(other.ID, bob.ID, group.Owner))
	require.NoError(t, env.Providers.Groups.AddMember(other.ID, alice.ID, group.User))

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "alice", "pass")
	b.Navigate(t, "/settings/groups")
	b.WaitText(t, "family")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// An owner's group page with members and an open invitation, which is where
// the copyable invite link and the per-row actions show.
func TestBrowserGroupDetailWithInvites(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	grp, err := env.Providers.Groups.Create("team", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, alice.ID, group.Owner))
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, bob.ID, group.User))
	_, err = env.Providers.Invitations.Create(grp.ID, "carol.with.a.long.address@example.com", alice.ID, 24, group.User)
	require.NoError(t, err)
	_, err = env.Providers.Invitations.Create(grp.ID, "", alice.ID, 24, group.Owner)
	require.NoError(t, err)

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "alice", "pass")
	b.Navigate(t, "/settings/groups/"+grp.ID)
	b.WaitText(t, "carol.with.a.long.address@example.com")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// A mismatched confirmation on the change-password form.
func TestBrowserSettingsChangePasswordMismatch(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "alice", "pass")
	b.Navigate(t, "/settings/security")
	b.Type(t, "#current_password", "pass")
	b.Type(t, "#new_password", "one")
	b.Type(t, "#confirm_password", "two")
	b.Click(t, `form[action="/settings/password"] button[type="submit"]`)
	b.WaitVisible(t, ".alert-error")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

func TestBrowserForgotPassword(t *testing.T) {
	env := cogtest.New(t)

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Navigate(t, "/forgot-password")
	b.WaitVisible(t, "#identifier")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// A signed-in user following an invite link.
func TestBrowserInviteLoggedIn(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.CreateUser(t, "bob", "pass")

	grp, err := env.Providers.Groups.Create("cool-team", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, alice.ID, group.Owner))
	inv, err := env.Providers.Invitations.Create(grp.ID, "", alice.ID, 24, group.User)
	require.NoError(t, err)

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "bob", "pass")
	b.Navigate(t, "/invite/"+inv.Token)
	b.WaitText(t, "cool-team")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// The NextCloud Login Flow v2 as a desktop or mobile client drives it: the
// client opens the grant URL in a browser, the user signs in and approves.
func TestBrowserNCLoginFlowGrant(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "secret")

	var init struct {
		Login string `json:"login"`
	}
	env.Post(t, "/index.php/login/v2").Status(200).JSON(&init)
	i := strings.Index(init.Login, "/index.php/login/v2/grant")
	require.True(t, i >= 0, "unexpected login URL %q", init.Login)
	grantPath := init.Login[i:]

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Navigate(t, grantPath)
	b.WaitVisible(t, "#username")
	t.Run("Login", func(t *testing.T) {
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	b.Type(t, "#username", "alice")
	b.Type(t, "#password", "secret")
	b.Click(t, `button[type="submit"]`)
	b.WaitText(t, "Grant access")
	t.Run("Grant", func(t *testing.T) {
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	b.Click(t, `button[type="submit"]`)
	b.WaitText(t, "Access granted")
	t.Run("Granted", func(t *testing.T) {
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

func TestBrowserNotFound(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "alice", "pass")
	b.Navigate(t, "/no/such/page")
	b.WaitReady(t, "body")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

// Adding an SSH key from the security page. The key is fixed so the
// fingerprint in the golden is too.
func TestBrowserSettingsAddSSHKey(t *testing.T) {
	env := cogtest.New(t)
	env.Providers.Config.SSHEnabled = true
	env.CreateUser(t, "alice", "pass")

	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	pub, err := gossh.NewPublicKey(priv.Public())
	require.NoError(t, err)
	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(pub))) + " alice@laptop"

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.Start(t)
	b.Login(t, "alice", "pass")
	b.Navigate(t, "/settings/security")
	b.WaitVisible(t, "#ssh-public-key")
	b.Type(t, "#ssh-public-key", line)
	b.Click(t, `form[action="/settings/ssh-keys"] button[type="submit"]`)
	b.WaitVisible(t, `#ssh-keys .alert-success`)
	b.Snapshot(t)
}
