//go:build !nosetup

package cog

import (
	"context"
	"time"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/update"
	"git.duckfam.us/jj-software/cog/internal/version"
)

// startUpdates gives the server an updater: admins can always check for
// and install a release from Admin > Updates, and with update.check (the
// default) it looks once a day by itself.
func startUpdates(ctx context.Context, p *services.Providers, restart func()) {
	exe, err := executable()
	if err != nil {
		p.Log.Error("updates unavailable", "err", err)
		return
	}
	c := newChecker(p.Config.Update.ManifestURL, exe)
	c.Log = p.Log.With("component", "update")
	c.Auto = p.Config.Update.Auto
	c.Restart = restart
	p.Updates = c
	if p.Config.Update.Check {
		go c.Run(ctx)
	}
}

// newChecker is an updater for exe reading the release list at url, or
// the default list for "".
func newChecker(url, exe string) *update.Checker {
	if url == "" {
		url = update.DefaultManifestURL
	}
	return &update.Checker{
		URL:      url,
		Current:  version.Get(),
		Platform: version.Platform(),
		Exe:      exe,
		Keys:     update.TrustedKeys,
		// The release list and binaries are on the public internet.
		Client: safehttp.Client(10 * time.Minute),
	}
}
