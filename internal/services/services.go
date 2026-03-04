// Package services constructs the runtime dependency graph for Cog.
package services

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/plugin/files"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// Providers holds all runtime services and the config that created them.
type Providers struct {
	Config       config.Config
	DB           *sql.DB
	Users        *user.Store
	Sessions     *auth.SessionStore
	AppPasswords *auth.AppPasswordStore
	Plugins      *plugin.Registry
	Shares       *share.Store
	Files        *files.Plugin
	Groups       *group.Store
	CSRFSecret   []byte
}

// New opens the system database and constructs all service providers.
func New(cfg config.Config) (*Providers, error) {
	sysDB, err := db.OpenSystem(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open system db: %w", err)
	}

	return newFromDB(cfg, sysDB, http.NewServeMux()), nil
}

// NewFromDB constructs providers from an existing database connection.
// Useful for tests that use :memory: databases.
func NewFromDB(cfg config.Config, sysDB *sql.DB) *Providers {
	return newFromDB(cfg, sysDB, http.NewServeMux())
}

// NewFromDBWithMux constructs providers with a specific ServeMux.
func NewFromDBWithMux(cfg config.Config, sysDB *sql.DB, mux *http.ServeMux) *Providers {
	return newFromDB(cfg, sysDB, mux)
}

func newFromDB(cfg config.Config, sysDB *sql.DB, mux *http.ServeMux) *Providers {
	registry := plugin.NewRegistry(cfg.DataDir, mux)
	filesPlugin := files.New(registry)

	secret := make([]byte, 32)
	rand.Read(secret)

	return &Providers{
		Config:       cfg,
		DB:           sysDB,
		Users:        user.NewStore(sysDB),
		Sessions:     auth.NewSessionStore(sysDB),
		AppPasswords: auth.NewAppPasswordStore(sysDB),
		Plugins:      registry,
		Shares:       share.NewStore(sysDB),
		Files:        filesPlugin,
		Groups:       group.NewStore(sysDB),
		CSRFSecret:   secret,
	}
}

// Close releases resources held by the providers.
func (p *Providers) Close() error {
	p.Plugins.Stop()
	return p.DB.Close()
}
