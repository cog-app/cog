// Package email provides SMTP email sending for Cog.
package email

import (
	"fmt"
	"net/smtp"

	"git.duckfam.us/jj-software/cog/internal/config"
)

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
