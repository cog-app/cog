// Package services constructs the runtime dependency graph for Cog.
package services

import (
	"database/sql"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// Providers holds all runtime services and the config that created them.
type Providers struct {
	Config       config.Config
	DB           *sql.DB
	Users        *user.Store
	Sessions     *auth.SessionStore
	AppPasswords *auth.AppPasswordStore
}

// New opens the system database and constructs all service providers.
func New(cfg config.Config) (*Providers, error) {
	sysDB, err := db.OpenSystem(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open system db: %w", err)
	}

	return &Providers{
		Config:       cfg,
		DB:           sysDB,
		Users:        user.NewStore(sysDB),
		Sessions:     auth.NewSessionStore(sysDB),
		AppPasswords: auth.NewAppPasswordStore(sysDB),
	}, nil
}

// NewFromDB constructs providers from an existing database connection.
// Useful for tests that use :memory: databases.
func NewFromDB(cfg config.Config, sysDB *sql.DB) *Providers {
	return &Providers{
		Config:       cfg,
		DB:           sysDB,
		Users:        user.NewStore(sysDB),
		Sessions:     auth.NewSessionStore(sysDB),
		AppPasswords: auth.NewAppPasswordStore(sysDB),
	}
}

// Close releases resources held by the providers.
func (p *Providers) Close() error {
	return p.DB.Close()
}
