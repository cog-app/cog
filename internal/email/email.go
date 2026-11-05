// Package email provides SMTP email sending for Cog.
package email

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"

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
	return s.SendMessage(Message{To: []string{data.User.Email}, Subject: subject, Text: textBody, HTML: htmlBody})
}

// Send sends an email with optional attachments. The body is plain text.
func (s *Sender) Send(to []string, subject, body string, attachments ...Attachment) error {
	return s.SendMessage(Message{To: to, Subject: subject, Text: body, Attachments: attachments})
}

// Message is an email to send. Its fields are values, not header text: the
// sender validates the addresses and encodes the rest, so nothing in them
// can add a header or a recipient.
type Message struct {
	To          []string
	Subject     string
	Text        string
	HTML        string // optional alternative to Text
	Attachments []Attachment
}

// SendMessage sends m.
func (s *Sender) SendMessage(m Message) error {
	raw, to, err := s.build(m)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return s.sendRaw(to, raw)
}

// build renders m as an RFC 5322 message, returning it with the bare
// recipient addresses for the SMTP envelope.
func (s *Sender) build(m Message) ([]byte, []string, error) {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return nil, nil, fmt.Errorf("from address %q: %w", s.From, err)
	}
	if len(m.To) == 0 {
		return nil, nil, errors.New("no recipients")
	}
	to := make([]string, len(m.To))
	toHeader := make([]string, len(m.To))
	for i, addr := range m.To {
		a, err := mail.ParseAddress(addr)
		if err != nil {
			return nil, nil, fmt.Errorf("recipient %q: %w", addr, err)
		}
		to[i], toHeader[i] = a.Address, formatAddress(a)
	}

	var buf bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, v) }
	header("From", formatAddress(from))
	header("To", strings.Join(toHeader, ", "))
	header("Subject", encodeHeader(m.Subject))
	header("MIME-Version", "1.0")

	body, contentType, err := buildBody(m)
	if err != nil {
		return nil, nil, err
	}
	header("Content-Type", contentType)
	buf.WriteString("\r\n")
	buf.Write(body)
	return buf.Bytes(), to, nil
}

// formatAddress writes a as a header value, as a bare address when it has no
// name.
func formatAddress(a *mail.Address) string {
	if a.Name == "" {
		return a.Address
	}
	return a.String()
}

// encodeHeader makes s safe as an unstructured header value: line breaks
// become spaces, and anything outside printable ASCII is RFC 2047 encoded.
func encodeHeader(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '\r' || r == '\n' }), " ")
	return mime.QEncoding.Encode("utf-8", s)
}

// buildBody renders m's parts, returning them with the Content-Type that
// introduces them: text alone, text with an HTML alternative, and either of
// those followed by attachments in a multipart/mixed.
func buildBody(m Message) ([]byte, string, error) {
	var buf bytes.Buffer
	textType := "text/plain; charset=utf-8"
	if m.HTML == "" && len(m.Attachments) == 0 {
		buf.WriteString(m.Text)
		return buf.Bytes(), textType, nil
	}
	w := multipart.NewWriter(&buf)
	parts := w
	contentType := "multipart/alternative; boundary=" + w.Boundary()
	if len(m.Attachments) > 0 {
		contentType = "multipart/mixed; boundary=" + w.Boundary()
	}
	if m.HTML != "" && len(m.Attachments) > 0 {
		// The alternatives nest inside the mixed part as one part.
		var alt bytes.Buffer
		parts = multipart.NewWriter(&alt)
		if err := writeAlternatives(parts, m.Text, m.HTML); err != nil {
			return nil, "", err
		}
		pw, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {"multipart/alternative; boundary=" + parts.Boundary()}})
		if err != nil {
			return nil, "", fmt.Errorf("alternative part: %w", err)
		}
		if _, err := pw.Write(alt.Bytes()); err != nil {
			return nil, "", fmt.Errorf("alternative part: %w", err)
		}
	} else if m.HTML != "" {
		if err := writeAlternatives(w, m.Text, m.HTML); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), contentType, nil
	} else if err := writePart(w, textType, nil, []byte(m.Text)); err != nil {
		return nil, "", err
	}
	for _, att := range m.Attachments {
		if err := writeAttachment(w, att); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("close multipart: %w", err)
	}
	return buf.Bytes(), contentType, nil
}

// writeAlternatives writes text and html as the parts of w, then closes it.
func writeAlternatives(w *multipart.Writer, text, html string) error {
	if err := writePart(w, "text/plain; charset=utf-8", nil, []byte(text)); err != nil {
		return err
	}
	if err := writePart(w, "text/html; charset=utf-8", nil, []byte(html)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close alternatives: %w", err)
	}
	return nil
}

// writeAttachment writes att base64 encoded, in 76-character lines.
func writeAttachment(w *multipart.Writer, att Attachment) error {
	mediaType, params, err := mime.ParseMediaType(att.ContentType)
	if err != nil {
		return fmt.Errorf("attachment %q content type: %w", att.Filename, err)
	}
	contentType := mime.FormatMediaType(mediaType, params)
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": att.Filename})
	if contentType == "" || disposition == "" {
		return fmt.Errorf("attachment %q: cannot encode its headers", att.Filename)
	}
	encoded := base64.StdEncoding.EncodeToString(att.Data)
	var body bytes.Buffer
	for i := 0; i < len(encoded); i += 76 {
		body.WriteString(encoded[i:min(i+76, len(encoded))])
		body.WriteString("\r\n")
	}
	return writePart(w, contentType, textproto.MIMEHeader{
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {disposition},
	}, body.Bytes())
}

func writePart(w *multipart.Writer, contentType string, h textproto.MIMEHeader, body []byte) error {
	if h == nil {
		h = textproto.MIMEHeader{}
	}
	h.Set("Content-Type", contentType)
	pw, err := w.CreatePart(h)
	if err != nil {
		return fmt.Errorf("create %s part: %w", contentType, err)
	}
	if _, err := pw.Write(body); err != nil {
		return fmt.Errorf("write %s part: %w", contentType, err)
	}
	return nil
}

// SendPasswordReset sends a password reset email using templates.
func (s *Sender) SendPasswordReset(ss *settings.Store, data EmailData) error {
	htmlBody, textBody, err := Render(ss, "password_reset", data)
	if err != nil {
		return fmt.Errorf("render password_reset: %w", err)
	}
	return s.SendMessage(Message{To: []string{data.User.Email}, Subject: "Reset your password", Text: textBody, HTML: htmlBody})
}

// SendOTP sends a one-time authentication code using templates.
func (s *Sender) SendOTP(ss *settings.Store, data EmailData) error {
	htmlBody, textBody, err := Render(ss, "otp", data)
	if err != nil {
		return fmt.Errorf("render otp: %w", err)
	}
	return s.SendMessage(Message{To: []string{data.User.Email}, Subject: "Your verification code", Text: textBody, HTML: htmlBody})
}
