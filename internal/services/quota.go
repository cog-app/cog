package services

import (
	"errors"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/quota"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// StorageUsage is what username stores on their root volume: their files
// there, what plugins keep in their storage, and their calendar objects and
// cards.
func (p *Providers) StorageUsage(username string) (int64, error) {
	return p.usage(quota.Root(username))
}

// usage is what an account holds: the user's files on its volume, and on
// their root volume everything else they keep too.
func (p *Providers) usage(a quota.Account) (int64, error) {
	plug, ok := p.Plugins.Plugin(files.PluginID)
	if !ok {
		return 0, errors.New("storage usage: the files plugin is not loaded")
	}
	fp, ok := plug.(*files.Plugin)
	if !ok {
		return 0, errors.New("storage usage: the files plugin is not the built-in one")
	}
	byVolume, root, err := fp.UsageByVolume(a.Username)
	if err != nil {
		return 0, err
	}
	if a.VolumeID != "" && a.VolumeID != root {
		return byVolume[a.VolumeID], nil
	}
	inFiles := byVolume[root]
	kept, err := p.Plugins.Storage().Usage(a.Username)
	if err != nil {
		return 0, err
	}
	var inDAV int64
	if p.DAV != nil {
		if inDAV, err = p.DAV.Usage(a.Username); err != nil {
			return 0, err
		}
	}
	return inFiles + kept + inDAV, nil
}

// accountAt is the account a write to path in username's files is charged
// to: whoever the mount holding it charges, on its volume; their root
// volume's account when that is where it lands.
func (p *Providers) accountAt(username, path string) (quota.Account, error) {
	fsys, err := p.Resolver.UserFS(username)
	if err != nil {
		return quota.Account{}, err
	}
	mfs, ok := fsys.(*volume.MountFS)
	if !ok {
		return quota.Root(username), nil
	}
	e, err := mfs.Entry(path)
	if err != nil {
		return quota.Account{}, err
	}
	charged := e.ChargeTo
	if charged == "" {
		charged = username
	}
	u, err := p.Users.GetByUsername(charged)
	if err != nil {
		return quota.Account{}, fmt.Errorf("account of %s: %w", charged, err)
	}
	root, err := p.Mounts.GetUserRoot(u.ID)
	if err != nil {
		return quota.Account{}, fmt.Errorf("account of %s: %w", charged, err)
	}
	if e.VolumeID == "" || e.VolumeID == root.VolumeID {
		return quota.Root(charged), nil
	}
	return quota.Account{Username: charged, VolumeID: e.VolumeID}, nil
}

// newQuotaChecker returns the checker every writer is held to, whichever
// front end (web, DAV, SSH, a command) the write came through.
func newQuotaChecker(p *Providers) *quota.Checker {
	return &quota.Checker{
		Quotas: p.Quotas,
		Usage:  p.usage,
		RootVolume: func(username string) (string, error) {
			u, err := p.Users.GetByUsername(username)
			if err != nil {
				return "", err
			}
			m, err := p.Mounts.GetUserRoot(u.ID)
			if err != nil {
				return "", err
			}
			return m.VolumeID, nil
		},
		GroupIDs: func(userID string) ([]string, error) {
			var ids []string
			for g, err := range p.Groups.ListGroupsForUser(userID) {
				if err != nil {
					return nil, err
				}
				ids = append(ids, g.ID)
			}
			return ids, nil
		},
		UserIDFromName: func(username string) (string, error) {
			u, err := p.Users.GetByUsername(username)
			if err != nil {
				return "", err
			}
			return u.ID, nil
		},
		AccountAt: p.accountAt,
	}
}
