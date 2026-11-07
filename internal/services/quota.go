package services

import (
	"errors"

	"git.duckfam.us/jj-software/cog/internal/quota"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// StorageUsage is what username stores: their files, what plugins keep in
// their storage, and their calendar objects and cards.
func (p *Providers) StorageUsage(username string) (int64, error) {
	plug, ok := p.Plugins.Plugin(files.PluginID)
	if !ok {
		return 0, errors.New("storage usage: the files plugin is not loaded")
	}
	fp, ok := plug.(*files.Plugin)
	if !ok {
		return 0, errors.New("storage usage: the files plugin is not the built-in one")
	}
	inFiles, err := fp.Usage(username)
	if err != nil {
		return 0, err
	}
	kept, err := p.Plugins.Storage().Usage(username)
	if err != nil {
		return 0, err
	}
	var inDAV int64
	if p.DAV != nil {
		if inDAV, err = p.DAV.Usage(username); err != nil {
			return 0, err
		}
	}
	return inFiles + kept + inDAV, nil
}

// newQuotaChecker returns the checker every writer is held to, whichever
// front end (web, DAV, SSH, a command) the write came through.
func newQuotaChecker(p *Providers) *quota.Checker {
	return &quota.Checker{
		Quotas: p.Quotas,
		Usage:  p.StorageUsage,
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
	}
}
