// Package services constructs the runtime dependency graph for Cog.
package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/activity"
	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/email"
	"git.duckfam.us/jj-software/cog/internal/federation"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/jmap"
	"git.duckfam.us/jj-software/cog/internal/logbuf"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/push"
	"git.duckfam.us/jj-software/cog/internal/quota"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/safehttp"
	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/internal/webpush"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/dashboard"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// Providers holds all runtime services and the config that created them.
type Providers struct {
	Config         config.Config
	Log            *slog.Logger
	DB             *sql.DB
	Users          *user.Store
	Sessions       *auth.SessionStore
	AppPasswords   *auth.AppPasswordStore
	Plugins        *plugin.Registry
	Shares         *share.Store
	Groups         *group.Store
	Invitations    *group.InvitationStore
	Volumes        *volume.Store
	Mounts         *mount.Store
	Access         *volume.Access
	Quotas         *quota.Store
	Settings       *settings.Store
	Search         *search.Manager
	LogBuffer      *logbuf.Buffer
	TOTP           *auth.TOTPStore
	MFAPending     *auth.MFAPendingStore
	EmailOTP       *auth.EmailOTPStore
	WebAuthn       *auth.WebAuthnStore
	PasswordResets *auth.PasswordResetStore
	Activities     *activity.Store
	DAV            *dav.Server
	JMAP           *jmap.Server    // JMAP over the DAV core's collections
	Push           *push.Store     // push subscriptions (WebDAV-Push; JMAP push)
	Pusher         *push.Deliverer // delivers to them; StartPush runs it
	Federation     *federation.Store
	FedClient      *federation.Client
	EmailHook      email.SendHook // if set, all email goes through this instead of SMTP
	RateLimiter    *ratelimit.Limiter
	CSRFSecret     []byte
	Cookies        cogplugin.CookieConfig
	Clock          clock.Clock
	StartedAt      time.Time
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

	p, err := NewFromDB(log, cfg, sysDB, clock.System(), plugins...)
	if err != nil {
		sysDB.Close()
		return nil, err
	}
	WireSearch(log, p, mgr)

	return p, nil
}

// WireSearch connects a search manager to the providers it needs: the hook
// that nudges indexing, the accessors for per-user databases and files, and
// the propagator that pushes shared resources into recipients' indices.
//
// Both the server and the test harness call this, so a path one has is a path
// the other can reach. Each piece fails quietly when missing: no hook factory
// means indexing waits for the periodic sweep, and no files directory means
// content that lives on disk is skipped.
func WireSearch(log *slog.Logger, p *Providers, mgr *search.Manager) {
	p.Search = mgr

	p.Plugins.SetDBHookFactory(mgr.UserDBHook)
	mgr.SetUserDBFunc(p.Plugins.PluginDB)
	mgr.SetFilesDirFunc(p.Plugins.UserFilesDir)

	prop := search.NewPropagator(log, mgr)
	prop.LookupUsername = func(userID string) (string, error) {
		u, err := p.Users.GetByID(userID)
		if err != nil {
			return "", err
		}
		return u.Username, nil
	}
	prop.ResolveRecipientUsernames = func(rec *search.ShareRecord) []string {
		switch rec.SubjectType {
		case "USER":
			u, err := p.Users.GetByID(rec.SubjectID)
			if err != nil {
				return nil
			}
			return []string{u.Username}
		case "GROUP":
			userIDs, err := p.Groups.ListClosureUserIDs(rec.SubjectID)
			if err != nil {
				return nil
			}
			var names []string
			for _, id := range userIDs {
				u, err := p.Users.GetByID(id)
				if err != nil {
					continue
				}
				names = append(names, u.Username)
			}
			return names
		case "INSTANCE":
			var names []string
			for u, err := range p.Users.List() {
				if err != nil {
					break
				}
				names = append(names, u.Username)
			}
			return names
		default:
			return nil
		}
	}
	prop.ResolveSourceName = func(resourceType string) (string, bool) {
		def, ok := p.Plugins.ResourceDefByType(resourceType)
		if !ok || def.Table == "" {
			return "", false
		}
		pluginID, _, found := strings.Cut(resourceType, ":")
		if !found {
			return "", false
		}
		return pluginID + ":" + def.Table, true
	}

	mgr.Propagator = prop
}

