// Package services constructs the runtime dependency graph for Cog.
package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
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
	SSHKeys        *keys.Store
	PasswordResets *auth.PasswordResetStore
	Activities     *activity.Store
	DAV            *dav.Server
	JMAP           *jmap.Server    // JMAP over the DAV core's collections
	Push           *push.Store     // push subscriptions (WebDAV-Push; JMAP push)
	Pusher         *push.Deliverer // delivers to them; StartPush runs it
	Federation     *federation.Store
	FedClient      *federation.Client
	FedPolicy      *federation.Policy // whether, and with whom, this server federates
	EmailHook      email.SendHook     // if set, all email goes through this instead of SMTP
	RateLimiter    *ratelimit.Limiter
	// Guard checks every credential against guessing, over RateLimiter.
	Guard      *ratelimit.Guard
	CSRFSecret []byte
	Cookies    cogplugin.CookieConfig
	Clock      clock.Clock
	StartedAt  time.Time

	closeOnce sync.Once
	closeErr  error
}

// maxInvitationMailPerHour bounds the invitation email one user's calendar
// changes send to people outside Cog.
const maxInvitationMailPerHour = 100

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
		if cerr := sysDB.Close(); cerr != nil {
			log.Error("close system db after failed migration", "err", cerr)
		}
		return nil, fmt.Errorf("migrate system db: %w", err)
	}

	mgr.SetSystemDB(sysDB)

	p, err := NewFromDB(log, cfg, sysDB, clock.System(), plugins...)
	if err != nil {
		if cerr := sysDB.Close(); cerr != nil {
			log.Error("close system db after failed setup", "err", cerr)
		}
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
			if errors.Is(err, user.ErrNotFound) {
				return nil
			}
			if err != nil {
				log.Error("share recipients: look up user", "share", rec.ID, "user", rec.SubjectID, "err", err)
				return nil
			}
			return []string{u.Username}
		case "GROUP":
			userIDs, err := p.Groups.ListClosureUserIDs(rec.SubjectID)
			if err != nil {
				log.Error("share recipients: list group members", "share", rec.ID, "group", rec.SubjectID, "err", err)
				return nil
			}
			members, err := p.Users.GetByIDs(userIDs)
			if err != nil {
				log.Error("share recipients: look up members", "share", rec.ID, "group", rec.SubjectID, "err", err)
				return nil
			}
			names := make([]string, 0, len(members))
			for _, u := range members {
				names = append(names, u.Username)
			}
			return names
		case "INSTANCE":
			names, err := p.Users.Usernames()
			if err != nil {
				log.Error("share recipients: list users", "share", rec.ID, "err", err)
				return nil
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

	prop.LookupShare = func(id string) (*search.ShareRecord, error) {
		rec, err := p.Shares.Get(id)
		if errors.Is(err, share.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return searchShare(rec), nil
	}
	mgr.Propagator = prop

	// Every way of making or removing a share goes through the store, so
	// its change hook is the one place recipients' indexes follow shares.
	prev := p.Shares.OnChange
	p.Shares.OnChange = func(rec share.Record) {
		if prev != nil {
			prev(rec)
		}
		prop.OnShareChange(searchShare(&rec))
	}

	mgr.SharedAccess = func(username string, refs []search.SharedRef) (map[search.SharedRef]bool, error) {
		u, err := p.Users.GetByUsername(username)
		if err != nil {
			return nil, fmt.Errorf("shared results for %s: %w", username, err)
		}
		owned := make([]share.OwnedRef, len(refs))
		for i, r := range refs {
			owned[i] = share.OwnedRef(r)
		}
		readable, err := p.Shares.Readable(u.ID, owned)
		if err != nil {
			return nil, err
		}
		out := make(map[search.SharedRef]bool, len(readable))
		for r := range readable {
			out[search.SharedRef(r)] = true
		}
		return out, nil
	}
}

// searchShare is a share as the search propagator sees it.
func searchShare(rec *share.Record) *search.ShareRecord {
	return &search.ShareRecord{
		ID:           rec.ID,
		OwnerUserID:  rec.OwnerUserID,
		ResourceType: rec.ResourceType,
		ResourceID:   rec.ResourceID,
		SubjectType:  rec.SubjectType,
		SubjectID:    rec.SubjectID,
	}
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

	secret, err := loadSecret(sysDB, filepath.Join(cfg.DataDir, secretFileName))
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
	plugin.SetTrustedProcessPlugins(cfg.TrustedProcessPlugins)

	// The DAV core owns collections; plugins that hold DAV resources get it
	// before they register. web.New finishes wiring it.
	davServer := &dav.Server{Collections: dav.NewStore(sysDB, c), Clock: c, Log: log.With("component", "dav"),
		Secret: secret, MailLimit: ratelimit.NewShared(sysDB, "invitation-mail", maxInvitationMailPerHour, time.Hour, c)}

	// CSRF tokens use a key of their own, derived from the master secret,
	// because trusted process plugins are given it: a plugin can then mint
	// form tokens but not decrypt TOTP secrets or forge reset links.
	csrfKey := auth.DerivedKey(secret, "csrf")
	registry := plugin.NewRegistry(log, cfg.DataDir, http.NewServeMux(), sysDB, csrfKey, c)

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

	// Plugins keep directories in the data dir under their IDs, beside the
	// users' own.
	users.Reserve(registry.PluginIDs()...)
	bad, err := users.Invalid()
	if err != nil {
		return nil, err
	}
	for _, name := range bad {
		log.Error("existing username is not allowed for new accounts and may share a directory with another user or the server; rename or remove it", "username", name)
	}

	shares := share.NewStore(sysDB, c)
	access := &volume.Access{Volumes: volumes, Shares: shares, Users: users}

	// Wire mount resolver into plugin registry.
	resolver := mount.NewResolver(users, mounts, volumes, access)
	registry.SetMountResolver(resolver)

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
	settingsStore := settings.NewStore(sysDB)
	fedStore := federation.NewStore(sysDB, c)
	fedClient := federation.NewClient(cfg.BaseURL)
	sshKeys, err := keys.NewStore(sysDB, registry, users, c)
	if err != nil {
		return nil, err
	}

	limiter := ratelimit.NewShared(sysDB, "auth", 10, 15*time.Minute, c)
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
		Access:         access,
		Quotas:         quotas,
		Settings:       settingsStore,
		TOTP:           auth.NewTOTPStore(sysDB, secret),
		MFAPending:     auth.NewMFAPendingStore(sysDB, c, secret),
		EmailOTP:       auth.NewEmailOTPStore(sysDB, c),
		PasswordResets: auth.NewPasswordResetStore(sysDB, c, secret),
		WebAuthn:       newWebAuthnStore(sysDB, c, cfg.BaseURL, secret, log),
		SSHKeys:        sshKeys,
		RateLimiter:    limiter,
		Guard:          &ratelimit.Guard{Limiter: limiter, Delay: time.Second},
		Activities:     activities,
		DAV:            davServer,
		JMAP:           jmapServer,
		Push:           pushStore,
		Pusher:         pusher,
		Federation:     fedStore,
		FedClient:      fedClient,
		FedPolicy:      &federation.Policy{Settings: settingsStore, EnvPeers: cfg.FederationPeers},
		CSRFSecret:     csrfKey,
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
	for i, id := range pluginIDs {
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
// Close stops the background work and closes the database. Only the first
// call does anything, so a command and Main may both defer it.
func (p *Providers) Close() error {
	p.closeOnce.Do(func() {
		if p.Search != nil {
			p.Search.Stop()
		}
		p.Plugins.Stop()
		p.closeErr = p.DB.Close()
	})
	return p.closeErr
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
// secretFileName is the master secret's file in the data dir. It starts with
// a dot, so no username can take it.
const secretFileName = ".cog-secret"

// loadSecret returns the master secret, which encrypts TOTP secrets and
// keys reset links, CSRF tokens and attachment links. It lives in a file
// beside the data, not in the database it protects, so a copy of the
// database alone reveals none of those. A secret from before is moved out of
// system_settings; otherwise one is made. Back the file up with the
// database: without it, TOTP secrets cannot be read and links stop working.
func loadSecret(db *sql.DB, path string) ([]byte, error) {
	if b, err := readSecret(path); err == nil {
		return b, dropSetting(db, secretSettingKey)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var key []byte
	var hexVal string
	err := db.QueryRow(`SELECT value FROM system_settings WHERE key = ?`, secretSettingKey).Scan(&hexVal)
	switch {
	case err == nil:
		if key, err = hex.DecodeString(hexVal); err != nil || len(key) == 0 {
			return nil, fmt.Errorf("system setting %s is not a hex key", secretSettingKey)
		}
	case errors.Is(err, sql.ErrNoRows):
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate secret: %w", err)
		}
	default:
		return nil, fmt.Errorf("read system setting %s: %w", secretSettingKey, err)
	}
	// Created whole or not at all, and never over another process's: a
	// link fails if the name exists, and then that one is the secret.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cog-secret-*")
	if err != nil {
		return nil, fmt.Errorf("write secret: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
		return nil, errors.Join(fmt.Errorf("write secret: %w", err), tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("write secret: %w", err)
	}
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("store secret: %w", err)
	}
	b, err := readSecret(path)
	if err != nil {
		return nil, err
	}
	return b, dropSetting(db, secretSettingKey)
}

func readSecret(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read secret: %w", err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(b) < 16 {
		return nil, fmt.Errorf("%s does not hold a hex key", path)
	}
	return b, nil
}

func dropSetting(db *sql.DB, key string) error {
	if _, err := db.Exec(`DELETE FROM system_settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("remove system setting %s: %w", key, err)
	}
	return nil
}

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

// SignOutEverywhere ends every sign-in a password reset must end: the user's
// browser sessions and app passwords. Whoever had the old password may have
// minted an app password with it, which would otherwise outlive the reset.
// SSH keys and passkeys stay; the user reviews those in Settings.
func (p *Providers) SignOutEverywhere(userID string) error {
	return errors.Join(p.Sessions.DeleteAllForUser(userID), p.AppPasswords.DeleteAllForUser(userID))
}

// EmailVisible reports whether viewer may see the email address of the user
// with targetID: their own, any for an admin, and otherwise only that of
// someone they share a group with. Everyone else is known by name alone, so
// a signed-in user cannot collect every address on the server.
func (p *Providers) EmailVisible(viewer *user.User, targetID string) (bool, error) {
	if viewer == nil {
		return false, nil
	}
	if viewer.ID == targetID || viewer.IsAdmin {
		return true, nil
	}
	return p.Groups.ShareAGroup(viewer.ID, targetID)
}

// DeleteUser removes an account and everything it kept: its rows, its
// shares (through the share store, so what they fed is updated), its open
// database handles and search index, and its directories in the data dir.
// A later account of the same name starts empty (audit H9).
func (p *Providers) DeleteUser(id string) error {
	u, err := p.Users.GetByID(id)
	if err != nil {
		return err
	}
	var shareIDs []string
	for _, list := range []iter.Seq2[share.Record, error]{p.Shares.ListByOwner(id), p.Shares.ListBySubject(id)} {
		for rec, err := range list {
			if err != nil {
				return fmt.Errorf("list shares of %s: %w", u.Username, err)
			}
			if rec.OwnerUserID == id || rec.SubjectType == "USER" {
				shareIDs = append(shareIDs, rec.ID)
			}
		}
	}
	for _, sid := range shareIDs {
		if err := p.Shares.Delete(sid); err != nil && !errors.Is(err, share.ErrNotFound) {
			return fmt.Errorf("delete share %s: %w", sid, err)
		}
	}
	var federated []federation.FederatedShare
	for _, list := range []iter.Seq2[federation.FederatedShare, error]{p.Federation.ListInbound(id, ""), p.Federation.ListOutbound(id)} {
		for fs, err := range list {
			if err != nil {
				return fmt.Errorf("list federated shares of %s: %w", u.Username, err)
			}
			federated = append(federated, fs)
		}
	}
	if err := p.Users.Delete(id); err != nil {
		return err
	}
	p.endFederatedShares(federated)
	// The account is gone; what follows only frees what it left behind, so
	// it carries on past a failure and reports them all.
	errs := []error{p.Plugins.CloseUser(u.Username)}
	if p.Search != nil {
		errs = append(errs, p.Search.CloseUser(u.Username))
	}
	dirs := []string{filepath.Join(p.Config.DataDir, u.Username)}
	for _, pluginID := range p.Plugins.PluginIDs() {
		dirs = append(dirs, filepath.Join(p.Config.DataDir, pluginID, u.Username))
	}
	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", dir, err))
		}
	}
	return errors.Join(errs...)
}

// endFederatedShares tells each remote server that a deleted user's share
// with it has ended, so the remote drops its mount or its copy of the
// secret instead of keeping a share that now leads nowhere. A remote that
// cannot be reached is logged: the share is already gone here, and its
// secret no longer opens anything.
func (p *Providers) endFederatedShares(shares []federation.FederatedShare) {
	for _, fs := range shares {
		if fs.RemoteHost == "" || fs.State == "rejected" || fs.State == "removed" {
			continue
		}
		notif := federation.OCMNotification{
			NotificationType: federation.NotifyShareUnshared,
			ResourceType:     fs.ResourceType,
			ProviderID:       fs.RemoteID,
			SharedSecret:     fs.Token,
		}
		if fs.Direction == "inbound" && fs.State == "pending" {
			notif.NotificationType = federation.NotifyShareDeclined
		}
		go func() {
			if err := p.FedClient.Notify(context.Background(), fs.RemoteHost, notif); err != nil {
				p.Log.Warn("notify remote of deleted user's federated share", "remote", fs.RemoteHost, "share", fs.ID, "err", err)
			}
		}()
	}
}
