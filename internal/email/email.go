// Package email provides SMTP email sending for Cog.
package email

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/smtp"
	"net/textproto"

	"git.duckfam.us/jj-software/cog/internal/settings"
)

// Attachment is an email file attachment.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Sender sends emails via SMTP.
type Sender struct {
	Host string
	Port int
	User string
	Pass string
	From string
}

// NewFromSettings creates a Sender from the settings store. Returns nil if SMTP is not configured.
func NewFromSettings(s *settings.Store) *Sender {
	if s == nil {
		return nil
	}
	host, _ := s.Get(settings.KeySMTPHost)
	from, _ := s.Get(settings.KeySMTPFrom)
	if host == "" || from == "" {
		return nil
	}
	user, _ := s.Get(settings.KeySMTPUser)
	pass, _ := s.Get(settings.KeySMTPPass)
	return &Sender{
		Host: host,
		Port: s.GetInt(settings.KeySMTPPort, 587),
		User: user,
		Pass: pass,
		From: from,
	}
}

// SendGroupInvite sends a group invitation email.
func (s *Sender) SendGroupInvite(toEmail, groupName, inviterName, inviteURL string) error {
	subject := fmt.Sprintf("You've been invited to join %q on Cog", groupName)
	body := fmt.Sprintf(
		"Hi,\n\n%s has invited you to join the group %q on Cog.\n\nClick the link below to accept the invitation:\n%s\n\nThis invitation expires in 7 days.\n",
		inviterName, groupName, inviteURL,
	)

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		s.From, toEmail, subject, body)

	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	var auth smtp.Auth
	if s.User != "" {
		auth = smtp.PlainAuth("", s.User, s.Pass, s.Host)
	}

	return smtp.SendMail(addr, auth, s.From, []string{toEmail}, []byte(msg))
}

// Send sends an email with optional attachments. The body is plain text.
func (s *Sender) Send(to []string, subject, body string, attachments ...Attachment) error {
	var buf bytes.Buffer

	boundary := fmt.Sprintf("==cog_%x==", []byte(subject)[:min(8, len(subject))])

	hasAttachments := len(attachments) > 0

	// Headers
	fmt.Fprintf(&buf, "From: %s\r\n", s.From)
	for _, addr := range to {
		fmt.Fprintf(&buf, "To: %s\r\n", addr)
	}
	fmt.Fprintf(&buf, "Subject: %s\r\n", subject)
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")

	if !hasAttachments {
		fmt.Fprintf(&buf, "Content-Type: text/plain; charset=utf-8\r\n\r\n")
		buf.WriteString(body)
	} else {
		fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)

		w := multipart.NewWriter(&buf)
		w.SetBoundary(boundary)

		// Text part
		textHeader := make(textproto.MIMEHeader)
		textHeader.Set("Content-Type", "text/plain; charset=utf-8")
		tw, _ := w.CreatePart(textHeader)
		tw.Write([]byte(body))

		// Attachments
		for _, att := range attachments {
			attHeader := make(textproto.MIMEHeader)
			attHeader.Set("Content-Type", att.ContentType)
			attHeader.Set("Content-Transfer-Encoding", "base64")
			attHeader.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", att.Filename))
			aw, _ := w.CreatePart(attHeader)
			encoded := base64.StdEncoding.EncodeToString(att.Data)
			// Write in 76-char lines per MIME spec
			for i := 0; i < len(encoded); i += 76 {
				end := min(i+76, len(encoded))
				aw.Write([]byte(encoded[i:end]))
				aw.Write([]byte("\r\n"))
			}
		}

		w.Close()
	}

	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	var auth smtp.Auth
	if s.User != "" {
		auth = smtp.PlainAuth("", s.User, s.Pass, s.Host)
	}

	return smtp.SendMail(addr, auth, s.From, to, buf.Bytes())
}

// SendPasswordReset sends a password reset link to the given email address.
func (s *Sender) SendPasswordReset(toEmail, resetURL string) error {
	subject := "Reset your Cog password"
	body := fmt.Sprintf(
		"A password reset was requested for your Cog account.\n\nClick the link below to set a new password:\n%s\n\nThis link expires in 1 hour. If you didn't request this, you can safely ignore this email.\n",
		resetURL,
	)

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		s.From, toEmail, subject, body)

	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	var auth smtp.Auth
	if s.User != "" {
		auth = smtp.PlainAuth("", s.User, s.Pass, s.Host)
	}

	return smtp.SendMail(addr, auth, s.From, []string{toEmail}, []byte(msg))
}

// SendOTP sends a one-time authentication code to the given email address.
func (s *Sender) SendOTP(toEmail, code string) error {
	subject := "Your Cog verification code"
	body := fmt.Sprintf(
		"Your Cog verification code is:\n\n    %s\n\nThis code expires in 10 minutes. If you didn't request this, you can safely ignore this email.\n",
		code,
	)

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		s.From, toEmail, subject, body)

	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	var auth smtp.Auth
	if s.User != "" {
		auth = smtp.PlainAuth("", s.User, s.Pass, s.Host)
	}

	return smtp.SendMail(addr, auth, s.From, []string{toEmail}, []byte(msg))
}
