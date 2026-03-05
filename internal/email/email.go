// Package email provides SMTP email sending for Cog.
package email

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/smtp"
	"net/textproto"

	"git.duckfam.us/jj-software/cog/internal/config"
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

// NewFromConfig creates a Sender from config. Returns nil if SMTP is not configured.
func NewFromConfig(cfg config.Config) *Sender {
	if !cfg.SMTPConfigured() {
		return nil
	}
	return &Sender{
		Host: cfg.SMTPHost,
		Port: cfg.SMTPPort,
		User: cfg.SMTPUser,
		Pass: cfg.SMTPPass,
		From: cfg.SMTPFrom,
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
