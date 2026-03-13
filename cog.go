// Package cog provides a convenience entry point for building custom Cog
// distributions with additional plugins.
package cog

//go:generate go tool templ generate -log-level warn

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/logbuf"
	"git.duckfam.us/jj-software/cog/internal/services"
	cogssh "git.duckfam.us/jj-software/cog/internal/ssh"
	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
	"git.duckfam.us/jj-software/cog/internal/web"
	"git.duckfam.us/jj-software/cog/plugin"
)

// seedHook is set by cog_seed.go when built with the "seed" build tag.
var seedHook func(*services.Providers)

// Config holds server configuration for custom Cog builds.
type Config = config.Config

func envOr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envBool(key string) bool {
	v := os.Getenv(key)
	return v == "true" || v == "1" || v == "yes"
}

func envBoolDefault(key string, defaultVal bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	return v == "true" || v == "1" || v == "yes"
}

func defaultBaseURL() string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "localhost"
	}
	return fmt.Sprintf("http://%s:8580", hostname)
}

func configFromEnv() config.Config {
	return config.Config{
		Addr:      envOr("COG_ADDR", ":8580"),
		DBPath:    envOr("COG_DB", "system.db"),
		DataDir:   envOr("COG_DATA_DIR", "data"),
		BaseURL:   envOr("COG_BASE_URL", defaultBaseURL()),
		LogFormat: envOr("COG_LOG_FORMAT", "text"),
		LogLevel:  envOr("COG_LOG_LEVEL", "info"),

		SSHAddr:        envOr("COG_SSH_ADDR", ":8522"),
		SSHEnabled:     envBoolDefault("COG_SSH_ENABLED", true),
		SSHHostKeyPath: os.Getenv("COG_SSH_HOST_KEY"),
	}
}

// newLogger builds a *slog.Logger from config values and a ring buffer
// that captures all log output for the admin UI.
func newLogger(cfg config.Config) (*slog.Logger, *logbuf.Buffer) {
	var level slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if strings.ToLower(cfg.LogFormat) == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	buf := logbuf.New(logbuf.DefaultCapacity)
	return slog.New(logbuf.NewTeeHandler(handler, buf)), buf
}

// openProviders creates a services.Providers from env-var config.
func openProviders(plugins ...plugin.Plugin) (*services.Providers, error) {
	cfg := configFromEnv()
	log, buf := newLogger(cfg)
	p, err := services.New(log, cfg, plugins...)
	if err != nil {
		return nil, err
	}
	p.LogBuffer = buf
	return p, nil
}

// Main starts a Cog server with optional additional plugins.
// Protected plugins (dashboard, files) are always included.
func Main(plugins ...plugin.Plugin) {
	serve := serveCmd(plugins)

	root := &cobra.Command{
		Use:          "cog",
		Short:        "Cloud On Go — file storage and plugin platform",
		SilenceUsage: true,
		RunE:         serve.RunE, // bare `cog` runs serve
	}

	root.AddCommand(serve)
	root.AddCommand(userCmd(plugins))
	root.AddCommand(groupCmd(plugins))
	root.AddCommand(volumeCmd(plugins))
	root.AddCommand(settingCmd(plugins))
	root.AddCommand(shellCmd(plugins))

	// Discover and register plugin CLI commands.
	// Close providers after Execute — not via OnFinalize or PersistentPostRun
	// which interfere with shell tab-completion and subcommand lifecycle.
	if pluginProviders := addPluginCommands(root, plugins); pluginProviders != nil {
		defer pluginProviders.DB.Close()
	}

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// addPluginCommands registers plugin CLI commands by initializing services
// (lightweight — no web server) and querying the registry for registered commands.
func addPluginCommands(root *cobra.Command, plugins []plugin.Plugin) *services.Providers {
	p, err := openProviders(plugins...)
	if err != nil {
		return nil
	}

	for pluginID, cmds := range p.Plugins.AdminCommands() {
		parent := &cobra.Command{
			Use:   pluginID,
			Short: pluginID + " plugin commands",
		}
		for _, cmd := range cmds {
			parent.AddCommand(buildCobraCmd(cmd))
		}
		root.AddCommand(parent)
	}

	// Return the providers so the caller can close them after Execute().
	// We avoid cobra.OnFinalize (fires globally, breaks shell tab-complete)
	// and PersistentPostRun (runs on every subcommand including shell, and
	// the search propagator was never started so Stop() would hang).
	return p
}

func buildCobraCmd(def plugin.CommandDef) *cobra.Command {
	var use strings.Builder
	use.WriteString(def.Name)
	for _, arg := range def.Args {
		use.WriteString(" <" + arg + ">")
	}
	cmd := &cobra.Command{
		Use:   use.String(),
		Short: def.Short,
	}
	if def.Run != nil {
		run := def.Run
		cmd.Args = cobra.ExactArgs(len(def.Args))
		cmd.RunE = func(c *cobra.Command, args []string) error {
			return run(c.Context(), args)
		}
	}
	for _, sub := range def.Sub {
		cmd.AddCommand(buildCobraCmd(sub))
	}
	return cmd
}

// Run starts a Cog server with custom context, config and error handling.
// See [Main] for using a default installation with custom plugins.
func Run(ctx context.Context, cfg Config, plugins ...plugin.Plugin) error {
	log, buf := newLogger(cfg)
	p, err := services.New(log, cfg, plugins...)
	if err != nil {
		return err
	}
	p.LogBuffer = buf
	defer p.Close()

	handler := web.New(p)

	if err := p.StartSearch(ctx); err != nil {
		log.Warn("search init failed", "err", err)
	}

	// Start SSH server if enabled.
	if cfg.SSHEnabled {
		ks := keys.NewStore(p.Plugins, p.Users)
		sshSrv, err := cogssh.New(cfg, p, ks, log)
		if err != nil {
			return err
		}
		defer sshSrv.Close()
		go func() {
			if err := sshSrv.ListenAndServe(); err != nil {
				log.Error("ssh server error", "err", err)
			}
		}()
	}

	log.Info("Cog listening", "addr", cfg.Addr)
	return http.ListenAndServe(cfg.Addr, handler)
}
