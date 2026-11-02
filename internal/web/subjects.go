package web

import (
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// subjectRef names a share or quota subject.
type subjectRef struct {
	Type string // USER, GROUP, PUBLIC, INSTANCE, DEFAULT
	ID   string
}

// subjectNamer turns subjects into display names. It looks up every user and
// group subject of a list up front, one query per kind, so naming the rows of
// a panel doesn't cost a query each.
type subjectNamer struct {
	users  map[string]*user.User
	groups map[string]*group.Group
}

func newSubjectNamer(p *services.Providers, refs []subjectRef) (*subjectNamer, error) {
	var userIDs, groupIDs []string
	for _, ref := range refs {
		switch ref.Type {
		case "USER":
			userIDs = append(userIDs, ref.ID)
		case "GROUP":
			groupIDs = append(groupIDs, ref.ID)
		}
	}
	n := &subjectNamer{}
	var err error
	if n.users, err = p.Users.GetByIDs(userIDs); err != nil {
		return nil, fmt.Errorf("look up subject users: %w", err)
	}
	if n.groups, err = p.Groups.GetByIDs(groupIDs); err != nil {
		return nil, fmt.Errorf("look up subject groups: %w", err)
	}
	return n, nil
}

// name returns the display name of a subject. A user that no longer exists
// is shown as deletedUser, or by its ID when that is empty; a group that no
// longer exists, by its ID.
func (n *subjectNamer) name(ref subjectRef, deletedUser string) string {
	switch ref.Type {
	case "USER":
		u := n.users[ref.ID]
		if u == nil {
			if deletedUser != "" {
				return deletedUser
			}
			return ref.ID
		}
		if u.DisplayName != "" {
			return u.DisplayName
		}
		return u.Username
	case "GROUP":
		if g := n.groups[ref.ID]; g != nil {
			return g.Name
		}
		return ref.ID
	case "DEFAULT":
		return "Default"
	case "PUBLIC":
		return "Public link"
	case "INSTANCE":
		return "Everyone"
	}
	return ref.ID
}
