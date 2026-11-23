// Package cog provides a convenience entry point for building custom Cog
// distributions with additional plugins.
package cog

//go:generate go tool templ generate -log-level warn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/logbuf"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/update"
	"git.duckfam.us/jj-software/cog/internal/version"
	"git.duckfam.us/jj-software/cog/internal/web"
	"git.duckfam.us/jj-software/cog/plugin"
)

// Config holds server configuration for custom Cog builds.
type Config = config.Config

// envString sets *dst from the environment variable key, when it is set.
func envString(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func envBool(key string) bool {
	v := os.Getenv(key)
	return v == "true" || v == "1" || v == "yes"
}

// envBoolVar sets *dst from the environment variable key, when it is set.
func envBoolVar(dst *bool, key string) {
	if _, ok := os.LookupEnv(key); ok {
		*dst = envBool(key)
	}
}

// defaultBaseURL is where the server says it is when nothing names an
// address: its HTTPS name when it serves TLS for one, else this host on the
// plain listener's port.
func defaultBaseURL(cfg config.Config) string {
	if cfg.TLS.Enabled() && len(cfg.TLS.Domains) > 0 {
		return "https://" + cfg.TLS.Domains[0]
	}
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "localhost"
	}
	port := "8580"
	if _, p, err := net.SplitHostPort(cfg.Addr); err == nil && p != "" {
		port = p
	}
	return "http://" + net.JoinHostPort(hostname, port)
}

// loadConfig is the configuration for this process: the defaults, then the
// configuration file (config.Find), then COG_* environment variables.
func loadConfig() (config.Config, error) {
	exe, err := os.Executable()
	if err != nil {
		return config.Config{}, fmt.Errorf("find this program: %w", err)
	}
	cfg := config.Defaults()
	if path := config.Find(os.Args[1:], exe); path != "" {
		if cfg, err = config.Load(path); err != nil {
			return cfg, err
		}
	}
	envString(&cfg.Addr, "COG_ADDR")
	envString(&cfg.DBPath, "COG_DB")
	envString(&cfg.DataDir, "COG_DATA_DIR")
	envString(&cfg.BaseURL, "COG_BASE_URL")
	envString(&cfg.LogFormat, "COG_LOG_FORMAT")
	envString(&cfg.LogLevel, "COG_LOG_LEVEL")
	envString(&cfg.TrustedProxies, "COG_TRUSTED_PROXIES")
	envString(&cfg.FederationPeers, "COG_FEDERATION_PEERS")
	envString(&cfg.TrustedProcessPlugins, "COG_TRUSTED_PROCESS_PLUGINS")
	envString(&cfg.PluginOrigin, "COG_PLUGIN_ORIGIN")
	envString(&cfg.SSHAddr, "COG_SSH_ADDR")
	envBoolVar(&cfg.SSHEnabled, "COG_SSH_ENABLED")
	envString(&cfg.SSHHostKeyPath, "COG_SSH_HOST_KEY")
	envBoolVar(&cfg.SSHNoAccountPasswords, "COG_SSH_NO_ACCOUNT_PASSWORDS")
	envBoolVar(&cfg.JMAPCalendars, "COG_JMAP_CALENDARS")

	envString(&cfg.TLS.Mode, "COG_TLS_MODE")
	envString(&cfg.TLS.Addr, "COG_TLS_ADDR")
	envString(&cfg.TLS.HTTPAddr, "COG_TLS_HTTP_ADDR")
	if v, ok := os.LookupEnv("COG_TLS_DOMAINS"); ok {
		cfg.TLS.Domains = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
	}
	envString(&cfg.TLS.Email, "COG_TLS_EMAIL")
	envString(&cfg.TLS.Challenge, "COG_TLS_CHALLENGE")
	// A DNS provider's credentials come from lego's own variables
	// (CF_DNS_API_TOKEN, ...), which it reads from the environment.
	envString(&cfg.TLS.DNSProvider, "COG_TLS_DNS_PROVIDER")
	envString(&cfg.TLS.CA, "COG_TLS_CA")
	envString(&cfg.TLS.CertFile, "COG_TLS_CERT")
	envString(&cfg.TLS.KeyFile, "COG_TLS_KEY")

	envBoolVar(&cfg.Update.Check, "COG_UPDATE_CHECK")
	envBoolVar(&cfg.Update.Auto, "COG_UPDATE_AUTO")
	envString(&cfg.Update.ManifestURL, "COG_UPDATE_MANIFEST")

	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL(cfg)
	}
	return cfg, nil
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

// openProviders returns the process's one service graph for a command,
// building it on first use. Process plugins serve only HTTP: they register
// no commands and no doctor checks, so a command finds their binaries, and
// the names they hold stay reserved, without starting them. That includes
// cog shell, which an SSH login runs for every session.
func openProviders(plugins ...plugin.Plugin) (*services.Providers, error) {
	return open(true, plugins)
}

