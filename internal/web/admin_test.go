package web_test

import (
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestSMTPSettingsPersist(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	sess, err := env.Providers.Sessions.Create(admin.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	token := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	// Save SMTP settings via POST.
	env.PostForm(t, "/admin/settings", url.Values{
		"csrf_token":         {token},
		"smtp_host":          {"smtp.example.com"},
		"smtp_port":          {"465"},
		"smtp_user":          {"testuser"},
		"smtp_pass":          {"secret"},
		"smtp_from":          {"noreply@example.com"},
		"invite_expiry_hours": {"72"},
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

	// Verify the GET page renders the saved values.
	body := env.Get(t, "/admin/settings", cookie).Status(200).ReadBody()
	require.Contains(t, body, "smtp.example.com")
	require.Contains(t, body, "465")
	require.Contains(t, body, "testuser")
	require.Contains(t, body, "noreply@example.com")
}
