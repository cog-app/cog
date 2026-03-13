// Package config holds the serializable server configuration.
package config

// Config holds settings that can be loaded from flags, files, or environment.
type Config struct {
	Addr      string `json:"addr"`
	DBPath    string `json:"db_path"`
	BaseURL   string `json:"base_url"`
	DataDir   string `json:"data_dir"`   // Root directory for user data (e.g. /var/lib/cog/data)
	LogFormat string `json:"log_format"` // "text" (default) or "json"
	LogLevel  string `json:"log_level"`  // "debug", "info" (default), "warn", "error"

	SSHAddr        string `json:"ssh_addr"`          // env COG_SSH_ADDR, default ":2222"
	SSHEnabled     bool   `json:"ssh_enabled"`       // env COG_SSH_ENABLED, default true
	SSHHostKeyPath string `json:"ssh_host_key_path"` // env COG_SSH_HOST_KEY, default ""
}
