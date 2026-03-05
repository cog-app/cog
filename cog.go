// Package cog provides a convenience entry point for building custom Cog
// distributions with additional plugins.
package cog

import (
	"context"
	"log"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/web"
	"git.duckfam.us/jj-software/cog/plugin"
)

// Config holds server configuration for custom Cog builds.
type Config = config.Config

// Run starts a Cog server with optional additional plugins.
// Protected plugins (dashboard, files) are always included.
func Run(cfg Config, plugins ...plugin.Plugin) error {
	p, err := services.New(cfg)
	if err != nil {
		return err
	}
	defer p.Close()

	handler := web.New(p, plugins...)

	if err := p.StartSearch(context.Background()); err != nil {
		log.Printf("Warning: search init failed: %v", err)
	}

	log.Printf("Cog listening on %s", cfg.Addr)
	return http.ListenAndServe(cfg.Addr, handler)
}
