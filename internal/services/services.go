// Package services constructs the runtime dependency graph for Cog.
package services

import (
	"context"
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
	"git.duckfam.us/jj-software/cog/internal/search"
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
	Access       *volume.Access
	Settings     *settings.Store
	Email        *email.Sender
	Search       *search.Manager
	CSRFSecret   []byte
}

// New opens the system database and constructs all service providers.
// It sets up the search manager with a hooked system DB for real-time indexing.
func New(cfg config.Config) (*Providers, error) {
	mgr := search.NewManager(cfg.DataDir)

	// Open the system DB with update hook feeding the search collector.
	sysDB, err := db.OpenHooked(cfg.DBPath, mgr.SystemHook())
	if err != nil {
		return nil, fmt.Errorf("open system db: %w", err)
	}
	if err := db.Migrate(sysDB); err != nil {
		sysDB.Close()
		return nil, fmt.Errorf("migrate system db: %w", err)
	}

	mgr.SetSystemDB(sysDB)

	p := NewFromDB(cfg, sysDB)
	p.Search = mgr

	// Wire the DB hook factory so per-user plugin DBs get update hooks.
	p.Plugins.SetDBHookFactory(mgr.UserDBHook)
	mgr.SetUserDBFunc(p.Plugins.PluginDB)
	mgr.SetFilesDirFunc(p.Plugins.UserFilesDir)

	return p, nil
}

// NewFromDB constructs providers from an existing database connection.
// Useful for tests that use :memory: databases.
func NewFromDB(cfg config.Config, sysDB *sql.DB) *Providers {
	users := user.NewStore(sysDB)
	volumes := volume.NewStore(sysDB)
	mounts := mount.NewStore(sysDB)

	// Bootstrap default volume and root mounts for existing users.
	mount.Bootstrap(cfg.DataDir, users, volumes, mounts)

	secret := make([]byte, 32)
	rand.Read(secret)

	registry := plugin.NewRegistry(cfg.DataDir, http.NewServeMux(), sysDB, secret)

	// Wire mount resolver into plugin registry.
	resolver := mount.NewResolver(users, mounts, volumes)
	registry.SetMountResolver(resolver)

	filesPlugin := files.New(registry)
	shares := share.NewStore(sysDB)

	return &Providers{
		Config:       cfg,
		DB:           sysDB,
		Users:        users,
		Sessions:     auth.NewSessionStore(sysDB),
		AppPasswords: auth.NewAppPasswordStore(sysDB),
		Plugins:      registry,
		Shares:       shares,
		Files:        filesPlugin,
		Groups:       group.NewStore(sysDB),
		Invitations:  group.NewInvitationStore(sysDB),
		Volumes:      volumes,
		Mounts:       mounts,
		Access:       &volume.Access{Volumes: volumes, Shares: shares, DB: sysDB},
		Settings:     settings.NewStore(sysDB),
		Email:        email.NewFromConfig(cfg),
		CSRFSecret:   secret,
	}
}

// StartSearch starts the search manager after plugins have been registered.
// Should be called after web.New() which registers all plugins.
func (p *Providers) StartSearch(ctx context.Context) error {
	if p.Search == nil {
		return nil
	}
	// Collect plugin search source defs from the registry.
	for pluginID, defs := range p.Plugins.SearchSourceDefs() {
		for _, def := range defs {
			p.Search.RegisterPluginSource(pluginID, def)
		}
	}
	return p.Search.Start(ctx)
}

// Close releases resources held by the providers.
func (p *Providers) Close() error {
	if p.Search != nil {
		p.Search.Stop()
	}
	p.Plugins.Stop()
	return p.DB.Close()
}
