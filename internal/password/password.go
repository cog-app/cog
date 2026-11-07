// Package password decides whether a password may be set. The policy is the
// admin's: a minimum length, and whether a password known from a breach is
// refused. Breached passwords are looked up by k-anonymity (the first five
// hex digits of the SHA-1 go to the service, the rest is compared here), so
// no password leaves the server.
package password

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
	"git.duckfam.us/jj-software/cog/internal/settings"
)

// ErrWeak is returned, wrapped with the reason, for a password the policy
// refuses. The reason is for the person choosing it.
var ErrWeak = errors.New("password refused")

// ErrUnchecked is returned when the breach list could not be consulted. The
// policy fails closed: a password an admin asked to have checked is not set
// unchecked.
var ErrUnchecked = errors.New("the password could not be checked against known breaches; try again in a moment")

// DefaultMinLength is the minimum length until an admin sets one. Eight is
// NIST SP 800-63B's floor; length is the one rule that helps.
const DefaultMinLength = 8

// MaxLength is as long as a password may be: bcrypt reads 72 bytes.
const MaxLength = 72

// Policy is what a password must satisfy.
type Policy struct {
	MinLength   int
	BreachCheck bool
}

// FromSettings reads the admin's policy. Unset means the defaults: eight
// characters, breaches checked.
func FromSettings(s *settings.Store) Policy {
	return Policy{
		MinLength:   s.GetInt(settings.KeyPasswordMinLength, DefaultMinLength),
		BreachCheck: s.GetBoolDefault(settings.KeyPasswordBreachCheck, true),
	}
}

// Checker applies a policy, read fresh for each password so an admin's
// change holds at once.
type Checker struct {
	Policy func() Policy
	Breach *BreachList
	Log    *slog.Logger
}

// Check reports whether pw may be set.
func (c *Checker) Check(pw string) error {
	p := c.Policy()
	n := utf8.RuneCountInString(pw)
	if n < p.MinLength {
		return fmt.Errorf("%w: use at least %d characters", ErrWeak, p.MinLength)
	}
	if len(pw) > MaxLength {
		return fmt.Errorf("%w: use at most %d bytes", ErrWeak, MaxLength)
	}
	if !p.BreachCheck || c.Breach == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), breachTimeout)
	defer cancel()
	breached, err := c.Breach.Breached(ctx, pw)
	if err != nil {
		if c.Log != nil {
			c.Log.Error("password breach check", "err", err)
		}
		return ErrUnchecked
	}
	if breached {
		return fmt.Errorf("%w: it appears in a known data breach; choose another", ErrWeak)
	}
	return nil
}

// DefaultBreachURL is the Pwned Passwords range endpoint.
const DefaultBreachURL = "https://api.pwnedpasswords.com/range/"

const breachTimeout = 10 * time.Second

// BreachList asks a Pwned Passwords range service whether a password is
// known. Only a five-character prefix of the password's SHA-1 is sent.
type BreachList struct {
	// URL is the range endpoint; DefaultBreachURL when empty.
	URL string
	// HTTP is the client; safehttp's when nil. The service is public, so
	// the guard costs nothing.
	HTTP *http.Client
}

// Breached reports whether pw appears in the list.
func (b *BreachList) Breached(ctx context.Context, pw string) (bool, error) {
	sum := sha1.Sum([]byte(pw))
	digest := strings.ToUpper(hex.EncodeToString(sum[:]))
	prefix, suffix := digest[:5], digest[5:]

	base := b.URL
	if base == "" {
		base = DefaultBreachURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+prefix, nil)
	if err != nil {
		return false, err
	}
	// Padding hides which prefix was asked about from the response size.
	req.Header.Set("Add-Padding", "true")
	client := b.HTTP
	if client == nil {
		client = safehttp.Client(breachTimeout)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("breach list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("breach list: status %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		rest, count, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		if !ok || !strings.EqualFold(rest, suffix) {
			continue
		}
		return strings.TrimSpace(count) != "0", nil
	}
	if err := sc.Err(); err != nil {
		return false, fmt.Errorf("breach list: read: %w", err)
	}
	return false, nil
}
