package ssh

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
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
	// noAccountPasswords refuses an account's own password, leaving app
	// passwords (config.SSHNoAccountPasswords).
	noAccountPasswords bool
}

// appPasswordValidator validates app password tokens.
type appPasswordValidator interface {
	Validate(token string) (userID string, err error)
}

// passwordCallback authenticates via user password or app password.
func (a *authCallbacks) passwordCallback(conn gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
	var perms *gossh.Permissions
	var checkErr error
	// An app password is tried first, and even from a limited client: it
	// cannot be guessed, and refusing it would cut off a working client
	// because something else at its address failed. Only then is the
	// secret checked as the account's password, within the limits.
	asCheck := func(f func(gossh.ConnMetadata, []byte) (*gossh.Permissions, error)) func() (bool, error) {
		return func() (bool, error) {
			perms, checkErr = f(conn, password)
			if checkErr != nil && !errors.Is(checkErr, errWrongPassword) {
				return false, checkErr
			}
			return checkErr == nil, nil
		}
	}
	// With account passwords refused there is nothing for the limits to
	// guard but the app password, which Try counts as a failure if wrong.
	var check func() (bool, error)
	if !a.noAccountPasswords {
		check = asCheck(a.checkPassword)
	}
	ok, err := a.guard.Try(remoteIP(conn.RemoteAddr()), ratelimit.AccountKey(conn.User()), string(password),
		asCheck(a.checkAppPassword), check)
	if err != nil {
		return nil, err
	}
	// Only ok says the password was right: a repeat of a refused one is
	// refused without check running, and perms and checkErr are then nil,
	// which the SSH library would take for success.
	if !ok {
		if checkErr != nil {
			return nil, checkErr
		}
		return nil, errWrongPassword
	}
	return perms, nil
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

// errWrongPassword is a password or app password that is not right for the
// account, as opposed to one that could not be checked.
var errWrongPassword = errors.New("wrong password")

// checkPassword signs in with password as the account password of the user
// conn names.
func (a *authCallbacks) checkPassword(conn gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
	u, err := a.users.Authenticate(conn.User(), string(password))
	if errors.Is(err, user.ErrAuthFailed) {
		return nil, errWrongPassword
	}
	if err != nil {
		// A lookup failure, not a wrong password: don't let it pass for one.
		return nil, fmt.Errorf("authenticate %q: %w", conn.User(), err)
	}
	if u.MFAMethod != "" {
		// A password is one factor of this account's two; SSH asks for
		// a key or an app password instead.
		return nil, errWrongPassword
	}
	return &gossh.Permissions{
		Extensions: map[string]string{
			"user_id":  u.ID,
			"username": u.Username,
		},
	}, nil
}

// checkAppPassword signs in with password as an app password of the user
// conn names.
func (a *authCallbacks) checkAppPassword(conn gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
	userID, err := a.appPasswords.Validate(string(password))
	if errors.Is(err, auth.ErrInvalidAppPassword) {
		return nil, errWrongPassword
	}
	if err != nil {
		return nil, fmt.Errorf("validate app password: %w", err)
	}

	u, err := a.users.GetByID(userID)
	if errors.Is(err, user.ErrNotFound) {
		return nil, errWrongPassword
	}
	if err != nil {
		return nil, fmt.Errorf("app password user: %w", err)
	}

	// Verify that the app password belongs to the user logging in.
	if !strings.EqualFold(u.Username, conn.User()) {
		return nil, fmt.Errorf("app password of another user for %q: %w", conn.User(), errWrongPassword)
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
