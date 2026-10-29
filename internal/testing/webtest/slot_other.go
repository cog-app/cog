//go:build !unix

package webtest

// tryLock does not limit browsers where flock is unavailable.
func tryLock(string) (func(), bool, error) { return func() {}, true, nil }
