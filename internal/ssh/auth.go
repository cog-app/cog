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
	// guard throttles password guessing per client address and per
	// account, as it does for HTTP. A nil guard allows every attempt.
	guard *ratelimit.Guard
}

// appPasswordValidator validates app password tokens.
type appPasswordValidator interface {
	Validate(token string) (userID string, err error)
}

// passwordCallback authenticates via user password or app password.
func (a *authCallbacks) passwordCallback(conn gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
	var perms *gossh.Permissions
	var checkErr error
	_, err := a.guard.Try(remoteIP(conn.RemoteAddr()), ratelimit.AccountKey(conn.User()), func() bool {
		perms, checkErr = a.checkPassword(conn, password)
		return checkErr == nil
	})
	if err != nil {
		return nil, err
	}
	return perms, checkErr
}

// remoteIP is the address part of a client's address, the key its failures
// are counted under.
func remoteIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
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
	if !strings.EqualFold(u.Username, conn.User()) {
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
