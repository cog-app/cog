package mount_test

import (
	"sync"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// An account with no root mount yet, reached by several requests at once
// (a page and its follow-ups, the search sync), gets one, and none of them
// fails because another made it first.
func TestResolverRootConcurrent(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")
	p := env.Providers
	r := mount.NewResolver(p.Users, p.Mounts, p.Volumes, p.Access)

	const n = 64
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			_, errs[i] = r.UserFilesDir("alice")
		})
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
}
