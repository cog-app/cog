package services

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// notifyShare tells the people a new share reaches that they received it:
// its user, or every member of its group but the owner. A share with
// everyone on the server tells no one; it would be a notification to every
// account at once. Every way of sharing ends in share.Store.Create, which
// calls this, so no front end has to remember to.
func (p *Providers) notifyShare(rec share.Record) {
	if err := p.notifyShareErr(rec); err != nil {
		p.Log.Error("notify share recipients", "share", rec.ID, "err", err)
	}
}

func (p *Providers) notifyShareErr(rec share.Record) error {
	var recipientIDs []string
	audience := "you"
	switch rec.SubjectType {
	case "USER":
		recipientIDs = []string{rec.SubjectID}
	case "GROUP":
		g, err := p.Groups.Get(rec.SubjectID)
		if err != nil {
			return fmt.Errorf("group %s: %w", rec.SubjectID, err)
		}
		audience = g.Name
		if recipientIDs, err = p.Groups.ListClosureUserIDs(rec.SubjectID); err != nil {
			return fmt.Errorf("members of group %s: %w", rec.SubjectID, err)
		}
	default:
		return nil
	}
	recipientIDs = slices.DeleteFunc(recipientIDs, func(id string) bool { return id == rec.OwnerUserID })
	if len(recipientIDs) == 0 {
		return nil
	}

	people, err := p.Users.GetByIDs(append(recipientIDs, rec.OwnerUserID))
	if err != nil {
		return fmt.Errorf("look up users: %w", err)
	}
	owner, ok := people[rec.OwnerUserID]
	if !ok {
		return fmt.Errorf("owner %s not found", rec.OwnerUserID)
	}
	kind, name, err := p.shareLabel(rec, owner.Username)
	if err != nil {
		return err
	}
	what := "a " + kind
	if name != "" {
		what = fmt.Sprintf("the %s %q", kind, name)
	}
	ownerName := owner.DisplayName
	if ownerName == "" {
		ownerName = owner.Username
	}
	subject := fmt.Sprintf("%s shared %s with %s", ownerName, what, audience)

	var errs []error
	for _, id := range recipientIDs {
		u, ok := people[id]
		if !ok {
			continue // removed since
		}
		if err := p.Activities.Notification(u.Username, owner.Username, "sharing", "share_received", subject, rec.ResourceType, rec.ResourceID, name); err != nil {
			errs = append(errs, fmt.Errorf("notify %s: %w", u.Username, err))
		}
	}
	return errors.Join(errs...)
}

// shareLabel names what a share is of: its kind ("folder", "calendar") and,
// where the server can tell, its name.
func (p *Providers) shareLabel(rec share.Record, ownerUsername string) (kind, name string, err error) {
	switch rec.ResourceType {
	case volume.ShareResourceType:
		v, err := p.Volumes.GetByID(rec.ResourceID)
		if err != nil {
			return "", "", fmt.Errorf("volume %s: %w", rec.ResourceID, err)
		}
		return "volume", v.Name, nil
	case dav.ShareType:
		id, err := strconv.ParseInt(rec.ResourceID, 10, 64)
		if err != nil {
			return "", "", fmt.Errorf("collection id %q: %w", rec.ResourceID, err)
		}
		c, err := p.DAV.Collections.ByID(dav.CollectionID(id))
		if err != nil {
			return "", "", fmt.Errorf("collection %d: %w", id, err)
		}
		if c.Has(dav.VCARD) {
			return "address book", c.Title(), nil
		}
		return "calendar", c.Title(), nil
	case files.PluginID + ":file", files.PluginID + ":directory":
		kind = "file"
		if strings.HasSuffix(rec.ResourceType, ":directory") {
			kind = "folder"
		}
		id, err := strconv.ParseInt(rec.ResourceID, 10, 64)
		if err != nil {
			return "", "", fmt.Errorf("file id %q: %w", rec.ResourceID, err)
		}
		plug, ok := p.Plugins.Plugin(files.PluginID)
		if !ok {
			return kind, "", nil
		}
		fp, ok := plug.(*files.Plugin)
		if !ok {
			return kind, "", nil
		}
		meta, err := fp.GetByID(ownerUsername, id)
		if err != nil {
			return "", "", fmt.Errorf("file %d: %w", id, err)
		}
		return kind, path.Base(meta.Path), nil
	}
	// A plugin's resource: its type names what it is; its name is in the
	// plugin's own tables, which the host does not read.
	_, kind, _ = strings.Cut(rec.ResourceType, ":")
	return kind, "", nil
}
