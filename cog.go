// Package cog provides a convenience entry point for building custom Cog
// distributions with additional plugins.
package cog

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/services"
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

func envIntOr(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}

func envBool(key string) bool {
	v := os.Getenv(key)
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
		Addr:     envOr("COG_ADDR", ":8580"),
		DBPath:   envOr("COG_DB", "system.db"),
		DataDir:  envOr("COG_DATA_DIR", "data"),
		BaseURL:  envOr("COG_BASE_URL", defaultBaseURL()),
		SMTPHost: envOr("COG_SMTP_HOST", ""),
		SMTPPort: envIntOr("COG_SMTP_PORT", 587),
		SMTPUser: envOr("COG_SMTP_USER", ""),
		SMTPPass: envOr("COG_SMTP_PASS", ""),
		SMTPFrom: envOr("COG_SMTP_FROM", ""),
	}
}

// openProviders creates a services.Providers from env-var config.
func openProviders(plugins ...plugin.Plugin) (*services.Providers, error) {
	return services.New(configFromEnv(), plugins...)
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

	// Register plugin commands lazily — only probe the DB when needed
	// (first arg contains ':' suggesting a plugin command, or for completions).
	if needsPluginDiscovery(os.Args) {
		addPluginCommands(root, plugins)
	}

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// needsPluginDiscovery returns true if the command line suggests we need to
// discover plugin commands (e.g. "gallery:scan" or shell completion).
func needsPluginDiscovery(args []string) bool {
	if len(args) < 2 {
		return false
	}
	for _, arg := range args[1:] {
		if strings.Contains(arg, ":") {
			return true
		}
		if arg == "__complete" || arg == "completion" {
			return true
		}
	}
	return false
}

// addPluginCommands registers plugin CLI commands by initializing services
// (lightweight — no web server) and querying the registry for registered commands.
func addPluginCommands(root *cobra.Command, plugins []plugin.Plugin) {
	p, err := openProviders(plugins...)
	if err != nil {
		return
	}

	for pluginID, cmds := range p.Plugins.Commands() {
		for _, cmd := range cmds {
			cmd := cmd
			use := pluginID + ":" + cmd.Name
			for _, arg := range cmd.Args {
				use += " <" + arg + ">"
			}
			root.AddCommand(&cobra.Command{
				Use:   use,
				Short: cmd.Short,
				Args:  cobra.ExactArgs(len(cmd.Args)),
				RunE: func(c *cobra.Command, args []string) error {
					return cmd.Run(c.Context(), args)
				},
			})
		}
	}

	// Keep providers alive for the duration of the process so plugin
	// command Run callbacks can use their captured plugin context.
	// Clean up when the root command's context is done or on exit.
	cobra.OnFinalize(func() { p.Close() })
}

// Run starts a Cog server with custom context, config and error handling.
// See [Main] for using a default installation with custom plugins.
func Run(ctx context.Context, cfg Config, plugins ...plugin.Plugin) error {
	p, err := services.New(cfg, plugins...)
	if err != nil {
		return err
	}
	defer p.Close()

	handler := web.New(p)

	if err := p.StartSearch(ctx); err != nil {
		log.Printf("Warning: search init failed: %v", err)
	}

	log.Printf("Cog listening on %s", cfg.Addr)
	return http.ListenAndServe(cfg.Addr, handler)
}
