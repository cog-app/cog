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
	"git.duckfam.us/jj-software/cog/internal/email"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/plugin/files"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
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
	Invitations  *group.InvitationStore
	Volumes      *volume.Store
	Mounts       *mount.Store
	Settings     *settings.Store
	Email        *email.Sender
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
	users := user.NewStore(sysDB)
	volumes := volume.NewStore(sysDB)
	mounts := mount.NewStore(sysDB)

	// Bootstrap default volume and root mounts for existing users.
	mount.Bootstrap(cfg.DataDir, users, volumes, mounts)

	secret := make([]byte, 32)
	rand.Read(secret)

	registry := plugin.NewRegistry(cfg.DataDir, mux, sysDB, secret)

	// Wire mount resolver into plugin registry.
	resolver := mount.NewResolver(users, mounts, volumes)
	registry.SetMountResolver(resolver)

	filesPlugin := files.New(registry)

	return &Providers{
		Config:       cfg,
		DB:           sysDB,
		Users:        users,
		Sessions:     auth.NewSessionStore(sysDB),
		AppPasswords: auth.NewAppPasswordStore(sysDB),
		Plugins:      registry,
		Shares:       share.NewStore(sysDB),
		Files:        filesPlugin,
		Groups:       group.NewStore(sysDB),
		Invitations:  group.NewInvitationStore(sysDB),
		Volumes:      volumes,
		Mounts:       mounts,
		Settings:     settings.NewStore(sysDB),
		Email:        email.NewFromConfig(cfg),
		CSRFSecret:   secret,
	}
}

// Close releases resources held by the providers.
func (p *Providers) Close() error {
	p.Plugins.Stop()
	return p.DB.Close()
}
