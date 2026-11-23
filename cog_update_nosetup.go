//go:build nosetup

package cog

import (
	"context"

	"git.duckfam.us/jj-software/cog/internal/services"
)

// startUpdates does nothing: a nosetup build (an image, a distribution's
// package) is updated by whoever packaged it.
func startUpdates(context.Context, *services.Providers, func()) {}
