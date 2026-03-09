// Package services constructs the runtime dependency graph for Cog.
package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/email"
	"git.duckfam.us/jj-software/cog/internal/group"

	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin/dashboard"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// Providers holds all runtime services and the config that created them.
type Providers struct {
	Config       config.Config
	Log          *slog.Logger
	DB           *sql.DB
	Users        *user.Store
	Sessions     *auth.SessionStore
	AppPasswords *auth.AppPasswordStore
	Plugins      *plugin.Registry
	Shares       *share.Store
	Groups       *group.Store
	Invitations  *group.InvitationStore
	Volumes      *volume.Store
	Mounts       *mount.Store
	Access       *volume.Access
	Settings     *settings.Store
	Search       *search.Manager
	CSRFSecret   []byte
}

// New opens the system database and constructs all service providers.
// It sets up the search manager with a hooked system DB for real-time indexing.
func New(log *slog.Logger, cfg config.Config, plugins ...plugin.Plugin) (*Providers, error) {
	mgr := search.NewManager(log, cfg.DataDir)

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

	p := NewFromDB(log, cfg, sysDB, plugins...)
	p.Search = mgr

	// Wire the DB hook factory so per-user plugin DBs get update hooks.
	p.Plugins.SetDBHookFactory(mgr.UserDBHook)
	mgr.SetUserDBFunc(p.Plugins.PluginDB)
	mgr.SetFilesDirFunc(p.Plugins.UserFilesDir)

	return p, nil
}

// NewFromDB constructs providers from an existing database connection.
// Useful for tests that use :memory: databases.
func NewFromDB(log *slog.Logger, cfg config.Config, sysDB *sql.DB, plugins ...plugin.Plugin) *Providers {
	// Ensure dashboard and files plugins are always present.
	var hasFiles, hasDash bool
	for _, p := range plugins {
		switch p.(type) {
		case *files.Plugin:
			hasFiles = true
		case *dashboard.Plugin:
			hasDash = true
		}
	}
	if !hasDash {
		plugins = append(plugins, dashboard.New())
	}
	if !hasFiles {
		plugins = append(plugins, files.New())
	}

	users := user.NewStore(sysDB)
	volumes := volume.NewStore(sysDB)
	mounts := mount.NewStore(sysDB)

	// Bootstrap default volume and root mounts for existing users.
	if err := mount.Bootstrap(cfg.DataDir, users, volumes, mounts); err != nil {
		log.Warn("mount bootstrap failed", "err", err)
	}

	secret := loadOrCreateSecret(sysDB)

	registry := plugin.NewRegistry(log, cfg.DataDir, http.NewServeMux(), sysDB, secret)
	for _, p := range plugins {
		registry.Add(p)
	}

	// Wire mount resolver into plugin registry.
	resolver := mount.NewResolver(users, mounts, volumes)
	registry.SetMountResolver(resolver)

	shares := share.NewStore(sysDB)

	return &Providers{
		Config:       cfg,
		Log:          log,
		DB:           sysDB,
		Users:        users,
		Sessions:     auth.NewSessionStore(sysDB),
		AppPasswords: auth.NewAppPasswordStore(sysDB),
		Plugins:      registry,
		Shares:       shares,
		Groups:       group.NewStore(sysDB),
		Invitations:  group.NewInvitationStore(sysDB),
		Volumes:      volumes,
		Mounts:       mounts,
		Access:       &volume.Access{Volumes: volumes, Shares: shares},
		Settings:     settings.NewStore(sysDB),
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

// EmailSender returns an *email.Sender built from the current SMTP settings
// in the database. Returns nil if SMTP is not configured.
func (p *Providers) EmailSender() *email.Sender {
	return email.NewFromSettings(p.Settings)
}

// Close releases resources held by the providers.
func (p *Providers) Close() error {
	if p.Search != nil {
		p.Search.Stop()
	}
	p.Plugins.Stop()
	return p.DB.Close()
}

const secretSettingKey = "csrf_secret"

// loadOrCreateSecret returns the persisted CSRF secret from the system_settings
// table, generating and storing a new one on first run.
func loadOrCreateSecret(db *sql.DB) []byte {
	var hexVal string
	err := db.QueryRow(`SELECT value FROM system_settings WHERE key = ?`, secretSettingKey).Scan(&hexVal)
	if err == nil {
		if b, err := hex.DecodeString(hexVal); err == nil && len(b) == 32 {
			return b
		}
	}

	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	hexVal = hex.EncodeToString(secret)
	db.Exec(`INSERT INTO system_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		secretSettingKey, hexVal)
	return secret
}
