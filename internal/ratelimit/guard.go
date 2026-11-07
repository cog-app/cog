package ratelimit

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrLimited refuses an attempt from a client that has failed too often.
var ErrLimited = errors.New("too many failed attempts")

// Guard is the one place a credential (a password, an app password, a
// second-factor code) is checked against guessing. Everything that checks
// one goes through a Guard, so every way in follows the same rules:
//
//   - A client address with too many recent failures is refused, whatever
//     account it tries.
//   - An account with too many recent failures, from anywhere, is slowed:
//     its attempts wait Delay each, one at a time. Guessing spread over many
//     addresses gets nowhere fast, and its owner can still sign in, where a
//     refusal would let anyone lock them out.
//   - A success clears the account's failures and nothing else. A client
//     signing in to one account does not wipe what it tried against others.
//   - At most MaxWaiting attempts at a slowed account wait at once. The
//     delay costs a guesser time; it is not to cost the server a goroutine
//     per guess, so an attempt past the queue is refused instead.
//   - A secret is first checked as a token, such as an app password, when
//     the caller has a way to: one too long and random to guess. That check
//     runs even when the client or account is limited, and alone then.
//     Refusing a right token would cut off working apps behind an address
//     something else failed from, and checking one gives a guesser nothing,
//     while a password, which can be guessed, is not checked at all. A
//     token's success clears nothing: an owner's apps syncing do not wipe
//     the failures of someone guessing the owner's password.
//   - A client sending a secret it was refused for at an account within the
//     window is refused again at once, and not counted again. A guesser has
//     to vary what it sends, so a repeat teaches it nothing; but an app
//     still holding a revoked token or an old password retries it for as
//     long as it runs, and counting each retry would lock its owner out, on
//     every device behind the same address, with a credential nobody is
//     guessing. The repeat is not checked either, so replaying one wrong
//     password costs the server no password hashing. What makes a refused
//     secret right, a new password or a new account of that name, calls
//     Forget.
type Guard struct {
	Limiter *Limiter
	// Delay is how long each attempt at an account over the limit waits.
	Delay time.Duration

	queues sync.Map // account key → *queue

	refusedMu  sync.Mutex
	refusedKey []byte
	// refused holds, by account key, the attempts refused there and when.
	refused  map[string]map[attempt]time.Time
	nRefused int
	swept    time.Time // when refused was last rid of old refusals
}

// attempt names who sent a secret to an account, and what it was, by an
// HMAC under a key of this process: the map holds nothing a dump of memory
// could test passwords against.
type attempt [sha256.Size]byte

// MaxRefused bounds how many refused secrets a Guard remembers. Past it a
// refusal is not remembered, and a repeat of it counts as a new failure:
// forgetting errs towards counting, never towards letting through.
const MaxRefused = 10_000

// MaxWaiting is how many attempts at a slowed account may wait their turn.
const MaxWaiting = 8

// queue serializes the attempts at one slowed account. waiting admits at
// most MaxWaiting of them; mu lets one through at a time.
type queue struct {
	mu      sync.Mutex
	waiting chan struct{}
}

// AccountKey is the key an account's failures are counted under, the same
// for every protocol, so guesses over SSH and over HTTP add up.
func AccountKey(username string) string {
	return "user:" + strings.ToLower(username)
}

// ShareKey is the key a password-protected share's failures are counted
// under.
func ShareKey(shareID string) string {
	return "share:" + shareID
}

