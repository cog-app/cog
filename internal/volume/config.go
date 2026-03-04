package volume

import (
	"encoding/json"
	"fmt"
)

// WebDAVConfig holds authentication and connection details for a WebDAV volume.
type WebDAVConfig struct {
	URL      string `json:"url"`       // e.g. "https://nc.example.com/remote.php/dav/files/user"
	AuthType string `json:"auth_type"` // "basic" or "bearer"
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

// ParseWebDAVConfig parses a WebDAV config from JSON.
func ParseWebDAVConfig(configJSON string) (*WebDAVConfig, error) {
	var c WebDAVConfig
	if err := json.Unmarshal([]byte(configJSON), &c); err != nil {
		return nil, fmt.Errorf("parse webdav config: %w", err)
	}
	if c.URL == "" {
		return nil, fmt.Errorf("webdav config: url is required")
	}
	if c.AuthType != "" && c.AuthType != "basic" && c.AuthType != "bearer" {
		return nil, fmt.Errorf("webdav config: auth_type must be 'basic' or 'bearer'")
	}
	return &c, nil
}
