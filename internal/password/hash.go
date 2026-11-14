package password

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// Cost is the bcrypt work factor for every secret the server stores hashed:
// passwords, share passwords, one-time codes and backup codes.
//
// It is a variable only so cogtest can lower it. A test that signs in with
// Basic auth runs bcrypt on every request, and at DefaultCost that was most
// of the suite's CPU. Checking a hash costs what the hash was made with, so
// lowering this also makes every later comparison cheap.
var Cost = bcrypt.DefaultCost

// Hash returns the bcrypt hash of secret at Cost.
func Hash(secret string) ([]byte, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(secret), Cost)
	if err != nil {
		return nil, fmt.Errorf("bcrypt: %w", err)
	}
	return h, nil
}

// dummies holds one hash of the empty secret per cost, for Waste.
var dummies sync.Map // int → []byte

// Waste spends as long as checking secret against a hash made at Cost
// would, and reports nothing. A caller with no hash to check (no such user,
// or one with no password) calls it so its answer takes as long as a wrong
// password's, and the response time does not say which it was.
func Waste(secret string) error {
	cost := Cost
	h, ok := dummies.Load(cost)
	if !ok {
		d, err := bcrypt.GenerateFromPassword(nil, cost)
		if err != nil {
			return fmt.Errorf("bcrypt: %w", err)
		}
		h, _ = dummies.LoadOrStore(cost, d)
	}
	err := bcrypt.CompareHashAndPassword(h.([]byte), []byte(secret))
	if err == nil || errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return nil
	}
	return fmt.Errorf("bcrypt: %w", err)
}
