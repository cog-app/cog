package cog

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	cogssh "git.duckfam.us/jj-software/cog/internal/ssh"
	"git.duckfam.us/jj-software/cog/internal/ssh/shell"
	"git.duckfam.us/jj-software/cog/plugin"
)

func shellCmd(plugins []plugin.Plugin) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Run the cog shell for a user (OpenSSH login shell)",
		RunE: func(c *cobra.Command, args []string) error {
			username, _ := c.Flags().GetString("user")
			command, _ := c.Flags().GetString("c")

			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			if _, err := p.Users.GetByUsername(username); err != nil {
				return fmt.Errorf("user %q: %w", username, err)
			}

			// The same filesystem an SSH session gets: writes are held
			// to the quota and recorded by the files plugin.
			userFS, err := cogssh.SessionFS(p, p.SSHKeys, username)
			if err != nil {
				return fmt.Errorf("files for %q: %w", username, err)
			}
			cfg := configFromEnv()
			log, _ := newLogger(cfg)
			sh := shell.New(username, userFS, p.Plugins.UserCommands(), p.Users.Usernames, p.Search, log)

			if command != "" {
				return sh.Run(c.Context(), command, os.Stdin, os.Stdout, os.Stderr)
			}
			return sh.RunInteractive(c.Context(), os.Stdin, os.Stdout, os.Stderr)
		},
	}
	cmd.Flags().String("user", "", "cog username to run as (required)")
	cmd.MarkFlagRequired("user")
	cmd.Flags().StringP("c", "c", "", "command to execute (non-interactive)")
	return cmd
}
