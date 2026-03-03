// Package config holds the serializable server configuration.
package config

// Config holds settings that can be loaded from flags, files, or environment.
type Config struct {
	Addr    string `json:"addr"`
	DBPath  string `json:"db_path"`
	BaseURL string `json:"base_url"`
}
