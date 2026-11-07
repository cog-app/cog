package ratelimit

import (
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
type Guard struct {
	Limiter *Limiter
	// Delay is how long each attempt at an account over the limit waits.
	Delay time.Duration

	queues sync.Map // account key → *queue
}

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

// Try runs check, an attempt by the client at ip to sign in to the account
// with key account ("" when the credential names no account until it is
// checked, as a bearer token does), unless the client is limited. It
// returns whether check succeeded, or ErrLimited.
func (g *Guard) Try(ip, account string, check func() bool) (bool, error) {
	if g == nil || g.Limiter == nil {
		return check(), nil
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
	if !check() {
		g.Limiter.RecordFailure(ip)
		if account != "" {
			g.Limiter.RecordFailure(account)
		}
		return false, nil
	}
	if account != "" {
		g.Limiter.Reset(account)
		g.queues.Delete(account)
	}
	return true, nil
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
