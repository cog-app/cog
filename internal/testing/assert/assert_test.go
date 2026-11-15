package assert_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
)

// recorder is a testing.TB that records failures instead of reporting them.
type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Helper()      {}
func (r *recorder) Error(...any) { r.failed = true }

func TestInDelta(t *testing.T) {
	r := &recorder{TB: t}
	if !assert.InDelta(r, 12.0, 12.04, 0.05) || r.failed {
		t.Error("12.04 is within 0.05 of 12")
	}
	r = &recorder{TB: t}
	if assert.InDelta(r, 12.0, 12.1, 0.05) || !r.failed {
		t.Error("12.1 is not within 0.05 of 12")
	}
}
