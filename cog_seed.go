//go:build seed

package cog

import (
	"flag"
	"log"

	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/user"
)

var seedEnabled = flag.Bool("seed", false, "seed development data (users, groups, plugin data)")

func init() {
	seedHook = runSeed
}

// seeder is implemented by plugins that provide dev seed data.
type seeder interface {
	Seed(username string) error
}

type seedUser struct {
	username    string
	password    string
	displayName string
}

var seedUsers = []seedUser{
	{"bob", "bob", "Bob Jones"},
	{"carol", "carol", "Carol White"},
	{"dave", "dave", "Dave Brown"},
}

func runSeed(p *services.Providers) {
	if !*seedEnabled {
		return
	}

	log.Println("Seeding development data...")

	// Collect the admin user ID.
	admin, err := p.Users.GetByUsername("admin")
	if err != nil {
		log.Printf("seed: cannot find admin user: %v", err)
		return
	}

	// Create extra users.
	var memberIDs []string
	for _, su := range seedUsers {
		u, err := p.Users.GetByUsername(su.username)
		if err != nil {
			u, err = p.Users.Create(su.username, su.password)
			if err != nil {
				log.Printf("seed: create user %s: %v", su.username, err)
				continue
			}
			// Create root mount for the user.
			if p.Volumes != nil && p.Mounts != nil {
				if defVol, volErr := p.Volumes.GetByName("default"); volErr == nil {
					subpath := u.Username + "/files"
					if _, err := p.Mounts.Create(u.ID, "/", defVol.ID, subpath, false); err != nil {
						log.Printf("seed: create mount for %s: %v", u.Username, err)
					}
				}
			}
			if su.displayName != "" {
				p.Users.Update(u.ID, user.UpdateOpts{DisplayName: &su.displayName})
			}
			log.Printf("Seeded user %s", su.username)
		}
		memberIDs = append(memberIDs, u.ID)
	}

	// Create a group.
	seedGroup(p, admin.ID, memberIDs)

	// Bootstrap mounts for new users so plugin DBs resolve correctly.
	mount.Bootstrap(p.Config.DataDir, p.Users, p.Volumes, p.Mounts)

	// Seed plugin data for the admin user.
	for _, id := range p.Plugins.PluginIDs() {
		plug, ok := p.Plugins.Plugin(id)
		if !ok {
			continue
		}
		if s, ok := plug.(seeder); ok {
			if err := s.Seed("admin"); err != nil {
				log.Printf("seed: plugin %s: %v", id, err)
			}
		}
	}

	log.Println("Seeding complete.")
}

func seedGroup(p *services.Providers, adminID string, memberIDs []string) {
	const groupName = "Engineering"

	// Check if group already exists.
	for g, err := range p.Groups.List() {
		if err != nil {
			break
		}
		if g.Name == groupName {
			return // already seeded
		}
	}

	g, err := p.Groups.Create(groupName, nil, &adminID)
	if err != nil {
		log.Printf("seed: create group: %v", err)
		return
	}
	if err := p.Groups.AddMember(g.ID, adminID, "OWNER"); err != nil {
		log.Printf("seed: add admin to group: %v", err)
	}
	for _, id := range memberIDs {
		if err := p.Groups.AddMember(g.ID, id, "USER"); err != nil {
			log.Printf("seed: add member to group: %v", err)
		}
	}
	log.Printf("Seeded group %q with %d members", groupName, len(memberIDs)+1)
}
