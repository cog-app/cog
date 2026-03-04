// Package cog provides a convenience entry point for building custom Cog
// distributions with additional plugins.
package cog

import (
	"log"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/server"
	"git.duckfam.us/jj-software/cog/internal/services"
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

	handler := server.New(p, plugins...)

	log.Printf("Cog listening on %s", cfg.Addr)
	return http.ListenAndServe(cfg.Addr, handler)
}
