package email_test

import (
	"bytes"
	"mime"
	"net/mail"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/email"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// --- NewFromSettings ---

func TestNewFromSettings_NilStore(t *testing.T) {
	require.True(t, email.NewFromSettings(nil) == nil)
}

func TestNewFromSettings_MissingHost(t *testing.T) {
	env := cogtest.New(t)
	// cogtest seeds host/from already — wipe them.
	require.NoError(t, env.Providers.Settings.Set(settings.KeySMTPHost, ""))
	require.True(t, email.NewFromSettings(env.Providers.Settings) == nil)
}

func TestNewFromSettings_MissingFrom(t *testing.T) {
	env := cogtest.New(t)
	require.NoError(t, env.Providers.Settings.Set(settings.KeySMTPFrom, ""))
	require.True(t, email.NewFromSettings(env.Providers.Settings) == nil)
}

func TestNewFromSettings_Configured(t *testing.T) {
	env := cogtest.New(t)
	// cogtest pre-seeds host=localhost, port=25, from=test@localhost.
	s := email.NewFromSettings(env.Providers.Settings)
	require.NotNil(t, s)
	require.Equal(t, "localhost", s.Host)
	require.Equal(t, 25, s.Port)
	require.Equal(t, "test@localhost", s.From)
}

// --- Send (via hook so no real SMTP) ---

func TestSend_PlainBodyNoAttachments(t *testing.T) {
	var captured struct {
		to  []string
		msg []byte
	}
	s := &email.Sender{
		From: "test@example.com",
		Hook: func(to []string, msg []byte) {
			captured.to = to
			captured.msg = msg
		},
	}

	require.NoError(t, s.Send([]string{"alice@example.com"}, "Hello", "hi there"))
	require.Equal(t, 1, len(captured.to))
	require.Equal(t, "alice@example.com", captured.to[0])

	msg := string(captured.msg)
	require.True(t, strings.Contains(msg, "From: test@example.com"))
	require.True(t, strings.Contains(msg, "To: alice@example.com"))
	require.True(t, strings.Contains(msg, "Subject: Hello"))
	require.True(t, strings.Contains(msg, "Content-Type: text/plain"))
	require.True(t, strings.Contains(msg, "hi there"))
}

func TestSend_WithAttachment_UsesMultipart(t *testing.T) {
	var captured []byte
	s := &email.Sender{
		From: "test@example.com",
		Hook: func(_ []string, msg []byte) { captured = msg },
	}

	att := email.Attachment{
		Filename:    "data.bin",
		ContentType: "application/octet-stream",
		Data:        []byte("payload-data"),
	}
	require.NoError(t, s.Send([]string{"a@b.com"}, "WithAttach", "see attached", att))

	msg := string(captured)
	require.True(t, strings.Contains(msg, "multipart/mixed"))
	require.True(t, strings.Contains(msg, "filename=data.bin"))
	require.True(t, strings.Contains(msg, "Content-Transfer-Encoding: base64"))
}

func TestSend_SubjectCannotAddHeaders(t *testing.T) {
	var captured []byte
	s := &email.Sender{
		From: "test@example.com",
		Hook: func(_ []string, msg []byte) { captured = msg },
	}
	require.NoError(t, s.Send([]string{"a@b.com"}, "Hi\r\nBcc: victim@example.com\n\nforged body", "real body"))
	msg, err := mail.ReadMessage(bytes.NewReader(captured))
	require.NoError(t, err)
	require.Equal(t, "", msg.Header.Get("Bcc"))
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	require.NoError(t, err)
	require.Equal(t, "Hi Bcc: victim@example.com forged body", subject)
}

func TestSend_NonASCIISubjectIsEncoded(t *testing.T) {
	var captured []byte
	s := &email.Sender{
		From: "test@example.com",
		Hook: func(_ []string, msg []byte) { captured = msg },
	}
	require.NoError(t, s.Send([]string{"a@b.com"}, "Café ☕", "body"))
	msg, err := mail.ReadMessage(bytes.NewReader(captured))
	require.NoError(t, err)
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	require.NoError(t, err)
	require.Equal(t, "Café ☕", subject)
}

func TestSend_RejectsRecipientWithHeaders(t *testing.T) {
	s := &email.Sender{From: "test@example.com", Hook: func([]string, []byte) { t.Fatal("sent") }}
	require.Error(t, s.Send([]string{"a@b.com\r\nBcc: victim@example.com"}, "x", "body"))
}

func TestSendMessage_HTMLWithAttachment(t *testing.T) {
	var captured []byte
	s := &email.Sender{
		From: "test@example.com",
		Hook: func(_ []string, msg []byte) { captured = msg },
	}
	require.NoError(t, s.SendMessage(email.Message{
		To: []string{"a@b.com"}, Subject: "s", Text: "plain", HTML: "<p>html</p>",
		Attachments: []email.Attachment{{Filename: `in "vite".ics`, ContentType: "text/calendar; method=REQUEST", Data: []byte("BEGIN:VCALENDAR")}},
	}))
	msg := string(captured)
	require.True(t, strings.Contains(msg, "multipart/mixed"))
	require.True(t, strings.Contains(msg, "multipart/alternative"))
	require.True(t, strings.Contains(msg, "<p>html</p>"))
	require.True(t, strings.Contains(msg, `filename="in \"vite\".ics"`))
}

func TestSend_MultipleRecipients(t *testing.T) {
	var captured []string
	s := &email.Sender{
		From: "test@example.com",
		Hook: func(to []string, _ []byte) { captured = to },
	}
	require.NoError(t, s.Send([]string{"a@x.com", "b@x.com"}, "multi", "body"))
	require.Equal(t, 2, len(captured))
}

// --- Template rendering ---

func TestRenderDefault_OTP(t *testing.T) {
	data := email.EmailData{
		InstanceName: "Cog",
		BaseURL:      "https://cog.example",
		User: email.UserInfo{
			Username: "alice",
			Email:    "alice@example.com",
		},
		Code: "123456",
	}
	html, text, err := email.RenderDefault("otp", data)
	require.NoError(t, err)
	require.True(t, strings.Contains(html, "123456"))
	require.True(t, strings.Contains(text, "123456"))
}

func TestRenderDefault_PasswordReset(t *testing.T) {
	data := email.EmailData{
		InstanceName: "Cog",
		BaseURL:      "https://cog.example",
		User:         email.UserInfo{Email: "alice@example.com"},
		ResetURL:     "https://cog.example/reset?token=xyz",
	}
	html, text, err := email.RenderDefault("password_reset", data)
	require.NoError(t, err)
	require.True(t, strings.Contains(html, "https://cog.example/reset?token=xyz"))
	require.True(t, strings.Contains(text, "https://cog.example/reset?token=xyz"))
}

func TestRenderDefault_GroupInvite(t *testing.T) {
	data := email.EmailData{
		InstanceName: "Cog",
		BaseURL:      "https://cog.example",
		User:         email.UserInfo{Email: "alice@example.com"},
		GroupName:    "Engineering",
		Inviter:      email.UserInfo{Username: "bob", DisplayName: "Bob"},
		InviteURL:    "https://cog.example/invite/abc",
	}
	html, text, err := email.RenderDefault("group_invite", data)
	require.NoError(t, err)
	require.True(t, strings.Contains(html, "Engineering"))
	require.True(t, strings.Contains(text, "Engineering"))
	require.True(t, strings.Contains(html, "https://cog.example/invite/abc"))
}

// --- Overrides ---

func TestRenderWithOverrides_OverrideReplacesBlock(t *testing.T) {
	override := `{{define "otp"}}OVERRIDDEN-{{.Code}}{{end}}`
	data := email.EmailData{User: email.UserInfo{Email: "a@b.com"}, Code: "999"}
	html, _, err := email.RenderWithOverrides("otp", data, override, "")
	require.NoError(t, err)
	require.True(t, strings.Contains(html, "OVERRIDDEN-999"))
}

func TestRenderWithOverrides_TextOverride(t *testing.T) {
	override := `{{define "otp"}}TXT-{{.Code}}{{end}}`
	data := email.EmailData{Code: "42"}
	_, text, err := email.RenderWithOverrides("otp", data, "", override)
	require.NoError(t, err)
	require.True(t, strings.Contains(text, "TXT-42"))
}

func TestRenderWithOverrides_BadHTML(t *testing.T) {
	_, _, err := email.RenderWithOverrides("otp",
		email.EmailData{Code: "1"},
		`{{define "otp"}}{{.NotAField}}{{end-bad}}`, // malformed
		"")
	require.True(t, err != nil)
}

func TestRenderWithOverrides_BadText(t *testing.T) {
	_, _, err := email.RenderWithOverrides("otp",
		email.EmailData{Code: "1"},
		"",
		`{{define "otp"}}{{end-bad}}`)
	require.True(t, err != nil)
}

// --- Validate*Override ---

func TestValidateHTMLOverride_Good(t *testing.T) {
	_, err := email.ValidateHTMLOverride(`{{define "otp"}}ok{{end}}`)
	require.NoError(t, err)
}

func TestValidateHTMLOverride_Bad(t *testing.T) {
	_, err := email.ValidateHTMLOverride(`{{define "otp"}}{{badfunc}}{{end}}`)
	require.True(t, err != nil)
}

func TestValidateTextOverride_Good(t *testing.T) {
	_, err := email.ValidateTextOverride(`{{define "otp"}}ok{{end}}`)
	require.NoError(t, err)
}

func TestValidateTextOverride_Bad(t *testing.T) {
	_, err := email.ValidateTextOverride(`{{end-bad}}`)
	require.True(t, err != nil)
}

// --- Render via settings store (DB-stored override) ---

func TestRender_PicksUpDBStoredOverride(t *testing.T) {
	env := cogtest.New(t)
	require.NoError(t, env.Providers.Settings.Set(
		settings.KeyEmailOverrideHTML,
		`{{define "otp"}}DBHTML-{{.Code}}{{end}}`,
	))
	require.NoError(t, env.Providers.Settings.Set(
		settings.KeyEmailOverrideText,
		`{{define "otp"}}DBTXT-{{.Code}}{{end}}`,
	))

	html, text, err := email.Render(env.Providers.Settings, "otp",
		email.EmailData{Code: "777"})
	require.NoError(t, err)
	require.True(t, strings.Contains(html, "DBHTML-777"))
	require.True(t, strings.Contains(text, "DBTXT-777"))
}

func TestRender_NilStoreUsesDefaults(t *testing.T) {
	html, text, err := email.Render(nil, "otp", email.EmailData{Code: "1"})
	require.NoError(t, err)
	require.True(t, strings.Contains(html, "1"))
	require.True(t, strings.Contains(text, "1"))
}

// --- SampleData ---

func TestSampleData_OTP(t *testing.T) {
	d := email.SampleData("otp", "https://example.com")
	require.Equal(t, "482917", d.Code)
}

func TestSampleData_PasswordReset(t *testing.T) {
	d := email.SampleData("password_reset", "https://example.com")
	require.True(t, strings.Contains(d.ResetURL, "https://example.com"))
}

func TestSampleData_GroupInvite(t *testing.T) {
	d := email.SampleData("group_invite", "https://example.com")
	require.Equal(t, "Engineering Team", d.GroupName)
	require.Equal(t, "bob", d.Inviter.Username)
}

func TestSampleData_UnknownName(t *testing.T) {
	d := email.SampleData("unknown", "https://example.com")
	require.Equal(t, "", d.Code)
	require.Equal(t, "", d.GroupName)
}

// --- High-level Send* helpers ---

func TestSendOTP_RoutesViaHook(t *testing.T) {
	var captured []byte
	s := &email.Sender{
		From: "test@example.com",
		Hook: func(_ []string, msg []byte) { captured = msg },
	}
	data := email.EmailData{
		InstanceName: "Cog",
		User:         email.UserInfo{Email: "alice@example.com"},
		Code:         "123456",
	}
	require.NoError(t, s.SendOTP(nil, data))
	msg := string(captured)
	require.True(t, strings.Contains(msg, "Subject: Your verification code"))
	require.True(t, strings.Contains(msg, "multipart/alternative"))
}

func TestSendPasswordReset_RoutesViaHook(t *testing.T) {
	var captured []byte
	s := &email.Sender{
		From: "test@example.com",
		Hook: func(_ []string, msg []byte) { captured = msg },
	}
	data := email.EmailData{
		InstanceName: "Cog",
		User:         email.UserInfo{Email: "alice@example.com"},
		ResetURL:     "https://cog.example/reset?token=x",
	}
	require.NoError(t, s.SendPasswordReset(nil, data))
	msg := string(captured)
	require.True(t, strings.Contains(msg, "Subject: Reset your password"))
}

func TestSendGroupInvite_RoutesViaHook(t *testing.T) {
	var captured []byte
	s := &email.Sender{
		From: "test@example.com",
		Hook: func(_ []string, msg []byte) { captured = msg },
	}
	data := email.EmailData{
		InstanceName: "Cog",
		User:         email.UserInfo{Email: "alice@example.com"},
		GroupName:    "Engineering",
		InviteURL:    "https://cog.example/invite/x",
	}
	require.NoError(t, s.SendGroupInvite(nil, data))
	msg := string(captured)
	require.True(t, strings.Contains(msg, "Engineering"))
	require.True(t, strings.Contains(msg, "Subject:"))
}

// --- DefaultHTML / DefaultText ---

func TestDefaultHTML_NonEmpty(t *testing.T) {
	require.True(t, len(email.DefaultHTML()) > 0)
}

func TestDefaultText_NonEmpty(t *testing.T) {
	require.True(t, len(email.DefaultText()) > 0)
}
