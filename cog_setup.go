//go:build !nosetup

package cog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/service"
	"git.duckfam.us/jj-software/cog/internal/setup"
	"git.duckfam.us/jj-software/cog/internal/update"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/version"
	"git.duckfam.us/jj-software/cog/plugin"
)

// launchedForSetup reports whether bare `cog` should run setup rather than
// serve: when Windows made a console for it (it was opened from Explorer),
// or when nothing says where a server's data is (no configuration file,
// no COG_* location in the environment, no system.db here).
func launchedForSetup() bool {
	if setup.OwnsConsole() {
		return true
	}
	for _, k := range []string{"COG_CONFIG", "COG_DB", "COG_DATA_DIR", "COG_ADDR"} {
		if os.Getenv(k) != "" {
			return false
		}
	}
	exe, err := executable()
	if err != nil {
		return false // serving reports it, with the rest of the configuration
	}
	if config.Find(nil, exe) != "" {
		return false
	}
	if _, err := os.Stat("system.db"); err == nil {
		return false
	}
	return true
}

// runSetupGUI runs setup for bare `cog`: in the browser where there is a
// desktop, else it says how to set up or serve.
func runSetupGUI() error {
	if !setup.GUIAvailable() {
		fmt.Println("Cog is not set up yet. Run")
		fmt.Println("  cog setup    to install it as a service, step by step")
		fmt.Println("  cog serve    to run it from this folder now")
		return nil
	}
	return runWizard()
}

// waitBeforeClosing keeps a console Windows made for this process open
// until Enter, so that an error can be read before the window goes.
func waitBeforeClosing() {
	if !setup.OwnsConsole() {
		return
	}
	fmt.Print("Press Enter to close this window.")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return // the window is closing anyway
	}
}

