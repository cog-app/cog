package cog

import (
	"fmt"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin"
)

// --- user commands ---

func userCmd(plugins []plugin.Plugin) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage users",
	}

	// user list
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all users",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			for u, err := range p.Users.List() {
				if err != nil {
					return err
				}
				admin := ""
				if u.IsAdmin {
					admin = " (admin)"
				}
				fmt.Printf("%s\t%s%s\n", u.ID, u.Username, admin)
			}
			return nil
		},
	})

	// user create
	createCmd := &cobra.Command{
		Use:   "create <username> <password>",
		Short: "Create a user",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			u, err := p.Users.Create(args[0], args[1])
			if err != nil {
				return err
			}

			isAdmin, _ := c.Flags().GetBool("admin")
			if isAdmin {
				admin := true
				p.Users.Update(u.ID, user.UpdateOpts{IsAdmin: &admin})
			}

			// Create root mount pointing to default volume.
			if p.Volumes != nil && p.Mounts != nil {
				if defVol, volErr := p.Volumes.GetByName("default"); volErr == nil {
					subpath := u.Username + "/files"
					if _, err := p.Mounts.Create(u.ID, "/", defVol.ID, subpath, false); err != nil {
						fmt.Printf("warning: create mount: %v\n", err)
					}
				}
			}

			// Bootstrap mounts so files directory is created.
			mount.Bootstrap(p.Config.DataDir, p.Users, p.Volumes, p.Mounts)

			fmt.Printf("Created user %q (id: %s)\n", u.Username, u.ID)
			return nil
		},
	}
	createCmd.Flags().Bool("admin", false, "make the user an admin")
	cmd.AddCommand(createCmd)

	// user delete
	cmd.AddCommand(&cobra.Command{
		Use:   "delete <username>",
		Short: "Delete a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			u, err := p.Users.GetByUsername(args[0])
			if err != nil {
				return fmt.Errorf("user %q not found", args[0])
			}
			if err := p.Users.Delete(u.ID); err != nil {
				return err
			}
			fmt.Printf("Deleted user %q\n", args[0])
			return nil
		},
	})

	// user set-password
	cmd.AddCommand(&cobra.Command{
		Use:   "set-password <username> <password>",
		Short: "Set a user's password",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			u, err := p.Users.GetByUsername(args[0])
			if err != nil {
				return fmt.Errorf("user %q not found", args[0])
			}
			if err := p.Users.UpdatePassword(u.ID, args[1]); err != nil {
				return err
			}
			fmt.Printf("Password updated for %q\n", args[0])
			return nil
		},
	})

	// user set-admin
	cmd.AddCommand(&cobra.Command{
		Use:   "set-admin <username> <true|false>",
		Short: "Set a user's admin status",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			u, err := p.Users.GetByUsername(args[0])
			if err != nil {
				return fmt.Errorf("user %q not found", args[0])
			}
			isAdmin := args[1] == "true"
			if err := p.Users.Update(u.ID, user.UpdateOpts{IsAdmin: &isAdmin}); err != nil {
				return err
			}
			fmt.Printf("Admin status for %q set to %v\n", args[0], isAdmin)
			return nil
		},
	})

	return cmd
}

// --- group commands ---

func groupCmd(plugins []plugin.Plugin) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "group",
		Short: "Manage groups",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all groups",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			for g, err := range p.Groups.List() {
				if err != nil {
					return err
				}
				fmt.Printf("%s\t%s\t(%d members)\n", g.ID, g.Name, g.MemberCount)
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "create <name>",
		Short: "Create a group",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			g, err := p.Groups.Create(args[0], nil, nil)
			if err != nil {
				return err
			}
			fmt.Printf("Created group %q (id: %s)\n", g.Name, g.ID)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a group",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			g, err := findGroupByName(p, args[0])
			if err != nil {
				return err
			}
			if err := p.Groups.Delete(g.ID); err != nil {
				return err
			}
			fmt.Printf("Deleted group %q\n", args[0])
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "members <name>",
		Short: "List members of a group",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			g, err := findGroupByName(p, args[0])
			if err != nil {
				return err
			}
			for m, err := range p.Groups.ListMembers(g.ID) {
				if err != nil {
					return err
				}
				fmt.Printf("%s\t%s\t%s\n", m.MemberID, m.Username, m.MemberType)
			}
			return nil
		},
	})

	addMemberCmd := &cobra.Command{
		Use:   "add-member <group> <username>",
		Short: "Add a user to a group",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			g, err := findGroupByName(p, args[0])
			if err != nil {
				return err
			}
			u, err := p.Users.GetByUsername(args[1])
			if err != nil {
				return fmt.Errorf("user %q not found", args[1])
			}
			mt := group.User
			if owner, _ := c.Flags().GetBool("owner"); owner {
				mt = group.Owner
			}
			if err := p.Groups.AddMember(g.ID, u.ID, mt); err != nil {
				return err
			}
			fmt.Printf("Added %q to group %q\n", args[1], args[0])
			return nil
		},
	}
	addMemberCmd.Flags().Bool("owner", false, "add as owner")
	cmd.AddCommand(addMemberCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "remove-member <group> <username>",
		Short: "Remove a user from a group",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			g, err := findGroupByName(p, args[0])
			if err != nil {
				return err
			}
			u, err := p.Users.GetByUsername(args[1])
			if err != nil {
				return fmt.Errorf("user %q not found", args[1])
			}
			if err := p.Groups.RemoveMember(g.ID, u.ID); err != nil {
				return err
			}
			fmt.Printf("Removed %q from group %q\n", args[1], args[0])
			return nil
		},
	})

	return cmd
}

// --- volume commands ---

func volumeCmd(plugins []plugin.Plugin) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Manage volumes",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all volumes",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			for v, err := range p.Volumes.List() {
				if err != nil {
					return err
				}
				fmt.Printf("%s\t%s\t%s\n", v.ID, v.Name, v.URI)
			}
			return nil
		},
	})

	createVolCmd := &cobra.Command{
		Use:   "create <name> <uri>",
		Short: "Create a volume",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			desc, _ := c.Flags().GetString("description")
			v, err := p.Volumes.Create(args[0], desc, args[1], "cli", nil, "")
			if err != nil {
				return err
			}
			fmt.Printf("Created volume %q (id: %s)\n", v.Name, v.ID)
			return nil
		},
	}
	createVolCmd.Flags().String("description", "", "volume description")
	cmd.AddCommand(createVolCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			v, err := p.Volumes.GetByName(args[0])
			if err != nil {
				return fmt.Errorf("volume %q not found", args[0])
			}
			if err := p.Volumes.Delete(v.ID); err != nil {
				return err
			}
			fmt.Printf("Deleted volume %q\n", args[0])
			return nil
		},
	})

	return cmd
}

// --- setting commands ---

func settingCmd(plugins []plugin.Plugin) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setting",
		Short: "Manage system settings",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "get <key>",
		Short: "Get a system setting",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			val, err := p.Settings.Get(args[0])
			if err != nil {
				return err
			}
			fmt.Println(val)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a system setting",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			if err := p.Settings.Set(args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("Set %q = %q\n", args[0], args[1])
			return nil
		},
	})

	return cmd
}

// --- helpers ---

func findGroupByName(p *services.Providers, name string) (*group.Group, error) {
	for g, err := range p.Groups.List() {
		if err != nil {
			return nil, err
		}
		if g.Name == name {
			return &g, nil
		}
	}
	return nil, fmt.Errorf("group %q not found", name)
}
