// Package email provides SMTP email sending for Cog.
package email

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log/slog"
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

// SendHook intercepts outgoing email. When set on a Sender, it is called
// instead of smtp.SendMail. Used by tests and dev tools to capture emails.
type SendHook func(to []string, msg []byte)

// Sender sends emails via SMTP.
type Sender struct {
	Host string
	Port int
	User string
	Pass string
	From string
	Hook SendHook // if set, called instead of smtp.SendMail
}

// NewFromSettings creates a Sender from the settings store. Returns nil if
// SMTP is not configured, or if its settings can't be read (which is logged):
// either way there is no way to send.
func NewFromSettings(s *settings.Store) *Sender {
	if s == nil {
		return nil
	}
	vals, err := s.GetMany(settings.KeySMTPHost, settings.KeySMTPFrom, settings.KeySMTPUser, settings.KeySMTPPass)
	if err != nil {
		slog.Error("email: read SMTP settings", "err", err)
		return nil
	}
	host, from := vals[settings.KeySMTPHost], vals[settings.KeySMTPFrom]
	if host == "" || from == "" {
		return nil
	}
	return &Sender{
		Host: host,
		Port: s.GetInt(settings.KeySMTPPort, 587),
		User: vals[settings.KeySMTPUser],
		Pass: vals[settings.KeySMTPPass],
		From: from,
	}
}

// sendRaw sends a raw message via SMTP, or calls Hook if set.
func (s *Sender) sendRaw(to []string, msg []byte) error {
	if s.Hook != nil {
		s.Hook(to, msg)
		return nil
	}
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	var auth smtp.Auth
	if s.User != "" {
		auth = smtp.PlainAuth("", s.User, s.Pass, s.Host)
	}
	return smtp.SendMail(addr, auth, s.From, to, msg)
}

// SendGroupInvite sends a group invitation email using templates.
func (s *Sender) SendGroupInvite(ss *settings.Store, data EmailData) error {
	htmlBody, textBody, err := Render(ss, "group_invite", data)
	if err != nil {
		return fmt.Errorf("render group_invite: %w", err)
	}
	subject := fmt.Sprintf("You've been invited to join %q", data.GroupName)
	return s.sendRaw([]string{data.User.Email}, buildMultipartAlternative(s.From, data.User.Email, subject, textBody, htmlBody))
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
		if err := w.SetBoundary(boundary); err != nil {
			return fmt.Errorf("send: %w", err)
		}

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

	return s.sendRaw(to, buf.Bytes())
}

// SendPasswordReset sends a password reset email using templates.
func (s *Sender) SendPasswordReset(ss *settings.Store, data EmailData) error {
	htmlBody, textBody, err := Render(ss, "password_reset", data)
	if err != nil {
		return fmt.Errorf("render password_reset: %w", err)
	}
	return s.sendRaw([]string{data.User.Email}, buildMultipartAlternative(s.From, data.User.Email, "Reset your password", textBody, htmlBody))
}

// SendOTP sends a one-time authentication code using templates.
func (s *Sender) SendOTP(ss *settings.Store, data EmailData) error {
	htmlBody, textBody, err := Render(ss, "otp", data)
	if err != nil {
		return fmt.Errorf("render otp: %w", err)
	}
	return s.sendRaw([]string{data.User.Email}, buildMultipartAlternative(s.From, data.User.Email, "Your verification code", textBody, htmlBody))
}

// buildMultipartAlternative constructs a MIME multipart/alternative message
// with text/plain and text/html parts.
func buildMultipartAlternative(from, to, subject, textBody, htmlBody string) []byte {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	textHeader := make(textproto.MIMEHeader)
	textHeader.Set("Content-Type", "text/plain; charset=utf-8")
	tw, _ := w.CreatePart(textHeader)
	tw.Write([]byte(textBody))

	htmlHeader := make(textproto.MIMEHeader)
	htmlHeader.Set("Content-Type", "text/html; charset=utf-8")
	hw, _ := w.CreatePart(htmlHeader)
	hw.Write([]byte(htmlBody))

	w.Close()

	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\n", from)
	fmt.Fprintf(&msg, "To: %s\r\n", to)
	fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	fmt.Fprintf(&msg, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&msg, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", w.Boundary())
	msg.Write(body.Bytes())
	return msg.Bytes()
}