func runWizard() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	w := &setup.Wizard{Exe: exe}
	ln, url, err := w.Listen()
	if err != nil {
		return err
	}
	fmt.Printf("Cog setup is open in your browser. If it is not, open this address:\n\n  %s\n\nSetup stops when you close it in the browser", url)
	if setup.OwnsConsole() {
		fmt.Println(" or close this window.")
	} else {
		fmt.Println(" or press Ctrl+C.")
	}
	if err := setup.OpenBrowser(url); err != nil {
		fmt.Fprintf(os.Stderr, "Could not open a browser: %v\n", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return w.Serve(ctx, ln)
}

// addSetupCommands adds setup and update to root.
func addSetupCommands(root *cobra.Command, plugins []plugin.Plugin) {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install Cog as a service, or change, reset or remove an install",
		Long: `Setup copies this binary to a folder you choose, writes its configuration,
makes the admin account, gets a certificate if Cog will serve HTTPS, and
installs a service that runs it. Run it again to change the settings, reset
an admin's password or remove the service.

It runs in the browser when there is a desktop, else at the terminal.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			cli, _ := c.Flags().GetBool("cli")
			web, _ := c.Flags().GetBool("web")
			if web || (!cli && setup.GUIAvailable()) {
				return runWizard()
			}
			exe, err := executable()
			if err != nil {
				return err
			}
			wiz := &setup.CLI{Exe: exe, In: os.Stdin, Out: os.Stdout}
			return wiz.Run(c.Context())
		},
	}
	cmd.Flags().Bool("cli", false, "ask at the terminal even when there is a desktop")
	cmd.Flags().Bool("web", false, "open setup in the browser")
	cmd.MarkFlagsMutuallyExclusive("cli", "web")

	apply := &cobra.Command{
		Use:    "apply",
		Short:  "Carry out a plan setup wrote (setup runs it with administrator rights)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			planFile, _ := c.Flags().GetString("plan")
			logFile, _ := c.Flags().GetString("log")
			resultFile, _ := c.Flags().GetString("result")
			return setup.ApplyFiles(c.Context(), planFile, logFile, resultFile)
		},
	}
	apply.Flags().String("plan", "", "plan file")
	apply.Flags().String("log", "", "log file")
	apply.Flags().String("result", "", "result file")
	for _, f := range []string{"plan", "log", "result"} {
		if err := apply.MarkFlagRequired(f); err != nil {
			panic(err)
		}
	}
	cmd.AddCommand(apply)

	cmd.AddCommand(&cobra.Command{
		Use:    "finish",
		Short:  "Make the admin account an install needs (setup runs it as the server's account)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			var req setup.FinishRequest
			if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
				return fmt.Errorf("read request: %w", err)
			}
			return finishSetup(plugins, req)
		},
	})
	root.AddCommand(cmd)
	root.AddCommand(updateCmd())
}

// finishSetup opens the install's database, which migrates it, and makes
// or resets the admin account req names.
func finishSetup(plugins []plugin.Plugin, req setup.FinishRequest) error {
	p, err := openProviders(plugins...)
	if err != nil {
		return err
	}
	defer p.Close()
	if req.AdminUser == "" {
		return nil
	}
	admin := true
	u, err := p.Users.GetByUsername(req.AdminUser)
	switch {
	case errors.Is(err, user.ErrNotFound):
		if u, err = p.Users.Create(req.AdminUser, req.AdminPassword); err != nil {
			return fmt.Errorf("create %s: %w", req.AdminUser, err)
		}
		if err := p.Users.Update(u.ID, user.UpdateOpts{IsAdmin: &admin}); err != nil {
			return fmt.Errorf("make %s an admin: %w", u.Username, err)
		}
		if err := createRootMount(p, u); err != nil {
			return err
		}
		if err := mount.Bootstrap(p.Config.DataDir, p.Users, p.Volumes, p.Mounts); err != nil {
			return fmt.Errorf("bootstrap mounts: %w", err)
		}
		fmt.Printf("Made the admin account %s\n", u.Username)
	case err != nil:
		return fmt.Errorf("look up %s: %w", req.AdminUser, err)
	default:
		if err := p.Users.UpdatePassword(u.ID, req.AdminPassword); err != nil {
			return fmt.Errorf("set the password of %s: %w", u.Username, err)
		}
		if err := p.Users.Update(u.ID, user.UpdateOpts{IsAdmin: &admin}); err != nil {
			return fmt.Errorf("make %s an admin: %w", u.Username, err)
		}
		if err := p.SignOutEverywhere(u.ID); err != nil {
			return fmt.Errorf("sign %s out: %w", u.Username, err)
		}
		fmt.Printf("Set the password of %s, made it an admin and signed it out everywhere\n", u.Username)
	}
	return nil
}

func updateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Install the newest release of Cog and restart its service",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			exe, err := executable()
			if err != nil {
				return err
			}
			checker := newChecker(cfg.Update.ManifestURL, exe)
			checker.Log = slog.New(slog.NewTextHandler(os.Stderr, nil))
			rel, err := checker.Check(c.Context())
			if err != nil {
				return err
			}
			if rel == nil {
				fmt.Printf("Cog %s is the newest release.\n", version.Get())
				return nil
			}
			if check, _ := c.Flags().GetBool("check"); check {
				fmt.Printf("Cog %s is available (this is %s).\n", rel.Version, version.Get())
				return nil
			}
			fmt.Printf("Installing Cog %s over %s\n", rel.Version, exe)
			if err := update.Install(c.Context(), checker.Client, rel, version.Platform(), exe); err != nil {
				return err
			}
			if cfg.Install.Service == "" {
				fmt.Println("Installed. Restart Cog to run it.")
				return nil
			}
			plan := setup.Plan{Scope: service.Scope(cfg.Install.Service), Dir: cfg.Install.Dir}
			fmt.Println("Restarting the service")
			return service.Restart(plan.Service())
		},
	}
	cmd.Flags().Bool("check", false, "only say whether a newer release exists")
	return cmd
}
