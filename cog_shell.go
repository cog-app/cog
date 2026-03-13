package cog

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/internal/ssh/shell"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"
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

			_, err = p.Users.GetByUsername(username)
			if err != nil {
				return fmt.Errorf("user %q not found", username)
			}

			filesDir := p.Plugins.UserFilesDir(username)
			if filesDir == "" {
				return fmt.Errorf("no files directory for user %q", username)
			}

			userFS, _ := p.Plugins.UserFS(username)
			// Wrap in OverlayFS to mount user files at /files, matching
			// the SSH session setup. The shell's virtual path system
			// expects /files as the root prefix.
			if userFS != nil {
				ofs := vfs.NewOverlayFS(nil)
				ofs.Mount("/files", userFS)
				userFS = ofs
			}
			cfg := configFromEnv()
			log, _ := newLogger(cfg)
			sh := shell.New(username, filesDir, userFS, p.Plugins.UserCommands(), p.Users.Usernames, p.Search, log)

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