// NewFromDB constructs providers from an existing database connection.
// Useful for tests that use :memory: databases.
func NewFromDB(log *slog.Logger, cfg config.Config, sysDB *sql.DB, c clock.Clock, plugins ...plugin.Plugin) (*Providers, error) {
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

	users := user.NewStore(sysDB, c)
	volumes := volume.NewStore(sysDB, c)
	mounts := mount.NewStore(sysDB, c)

	// Bootstrap default volume and root mounts for existing users.
	if err := mount.Bootstrap(cfg.DataDir, users, volumes, mounts); err != nil {
		log.Warn("mount bootstrap failed", "err", err)
	}

	secret, err := loadOrCreateKey(sysDB, secretSettingKey, func() ([]byte, error) {
		b := make([]byte, 32)
		_, err := rand.Read(b)
		return b, err
	})
	if err != nil {
		return nil, err
	}
	vapidKey, err := loadOrCreateKey(sysDB, vapidSettingKey, webpush.GenerateVAPIDKey)
	if err != nil {
		return nil, err
	}
	vapid, err := webpush.ParseVAPID(vapidKey, vapidSubject(cfg.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("VAPID key in system settings: %w", err)
	}

	ratelimit.SetTrustedProxies(cfg.TrustedProxies)
	federation.SetPeerAllowlist(cfg.FederationPeers)
	plugin.SetTrustedProcessPlugins(cfg.TrustedProcessPlugins)

	// The DAV core owns collections; plugins that hold DAV resources get it
	// before they register. web.New finishes wiring it.
	davServer := &dav.Server{Collections: dav.NewStore(sysDB, c), Clock: c, Log: log.With("component", "dav")}

	registry := plugin.NewRegistry(log, cfg.DataDir, http.NewServeMux(), sysDB, secret, c)

	// WebDAV-Push: every committed write to a collection's sync state, in
	// any plugin database, is noticed by the DAV core; the deliverer turns
	// what it noticed into pushes.
	pushStore := push.NewStore(sysDB, c)
	pusher := &push.Deliverer{
		Store:    pushStore,
		Sender:   &webpush.Sender{Client: safehttp.Client(30 * time.Second), VAPID: vapid, Now: c},
		Messages: map[push.Protocol]push.Messenger{push.WebDAV: davServer.PushMessage},
		Sources:  []func(context.Context) error{davServer.MarkPush},
		Cleanups: []func(context.Context) error{davServer.PushCleanup},
		Log:      log.With("component", "push"),
	}
	davServer.EnablePush(pushStore, pusher, vapid)
	jmapServer := &jmap.Server{DAV: davServer, Push: pushStore, Pusher: pusher, BaseURL: cfg.BaseURL,
		Calendars: cfg.JMAPCalendars, Clock: c, Log: log.With("component", "jmap")}
	pusher.Messages[push.JMAP] = jmapServer.PushMessage
	davServer.UserListeners = append(davServer.UserListeners, jmapServer.UsersChanged)
	registry.SetTxObserverFactory(davServer.SyncObserver)
	for _, p := range plugins {
		if dp, ok := p.(dav.Provider); ok {
			dp.SetDAV(davServer)
		}
		if err := registry.Add(p); err != nil {
			log.Error("register plugin", "plugin", p.ID(), "err", err)
		}
	}

	// Discover and load process plugins from PATH.
	if !cfg.DisableProcessPlugins {
		registry.DiscoverProcessPlugins()
	}

	// Wire mount resolver into plugin registry.
	resolver := mount.NewResolver(users, mounts, volumes)
	registry.SetMountResolver(resolver)

	shares := share.NewStore(sysDB, c)
	// A shared address book's cards reach its sharees' birthday calendars.
	shares.OnChange = func(rec share.Record) {
		if rec.ResourceType != dav.ShareType {
			return
		}
		id, err := strconv.ParseInt(rec.ResourceID, 10, 64)
		if err != nil {
			return // names no collection
		}
		if err := davServer.ShareChanged(rec.SubjectType, rec.SubjectID, dav.CollectionID(id)); err != nil {
			log.Error("birthday calendars after a share change", "share", rec.ID, "err", err)
		}
	}
	quotas := quota.NewStore(sysDB, c)
	activities := activity.NewStore(sysDB, c)
	fedStore := federation.NewStore(sysDB, c)
	fedClient := federation.NewClient(cfg.BaseURL)

	return &Providers{
		Config:         cfg,
		Log:            log,
		DB:             sysDB,
		Users:          users,
		Sessions:       auth.NewSessionStore(sysDB, c),
		AppPasswords:   auth.NewAppPasswordStore(sysDB, c),
		Plugins:        registry,
		Shares:         shares,
		Groups:         group.NewStore(sysDB),
		Invitations:    group.NewInvitationStore(sysDB, c),
		Volumes:        volumes,
		Mounts:         mounts,
		Access:         &volume.Access{Volumes: volumes, Shares: shares},
		Quotas:         quotas,
		Settings:       settings.NewStore(sysDB),
		TOTP:           auth.NewTOTPStore(sysDB, secret),
		MFAPending:     auth.NewMFAPendingStore(sysDB, c, secret),
		EmailOTP:       auth.NewEmailOTPStore(sysDB, c),
		PasswordResets: auth.NewPasswordResetStore(sysDB, c, secret),
		WebAuthn:       newWebAuthnStore(sysDB, c, cfg.BaseURL, secret, log),
		RateLimiter:    ratelimit.New(10, 15*time.Minute, c),
		Activities:     activities,
		DAV:            davServer,
		JMAP:           jmapServer,
		Push:           pushStore,
		Pusher:         pusher,
		Federation:     fedStore,
		FedClient:      fedClient,
		CSRFSecret:     secret,
		Cookies:        cogplugin.CookieConfigFromBaseURL(cfg.BaseURL),
		Clock:          c,
		StartedAt:      c(),
	}, nil
}

func (p *Providers) SeedPlugins(username string) error {
	type seeder interface {
		Seed(username string) error
	}

	pluginIDs := p.Plugins.PluginIDs()
	errs := make([]error, len(pluginIDs))

	// Seed plugin data for the admin user concurrently.
	var wg sync.WaitGroup
	for i, id := range p.Plugins.PluginIDs() {
		plug, ok := p.Plugins.Plugin(id)
		if !ok {
			continue
		}
		s, ok := plug.(seeder)
		if !ok {
			continue
		}
		wg.Go(func() {
			if err := s.Seed(username); err != nil {
				errs[i] = fmt.Errorf("seed: plugin failed id=%v: %w", id, err)
			}
		})
	}
	wg.Wait()

	return errors.Join(errs...)
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
	s := email.NewFromSettings(p.Settings)
	if s != nil && p.EmailHook != nil {
		s.Hook = p.EmailHook
	}
	return s
}

// Close releases resources held by the providers.
func (p *Providers) Close() error {
	if p.Search != nil {
		p.Search.Stop()
	}
	p.Plugins.Stop()
	return p.DB.Close()
}

// newWebAuthnStore creates a WebAuthn store, returning nil on error
// (e.g., when BaseURL is not a valid URL in test environments).
func newWebAuthnStore(db *sql.DB, c clock.Clock, baseURL string, secret []byte, log *slog.Logger) *auth.WebAuthnStore {
	if baseURL == "" {
		return nil
	}
	wa, err := auth.NewWebAuthnStore(db, c, baseURL, secret)
	if err != nil {
		log.Warn("webauthn init failed (passkeys disabled)", "err", err)
		return nil
	}
	return wa
}

// Keys kept in system_settings, hex-encoded.
const (
	secretSettingKey = "csrf_secret"
	// vapidSettingKey holds the VAPID private key (RFC 8292). Push
	// subscriptions are bound to its public key, so replacing it breaks
	// every client's until they register again.
	vapidSettingKey = "vapid_private_key"
)

// loadOrCreateKey returns a key persisted in system_settings, generating
// and storing one with gen on first run. Two processes starting at once
// both generate; the first to store wins, and both use what was stored.
func loadOrCreateKey(db *sql.DB, name string, gen func() ([]byte, error)) ([]byte, error) {
	var hexVal string
	err := db.QueryRow(`SELECT value FROM system_settings WHERE key = ?`, name).Scan(&hexVal)
	switch {
	case err == nil:
		b, err := hex.DecodeString(hexVal)
		if err != nil || len(b) == 0 {
			return nil, fmt.Errorf("system setting %s is not a hex key", name)
		}
		return b, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("read system setting %s: %w", name, err)
	}
	key, err := gen()
	if err != nil {
		return nil, fmt.Errorf("generate %s: %w", name, err)
	}
	if _, err := db.Exec(`INSERT INTO system_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`,
		name, hex.EncodeToString(key)); err != nil {
		return nil, fmt.Errorf("store %s: %w", name, err)
	}
	if err := db.QueryRow(`SELECT value FROM system_settings WHERE key = ?`, name).Scan(&hexVal); err != nil {
		return nil, fmt.Errorf("read system setting %s: %w", name, err)
	}
	return hex.DecodeString(hexVal)
}

// vapidSubject is the contact push services are given (RFC 8292 section
// 2.1): the server's URL, when it is an https one.
func vapidSubject(baseURL string) string {
	if strings.HasPrefix(baseURL, "https://") {
		return baseURL
	}
	return ""
}

// StartPush runs push delivery until ctx ends.
func (p *Providers) StartPush(ctx context.Context) {
	if p.Pusher != nil {
		go p.Pusher.Run(ctx)
	}
}
