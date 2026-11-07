package user

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ErrInvalidUsername is returned by Create for a name outside the username
// grammar or one the server reserves for itself.
var ErrInvalidUsername = errors.New("invalid username")

// usernamePattern is the whole username grammar. A username names a
// directory in the data dir, so it may contain no separator and cannot be "."
// or "..": the first character is a letter or digit, and the rest add only
// characters that are inert in a path, a URL segment and an email address.
// AgentSeparator is not among them: it is kept for agents' names.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// AgentSeparator joins an agent's owner to its name, as in "alice+helper".
// No person's username may contain it, so a name that does is an agent's,
// and the part before it is who owns it. Like the characters people may
// use, it is inert in a path, a URL segment and an email address.
const AgentSeparator = "+"

// AgentUsername is the username of owner's agent called name.
func AgentUsername(owner, name string) string {
	return owner + AgentSeparator + name
}

// validateAgentUsername reports whether name, the part after the owner's
// username and AgentSeparator, may be given to owner's new agent.
func (s *Store) validateAgentUsername(owner, name string) error {
	full := AgentUsername(owner, name)
	if !usernamePattern.MatchString(name) || len(full) > 64 {
		return fmt.Errorf("%w: %q must be letters, digits, '.', '_', '@' or '-', starting with a letter or digit, and %q at most 64 characters", ErrInvalidUsername, name, full)
	}
	return nil
}

// reservedNames are entries of the data dir that the server owns. A user of
// the same name would share a directory with them.
var reservedNames = []string{"app_data", "avatars", "system.db"}

// reservedPrefixes start the names of entries the server creates in the data
// dir on demand, such as chunked-upload staging.
var reservedPrefixes = []string{"cog-", "system.db"}

// ValidateUsername reports whether name may be given to a new account. Names
// are compared to reserved ones without regard to case, since a data dir on a
// case-insensitive file system would make them the same directory.
func (s *Store) ValidateUsername(name string) error {
	if !usernamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q must be 1–64 letters, digits, '.', '_', '@' or '-', starting with a letter or digit", ErrInvalidUsername, name)
	}
	lower := strings.ToLower(name)
	s.reservedMu.RLock()
	reserved := slices.Contains(reservedNames, lower) || slices.Contains(s.reserved, lower)
	s.reservedMu.RUnlock()
	if !reserved {
		for _, p := range reservedPrefixes {
			reserved = reserved || strings.HasPrefix(lower, p)
		}
	}
	if reserved {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidUsername, name)
	}
	return nil
}

// Reserve adds names the server owns in the data dir, such as the IDs of
// plugins that keep a directory there, to those no new account may take.
func (s *Store) Reserve(names ...string) {
	s.reservedMu.Lock()
	defer s.reservedMu.Unlock()
	for _, n := range names {
		s.reserved = append(s.reserved, strings.ToLower(n))
	}
}

// Invalid returns the usernames of existing accounts that would be refused
// today, so startup can report accounts created before a rule existed: a
// person whose name holds AgentSeparator reads as somebody's agent.
func (s *Store) Invalid() ([]string, error) {
	var users []User
	names := map[string]string{}
	for u, err := range s.List() {
		if err != nil {
			return nil, fmt.Errorf("check usernames: %w", err)
		}
		users = append(users, u)
		names[u.ID] = u.Username
	}
	var bad []string
	for _, u := range users {
		if u.IsAgent() {
			owner, short, ok := strings.Cut(u.Username, AgentSeparator)
			if !ok || owner != names[u.OwnerID] || s.validateAgentUsername(owner, short) != nil {
				bad = append(bad, u.Username)
			}
			continue
		}
		if s.ValidateUsername(u.Username) != nil {
			bad = append(bad, u.Username)
		}
	}
	return bad, nil
}
