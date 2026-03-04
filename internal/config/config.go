// Package config holds the serializable server configuration.
package config

// Config holds settings that can be loaded from flags, files, or environment.
type Config struct {
	Addr    string `json:"addr"`
	DBPath  string `json:"db_path"`
	BaseURL string `json:"base_url"`
	DataDir string `json:"data_dir"` // Root directory for user data (e.g. /var/lib/cog/data)

	// SMTP settings for sending invitation emails.
	SMTPHost string `json:"smtp_host"`
	SMTPPort int    `json:"smtp_port"`
	SMTPUser string `json:"smtp_user"`
	SMTPPass string `json:"smtp_pass"`
	SMTPFrom string `json:"smtp_from"`
}

// SMTPConfigured returns true if SMTP settings are present.
func (c Config) SMTPConfigured() bool {
	return c.SMTPHost != "" && c.SMTPFrom != ""
}