// Try checks secret, an attempt by the client at ip to sign in to the
// account with key account ("" when the credential names no account until
// it is checked, as a bearer token does). It returns whether the secret was
// right, or ErrLimited, or the error of a check that could not tell, which
// counts as nothing.
//
// token, when not nil, checks secret as a token that cannot be guessed. It
// runs first, and runs even when the client or account is limited; only if
// it refuses is check, which checks secret as anything else (a password),
// run, subject to the limits. A nil check accepts nothing else.
//
// A secret this client was refused for at this account within the window
// is refused without either check running or a failure counted. An empty
// secret is never remembered; nor is one a caller passes as "" because the
// right answer can change without Forget, as a one-time code's does.
func (g *Guard) Try(ip, account, secret string, token, check func() (bool, error)) (bool, error) {
	if check == nil {
		check = func() (bool, error) { return false, nil }
	}
	if g == nil || g.Limiter == nil {
		if token != nil {
			if ok, err := token(); ok || err != nil {
				return ok, err
			}
		}
		return check()
	}
	a, remember := g.attempt(ip, account, secret)
	if remember && g.refusedBefore(account, a) {
		return false, nil
	}
	if token != nil {
		ok, err := token()
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	if g.Limiter.IsLimited(ip) {
		return false, ErrLimited
	}
	if account != "" && g.Limiter.IsLimited(account) {
		v, _ := g.queues.LoadOrStore(account, &queue{waiting: make(chan struct{}, MaxWaiting)})
		q := v.(*queue)
		select {
		case q.waiting <- struct{}{}:
		default:
			return false, ErrLimited
		}
		q.mu.Lock()
		defer func() {
			q.mu.Unlock()
			<-q.waiting
		}()
		time.Sleep(g.Delay)
	}
	ok, err := check()
	if err != nil {
		return false, err
	}
	if !ok {
		g.Limiter.RecordFailure(ip)
		if account != "" {
			g.Limiter.RecordFailure(account)
		}
		if remember {
			g.remember(account, a)
		}
		return false, nil
	}
	if account != "" {
		g.Limiter.Reset(account)
		g.queues.Delete(account)
	}
	return true, nil
}

// Forget drops the secrets remembered as refused at account, whose right
// answer has just changed: a password set to one a client was refused for
// a minute ago is right now.
func (g *Guard) Forget(account string) {
	if g == nil {
		return
	}
	g.refusedMu.Lock()
	defer g.refusedMu.Unlock()
	g.nRefused -= len(g.refused[account])
	delete(g.refused, account)
}

// attempt names an attempt, or reports false when secret is empty and the
// attempt is not to be remembered.
func (g *Guard) attempt(ip, account, secret string) (attempt, bool) {
	var a attempt
	if secret == "" {
		return a, false
	}
	g.refusedMu.Lock()
	if g.refusedKey == nil {
		// crypto/rand.Read does not fail: it panics if the system's
		// generator is unavailable.
		g.refusedKey = make([]byte, 32)
		_, _ = rand.Read(g.refusedKey)
	}
	mac := hmac.New(sha256.New, g.refusedKey)
	g.refusedMu.Unlock()
	for _, part := range []string{ip, account, secret} {
		mac.Write([]byte(part))
		mac.Write([]byte{0})
	}
	copy(a[:], mac.Sum(nil))
	return a, true
}

// refusedBefore reports whether a was refused at account within the window.
func (g *Guard) refusedBefore(account string, a attempt) bool {
	g.refusedMu.Lock()
	defer g.refusedMu.Unlock()
	at, ok := g.refused[account][a]
	return ok && g.Limiter.clock().Sub(at) < g.Limiter.Window
}

// remember records that a was refused at account now. A full Guard first
// drops refusals older than the window, at most once a minute so a Guard
// full of recent ones is not scanned on every failure; if it is still full,
// a is not kept.
func (g *Guard) remember(account string, a attempt) {
	now := g.Limiter.clock()
	g.refusedMu.Lock()
	defer g.refusedMu.Unlock()
	if g.nRefused >= MaxRefused && now.Sub(g.swept) >= time.Minute {
		g.swept = now
		for acct, as := range g.refused {
			for k, at := range as {
				if now.Sub(at) >= g.Limiter.Window {
					delete(as, k)
					g.nRefused--
				}
			}
			if len(as) == 0 {
				delete(g.refused, acct)
			}
		}
	}
	if g.nRefused >= MaxRefused {
		return
	}
	if g.refused == nil {
		g.refused = map[string]map[attempt]time.Time{}
	}
	as := g.refused[account]
	if as == nil {
		as = map[attempt]time.Time{}
		g.refused[account] = as
	}
	if _, ok := as[a]; !ok {
		g.nRefused++
	}
	as[a] = now
}

// Limited reports whether the client at ip is refused, for a step that
// counts attempts without checking a credential, such as registration.
func (g *Guard) Limited(ip string) bool {
	return g != nil && g.Limiter != nil && g.Limiter.IsLimited(ip)
}

// Record counts an attempt by the client at ip against its limit.
func (g *Guard) Record(ip string) {
	if g != nil && g.Limiter != nil {
		g.Limiter.RecordFailure(ip)
	}
}
