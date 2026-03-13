package plugin

import (
	"context"
	"database/sql"
	"errors"
	"iter"
	"log/slog"

	"git.duckfam.us/jj-software/cog/vfs"
)

// ErrQuotaExceeded is returned when a file upload would exceed the user's quota.
var ErrQuotaExceeded = errors.New("quota exceeded")

// PluginContext is the combined registration and service interface passed to
// Plugin.Register(). It embeds Registrar for route/resource/hook registration
// and adds service accessors that plugins use at runtime.
type PluginContext interface {
	Registrar

	// PluginDB returns the per-user plugin database, auto-scoped to the current plugin ID.
	PluginDB(username string) (*sql.DB, error)
	// UserFilesDir returns the path to the user's files directory on the local
	// filesystem. Deprecated: prefer UserVFS for mount-aware file access.
	UserFilesDir(username string) string
	// EnsureUserDirs creates the standard directory structure for a user.
	EnsureUserDirs(username string) error
	// DataDir returns the root data directory.
	DataDir() string
	// NavItems returns nav items from all enabled plugins.
	NavItems() []NavItem
	// Widgets returns widgets from all enabled plugins.
	Widgets() []Widget

	// CSRFSecret returns the CSRF secret for token generation.
	CSRFSecret() []byte

	// Logger returns a structured logger pre-tagged with the plugin ID.
	Logger() *slog.Logger
	// RootDB returns a plugin-global database (not per-user).
	RootDB() (*sql.DB, error)

	// UserVFS returns the mount-aware virtual filesystem for a user.
	// Returns nil if VFS is not available (e.g., gRPC plugin without VFS permission).
	UserVFS(username string) vfs.FS

	// QuotaCheck checks if a user has enough quota for additionalBytes.
	// Returns nil if within quota or quotas are not configured.
	// Returns ErrQuotaExceeded if the upload would exceed the user's quota.
	QuotaCheck(username string, additionalBytes int64) error

	// Optional service accessors — return nil if the service is unavailable.
	ShareStore() ShareStore
	EmailSender() EmailSender
	UserLookup() UserLookup
	GroupStore() GroupStore
	FilesAccess() FilesAccess
}

// UserInfo is a public version of the internal user type.
type UserInfo struct {
	ID          string
	Username    string
	DisplayName string
	IsAdmin     bool
}

// ShareRecord is a public version of the internal share record.
type ShareRecord struct {
	ID           string
	OwnerUserID  string
	ResourceType string
	ResourceID   string
	SubjectType  string
	SubjectID    string
	Permissions  uint32
}

// AccessDecision is the result of a permission check.
type AccessDecision struct {
	Allowed         bool
	EffectivePerms  uint32
	MatchedShareIDs []string
	OwnerUserID     string
}

// ShareStore is the interface for share operations that plugins need.
type ShareStore interface {
	ListBySubject(subjectID string) iter.Seq2[ShareRecord, error]
	CheckAccess(userID, resourceType, resourceID string) (*AccessDecision, error)
	Create(ownerUserID, resourceType, resourceID, subjectType, subjectID string, permissions uint32) (*ShareRecord, error)
	Delete(id string) error
}

// EmailSender is the interface for sending emails.
type EmailSender interface {
	Send(to []string, subject, body string, attachments ...EmailAttachment) error
	SendWithAttachments(to, subject, textBody, htmlBody string, attachments []EmailAttachment) error
}

// EmailAttachment is a file attached to an email.
type EmailAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// UserLookup provides user lookup operations.
type UserLookup interface {
	GetByID(id string) (*UserInfo, error)
	GetByUsername(username string) (*UserInfo, error)
}

// GroupInfo is a public version of the internal group type.
type GroupInfo struct {
	ID          string
	Name        string
	MemberCount int
}

// GroupStore provides group operations that plugins need.
type GroupStore interface {
	ListGroupsForUser(userID string) iter.Seq2[GroupInfo, error]
}

// FilesAccess provides file-system operations that plugins need.
type FilesAccess interface {
	List(username, dirPath string) ([]FileInfo, error)
	UserFilesDir(username string) string
}

// FileInfo is basic file metadata exposed to plugins.
type FileInfo struct {
	Path     string
	Name     string
	Size     int64
	MimeType string
	IsDir    bool
}

// CSRFTokenFromContext retrieves the CSRF token from the request context.
func CSRFTokenFromContext(ctx context.Context) string {
	s, _ := ctx.Value(csrfContextKey).(string)
	return s
}

// UserFromContext retrieves the authenticated user info from the request context.
// Returns nil if no user is authenticated.
func UserFromContext(ctx context.Context) *UserInfo {
	u, _ := ctx.Value(userContextKey).(*UserInfo)
	return u
}

type contextKeyType string

const csrfContextKey contextKeyType = "plugin_csrf_token"
const userContextKey contextKeyType = "plugin_user_info"

// WithCSRFToken returns a new context with the CSRF token set.
func WithCSRFToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, csrfContextKey, token)
}

// WithUserInfo returns a new context with the user info set.
func WithUserInfo(ctx context.Context, u *UserInfo) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}
