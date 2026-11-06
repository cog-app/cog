package email

import (
	"bytes"
	_ "embed"
	"fmt"
	htmltpl "html/template"
	"strings"
	texttpl "text/template"

	"git.duckfam.us/jj-software/cog/internal/settings"
)

//go:embed email_html.tmpl
var defaultHTMLTmpl string

//go:embed email_text.tmpl
var defaultTextTmpl string

// EmailData is the data passed to all email templates.
type EmailData struct {
	// Instance info
	BaseURL      string
	InstanceName string

	// Recipient
	User UserInfo

	// group_invite fields
	GroupName string
	Inviter   UserInfo
	InviteURL string

	// password_reset fields
	ResetURL string

	// otp fields
	Code string

	// email_verify fields; User.Email is the address being confirmed
	VerifyURL string
}

// UserInfo holds user details for email templates.
type UserInfo struct {
	Username    string
	DisplayName string
	Email       string
	AvatarURL   string
}

// Render renders a named email template using embedded defaults plus any
// DB-stored overrides. Returns (htmlBody, textBody, error).
func Render(s *settings.Store, name string, data EmailData) (string, string, error) {
	var htmlOverride, textOverride string
	if s != nil {
		vals, err := s.GetMany(settings.KeyEmailOverrideHTML, settings.KeyEmailOverrideText)
		if err != nil {
			return "", "", fmt.Errorf("read template overrides: %w", err)
		}
		htmlOverride, textOverride = vals[settings.KeyEmailOverrideHTML], vals[settings.KeyEmailOverrideText]
	}
	return render(name, data, htmlOverride, textOverride)
}

// RenderDefault renders using only embedded defaults (no DB overrides).
func RenderDefault(name string, data EmailData) (string, string, error) {
	return render(name, data, "", "")
}

// RenderWithOverrides renders using provided override strings (no DB lookup).
func RenderWithOverrides(name string, data EmailData, htmlOverride, textOverride string) (string, string, error) {
	return render(name, data, htmlOverride, textOverride)
}

func render(name string, data EmailData, htmlOverride, textOverride string) (string, string, error) {
	htmlBody, err := renderHTML(name, data, htmlOverride)
	if err != nil {
		return "", "", fmt.Errorf("html: %w", err)
	}
	textBody, err := renderText(name, data, textOverride)
	if err != nil {
		return "", "", fmt.Errorf("text: %w", err)
	}
	return htmlBody, textBody, nil
}

func renderHTML(name string, data EmailData, override string) (string, error) {
	// Parse defaults with a stub content func (required at parse time).
	tpl, err := htmltpl.New("").Funcs(htmltpl.FuncMap{
		"content": func() string { return "" },
	}).Parse(defaultHTMLTmpl)
	if err != nil {
		return "", fmt.Errorf("parse default html: %w", err)
	}

	// Apply override on top (replaces matching {{define}} blocks).
	if override != "" {
		if _, err := tpl.Parse(override); err != nil {
			return "", fmt.Errorf("parse html override: %w", err)
		}
	}

	// Inject real content func that renders the named sub-template.
	var buf bytes.Buffer
	tpl.Funcs(htmltpl.FuncMap{
		"content": func() htmltpl.HTML {
			var inner bytes.Buffer
			tpl.ExecuteTemplate(&inner, name, data)
			return htmltpl.HTML(inner.String())
		},
	})

	if err := tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		return "", fmt.Errorf("execute layout: %w", err)
	}
	return buf.String(), nil
}

func renderText(name string, data EmailData, override string) (string, error) {
	tpl, err := texttpl.New("").Funcs(texttpl.FuncMap{
		"content": func() string { return "" },
	}).Parse(defaultTextTmpl)
	if err != nil {
		return "", fmt.Errorf("parse default text: %w", err)
	}

	if override != "" {
		if _, err := tpl.Parse(override); err != nil {
			return "", fmt.Errorf("parse text override: %w", err)
		}
	}

	var buf bytes.Buffer
	tpl.Funcs(texttpl.FuncMap{
		"content": func() string {
			var inner bytes.Buffer
			tpl.ExecuteTemplate(&inner, name, data)
			return inner.String()
		},
	})

	if err := tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		return "", fmt.Errorf("execute layout: %w", err)
	}
	return strings.TrimSpace(buf.String()) + "\n", nil
}

// SampleData returns EmailData with realistic sample values for previewing.
func SampleData(name, baseURL string) EmailData {
	d := EmailData{
		BaseURL:      baseURL,
		InstanceName: "Cog",
		User: UserInfo{
			Username:    "alice",
			DisplayName: "Alice Johnson",
			Email:       "alice@example.com",
			AvatarURL:   baseURL + "/avatar/sample",
		},
	}
	switch name {
	case "group_invite":
		d.GroupName = "Engineering Team"
		d.Inviter = UserInfo{
			Username:    "bob",
			DisplayName: "Bob Smith",
			Email:       "bob@example.com",
			AvatarURL:   baseURL + "/avatar/sample2",
		}
		d.InviteURL = baseURL + "/invite/abc123"
	case "password_reset":
		d.ResetURL = baseURL + "/reset-password?token=abc123"
	case "otp":
		d.Code = "482917"
	case "email_verify":
		d.VerifyURL = baseURL + "/settings/email/verify?token=abc123"
	}
	return d
}

// DefaultHTML returns the embedded HTML template source.
func DefaultHTML() string { return defaultHTMLTmpl }

// DefaultText returns the embedded text template source.
func DefaultText() string { return defaultTextTmpl }

// ValidateHTMLOverride checks if an HTML override template is valid.
func ValidateHTMLOverride(override string) (*htmltpl.Template, error) {
	tpl, err := htmltpl.New("").Funcs(htmltpl.FuncMap{
		"content": func() htmltpl.HTML { return "" },
	}).Parse(defaultHTMLTmpl)
	if err != nil {
		return nil, err
	}
	_, err = tpl.Parse(override)
	return tpl, err
}

// ValidateTextOverride checks if a text override template is valid.
func ValidateTextOverride(override string) (*texttpl.Template, error) {
	tpl, err := texttpl.New("").Funcs(texttpl.FuncMap{
		"content": func() string { return "" },
	}).Parse(defaultTextTmpl)
	if err != nil {
		return nil, err
	}
	_, err = tpl.Parse(override)
	return tpl, err
}
