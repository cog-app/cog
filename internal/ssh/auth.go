package ssh

import (
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
	"git.duckfam.us/jj-software/cog/internal/user"

	gossh "golang.org/x/crypto/ssh"
)

// authCallbacks holds dependencies for SSH authentication.
type authCallbacks struct {
	users        *user.Store
	appPasswords appPasswordValidator
	keys         *keys.Store
}

// appPasswordValidator validates app password tokens.
type appPasswordValidator interface {
	Validate(token string) (userID string, err error)
}

// passwordCallback authenticates via user password or app password.
func (a *authCallbacks) passwordCallback(conn gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
	// Try user password first.
	u, err := a.users.Authenticate(conn.User(), string(password))
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
	fp := gossh.FingerprintSHA256(key)
	username, _, err := a.keys.FindByFingerprint(fp)
	if err != nil {
		return nil, err
	}

	if username != conn.User() {
		return nil, fmt.Errorf("key belongs to %q, not %q", username, conn.User())
	}

	// Look up user to get ID.
	u, err := a.users.GetByUsername(username)
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