// openServerProviders returns the process's one service graph for the
// server, with the process plugins started.
func openServerProviders(plugins ...plugin.Plugin) (*services.Providers, error) {
	return open(false, plugins)
}

// open builds the process's one service graph from env-var config; the
// first caller decides whether process plugins start. Main may build it to
// find plugin commands before the command line is parsed, and the command
// then runs on the same graph: a second would open everything twice.
func open(namesOnly bool, plugins []plugin.Plugin) (*services.Providers, error) {
	shared.once.Do(func() {
		cfg, err := loadConfig()
		if err != nil {
			shared.err = err
			return
		}
		cfg.ProcessPluginNamesOnly = namesOnly
		log, buf := newLogger(cfg)
		shared.p, shared.err = services.New(log, cfg, plugins...)
		if shared.err == nil {
			shared.p.LogBuffer = buf
		}
	})
	return shared.p, shared.err
}

var shared struct {
	once sync.Once
	p    *services.Providers
	err  error
}

// Main starts a Cog server with optional additional plugins.
// Protected plugins (dashboard, files) are always included.
func Main(plugins ...plugin.Plugin) {
	// Opened from a file manager with nothing set up: show setup in the
	// browser, before anything opens a database in whatever directory
	// the file manager started this in.
	if len(os.Args) == 1 && launchedForSetup() {
		if err := runSetupGUI(); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			waitBeforeClosing()
			os.Exit(1)
		}
		return
	}
	serve := serveCmd(plugins)

	root := &cobra.Command{
		Use:           "cog",
		Short:         "Cloud On Go — file storage and plugin platform",
		SilenceUsage:  true,
		SilenceErrors: true,       // printed below, except a restart
		RunE:          serve.RunE, // bare `cog` runs serve
	}
	// Read before the command line is parsed (loadConfig), since the
	// service graph may be opened to find plugin commands; declared here
	// so that every command accepts it.
	root.PersistentFlags().String("config", "", "configuration file (default: $COG_CONFIG, or cog.toml beside an installed binary's bin directory)")

	root.AddCommand(serve)
	root.AddCommand(userCmd(plugins))
	root.AddCommand(groupCmd(plugins))
	root.AddCommand(volumeCmd(plugins))
	root.AddCommand(settingCmd(plugins))
	root.AddCommand(shellCmd(plugins))
	root.AddCommand(doctorCmd(plugins))
	root.AddCommand(versionCmd())
	addSetupCommands(root, plugins)

	// Discover and register plugin CLI commands, which means opening the
	// service graph — so only when the command line names no built-in
	// command, which opens what it needs itself. Bare `cog` serves, so its
	// graph starts the process plugins. Close providers after Execute — not
	// via OnFinalize or PersistentPostRun, which interfere with shell
	// tab-completion and subcommand lifecycle.
	if args := os.Args[1:]; !namesBuiltin(root, args) {
		if pluginProviders := addPluginCommands(root, plugins, len(args) == 0); pluginProviders != nil {
			defer func() {
				if err := pluginProviders.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "close: %v\n", err)
				}
			}()
		}
	}

	err := root.Execute()
	if errors.Is(err, errRestart) {
		err = restartProcess()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// restartProcess starts the server again on the binary an update put in
// place. It returns only on failure.
func restartProcess() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	return update.Restart(exe)
}

// executable is the path of the running binary, with symbolic links
// resolved: the file an update replaces.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find this program: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", fmt.Errorf("find this program: %w", err)
	}
	return exe, nil
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(c *cobra.Command, args []string) {
			fmt.Println(version.Get())
		},
	}
}

// namesBuiltin reports whether args run one of root's own commands, which
// opens what it needs itself. Bare `cog` (serve), help and anything root
// does not know may be, or list, a plugin's command.
func namesBuiltin(root *cobra.Command, args []string) bool {
	cmd, _, err := root.Find(args)
	return err == nil && cmd != root && cmd.Name() != "help"
}

// addPluginCommands registers plugin CLI commands by initializing services
// (lightweight — no web server) and querying the registry for registered commands.
func addPluginCommands(root *cobra.Command, plugins []plugin.Plugin, serving bool) *services.Providers {
	openGraph := openProviders
	if serving {
		openGraph = openServerProviders
	}
	p, err := openGraph(plugins...)
	if err != nil {
		// The built-in commands still work without services, so this is not
		// fatal; say why the plugin commands are missing.
		fmt.Fprintf(os.Stderr, "warning: plugin commands unavailable: %v\n", err)
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
	// An embedding program restarts itself if it wants to: Run has no
	// updater to ask it to.
	return runServer(ctx, p, web.New(p), func() {})
}
