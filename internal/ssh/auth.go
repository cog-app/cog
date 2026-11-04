package ssh

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
	"git.duckfam.us/jj-software/cog/internal/user"

	gossh "golang.org/x/crypto/ssh"
)

// authCallbacks holds dependencies for SSH authentication.
type authCallbacks struct {
	users        *user.Store
	appPasswords appPasswordValidator
	keys         *keys.Store
	// limiter, when set, throttles password guessing per client address
	// and per account, as the web login does. A nil limiter allows every
	// attempt.
	limiter *ratelimit.Limiter
}

// errRateLimited refuses a password attempt from a client, or for an account,
// that has failed too often.
var errRateLimited = errors.New("too many failed attempts")

// appPasswordValidator validates app password tokens.
type appPasswordValidator interface {
	Validate(token string) (userID string, err error)
}

// passwordCallback authenticates via user password or app password.
func (a *authCallbacks) passwordCallback(conn gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
	if a.limiter == nil {
		return a.checkPassword(conn, password)
	}
	ip := remoteIP(conn.RemoteAddr())
	account := "ssh-user:" + strings.ToLower(conn.User())
	if a.limiter.IsLimited(ip) || a.limiter.IsLimited(account) {
		return nil, errRateLimited
	}
	perms, err := a.checkPassword(conn, password)
	if err != nil {
		a.limiter.RecordFailure(ip)
		a.limiter.RecordFailure(account)
		return nil, err
	}
	a.limiter.Reset(ip)
	a.limiter.Reset(account)
	return perms, nil
}

// remoteIP is the address part of a client's address, the key its failures
// are counted under.
func remoteIP(addr net.Addr) string {
	if host, _, err := net.SplitHostPort(addr.String()); err == nil {
		return host
	}
	return addr.String()
}

func (a *authCallbacks) checkPassword(conn gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
	// Try user password first.
	u, err := a.users.Authenticate(conn.User(), string(password))
	if err != nil && !errors.Is(err, user.ErrAuthFailed) {
		// A lookup failure, not a wrong password: don't let it pass for one.
		return nil, fmt.Errorf("authenticate %q: %w", conn.User(), err)
	}
	if err == nil && u.MFAMethod != "" {
		// A password is one factor of this account's two; SSH asks for
		// a key or an app password instead.
		err = user.ErrAuthFailed
	}
	if err == nil {
		return &gossh.Permissions{
			Extensions: map[string]string{
				"user_id":  u.ID,
				"username": u.Username,
			},
		}, nil
	}

	// Fall back to app password.
	userID, err := a.appPasswords.Validate(string(password))
	if err != nil {
		return nil, err
	}

	u, err = a.users.GetByID(userID)
	if err != nil {
		return nil, err
	}

	// Verify that the app password belongs to the user logging in.
	if u.Username != conn.User() {
		return nil, fmt.Errorf("app password does not belong to user %q", conn.User())
	}

	return &gossh.Permissions{
		Extensions: map[string]string{
			"user_id":  u.ID,
			"username": u.Username,
		},
	}, nil
}

// publicKeyCallback authenticates via SSH public key.
func (a *authCallbacks) publicKeyCallback(conn gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
	if _, err := a.keys.Find(conn.User(), gossh.FingerprintSHA256(key)); err != nil {
		return nil, err
	}

	// Look up user to get ID.
	u, err := a.users.GetByUsername(conn.User())
	if err != nil {
		return nil, err
	}

	return &gossh.Permissions{
		Extensions: map[string]string{
			"user_id":  u.ID,
			"username": u.Username,
		},
	}, nil
}
