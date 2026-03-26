package web_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/web"
)

func TestSMTPSettingsPersist(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	sess, err := env.Providers.Sessions.Create(admin.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	token := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	// Save SMTP settings via POST (now on the email page).
	env.PostForm(t, "/admin/email", url.Values{
		"csrf_token": {token},
		"smtp_host":  {"smtp.example.com"},
		"smtp_port":  {"465"},
		"smtp_user":  {"testuser"},
		"smtp_pass":  {"secret"},
		"smtp_from":  {"noreply@example.com"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	// Verify settings were persisted to the DB.
	s := env.Providers.Settings
	host, _ := s.Get(settings.KeySMTPHost)
	require.Equal(t, "smtp.example.com", host)
	require.Equal(t, 465, s.GetInt(settings.KeySMTPPort, 0))
	user, _ := s.Get(settings.KeySMTPUser)
	require.Equal(t, "testuser", user)
	pass, _ := s.Get(settings.KeySMTPPass)
	require.Equal(t, "secret", pass)
	from, _ := s.Get(settings.KeySMTPFrom)
	require.Equal(t, "noreply@example.com", from)

	// Verify the GET email page renders the saved values.
	body := env.Get(t, "/admin/email", cookie).Status(200).ReadBody()
	require.Contains(t, body, "smtp.example.com")
	require.Contains(t, body, "465")
	require.Contains(t, body, "testuser")
	require.Contains(t, body, "noreply@example.com")
}

func TestAdminListUsers(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)
	env.Get(t, "/admin/users", cookie).Status(200)
}

func TestAdminNewUserForm(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)
	env.Get(t, "/admin/users/new", cookie).Status(200)
}

func TestAdminEditUserForm(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	target := env.CreateUser(t, "target", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)
	env.Get(t, "/admin/users/"+target.ID, cookie).Status(200)
}

func TestAdminUpdateUser(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	target := env.CreateUser(t, "target", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	env.PostForm(t, "/admin/users/"+target.ID, url.Values{
		"csrf_token":   {token},
		"display_name": {"Updated Name"},
		"is_admin":     {"1"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	u, err := env.Providers.Users.GetByID(target.ID)
	require.NoError(t, err)
	require.Equal(t, "Updated Name", u.DisplayName)
	require.True(t, u.IsAdmin)
}

func TestAdminEditGroupForm(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)

	grp, err := env.Providers.Groups.Create("test-group", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, admin.ID, group.Owner))

	env.Get(t, "/admin/groups/"+grp.ID, cookie).Status(200)
}

func TestAdminUpdateGroupViaAdmin(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	grp, err := env.Providers.Groups.Create("old-name", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, admin.ID, group.Owner))

	env.PostForm(t, "/admin/groups/"+grp.ID, url.Values{
		"csrf_token": {token},
		"name":       {"new-name"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	got, err := env.Providers.Groups.Get(grp.ID)
	require.NoError(t, err)
	require.Equal(t, "new-name", got.Name)
}

func TestAdminDeleteGroupViaAdmin(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	grp, err := env.Providers.Groups.Create("to-delete", nil, nil)
	require.NoError(t, err)

	env.PostForm(t, "/admin/groups/"+grp.ID+"/delete", url.Values{
		"csrf_token": {token},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	_, err = env.Providers.Groups.Get(grp.ID)
	require.Error(t, err)
}

func TestAdminAddMemberViaAdmin(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	grp, err := env.Providers.Groups.Create("team", nil, nil)
	require.NoError(t, err)

	env.PostForm(t, "/admin/groups/"+grp.ID+"/members", url.Values{
		"csrf_token":  {token},
		"member_id":   {bob.ID},
		"member_type": {"USER"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	got, err := env.Providers.Groups.Get(grp.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.MemberCount)
}

func TestAdminRemoveMemberViaAdmin(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	grp, err := env.Providers.Groups.Create("team", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, bob.ID, group.User))

	env.Delete(t, "/admin/groups/"+grp.ID+"/members/"+bob.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	got, err := env.Providers.Groups.Get(grp.ID)
	require.NoError(t, err)
	require.Equal(t, 0, got.MemberCount)
}

func TestAdminShowLogs(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)
	env.Get(t, "/admin/logs", cookie).Status(200)
}

func TestAdminLogEntries(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)
	env.Get(t, "/admin/logs/entries", cookie).Status(200)
}

func TestAdminToggleProtectedPlugin(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	env.PostForm(t, "/admin/plugins/files/toggle", url.Values{
		"csrf_token": {token},
	}, cookie).Status(400)
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{time.Hour, "1h"},
		{time.Minute + 30*time.Second, "1m 30s"},
		{24 * time.Hour, "1d"},
		{365 * 24 * time.Hour, "1y"},
		{365*24*time.Hour + time.Hour, "1y 1h"},
	}
	for _, tt := range tests {
		t.Run(tt.d.String(), func(t *testing.T) {
			got := web.FormatDuration(tt.d)
			if got != tt.want {
				t.Errorf("FormatDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}
