package cog

import (
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/user"
)

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
	if !envBool("COG_SEED") {
		return
	}

	p.Log.Info("seeding development data")

	// Collect the admin user ID.
	admin, err := p.Users.GetByUsername("admin")
	if err != nil {
		p.Log.Error("cannot find admin user", "err", err)
		return
	}

	// Create extra users.
	var memberIDs []string
	for _, su := range seedUsers {
		u, err := p.Users.GetByUsername(su.username)
		if err != nil {
			u, err = p.Users.Create(su.username, su.password)
			if err != nil {
				p.Log.Error("seed: create user failed", "user", su.username, "err", err)
				continue
			}
			// Create root mount for the user.
			if p.Volumes != nil && p.Mounts != nil {
				if defVol, volErr := p.Volumes.GetByName("default"); volErr == nil {
					subpath := u.Username + "/files"
					if _, err := p.Mounts.Create(u.ID, "/", defVol.ID, subpath, false); err != nil {
						p.Log.Error("seed: create mount failed", "user", u.Username, "err", err)
					}
				}
			}
			if su.displayName != "" {
				p.Users.Update(u.ID, user.UpdateOpts{DisplayName: &su.displayName})
			}
			p.Log.Info("seeded user", "user", su.username)
		}
		memberIDs = append(memberIDs, u.ID)
	}

	// Create a group.
	seedGroup(p, admin.ID, memberIDs)

	// Bootstrap mounts for new users so plugin DBs resolve correctly.
	mount.Bootstrap(p.Config.DataDir, p.Users, p.Volumes, p.Mounts)

	// Seed plugin data for the admin user concurrently.
	if err := p.SeedPlugins("admin"); err != nil {
		p.Log.Error("seed: plugin failed", "err", err)
	}

	p.Log.Info("seeding complete")
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
		p.Log.Error("seed: create group failed", "err", err)
		return
	}
	if err := p.Groups.AddMember(g.ID, adminID, group.Owner); err != nil {
		p.Log.Error("seed: add admin to group failed", "err", err)
	}
	for _, id := range memberIDs {
		if err := p.Groups.AddMember(g.ID, id, group.User); err != nil {
			p.Log.Error("seed: add member to group failed", "err", err)
		}
	}
	p.Log.Info("seeded group", "group", groupName, "members", len(memberIDs)+1)
}
