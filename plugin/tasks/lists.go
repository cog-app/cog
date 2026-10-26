package tasks

import (
	"errors"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/dav"
)

// userID returns the ID of a user by username. Collections are keyed by user
// ID; plugin databases by username.
func (p *Plugin) userID(username string) (string, error) {
	users := p.ctx.UserLookup()
	if users == nil {
		return "", errors.New("tasks: no user lookup service")
	}
	u, err := users.GetByUsername(username)
	if err != nil {
		return "", fmt.Errorf("look up user %s: %w", username, err)
	}
	return u.ID, nil
}

// Lists returns a user's collections that hold tasks, creating the default
// calendar (events and tasks) when the user has no calendar collections.
func (p *Plugin) Lists(userID string) ([]TaskList, error) {
	cols, err := p.dav.Collections.EnsureDefault(userID, dav.HomeCalendars)
	if err != nil {
		return nil, err
	}
	var lists []TaskList
	for i := range cols {
		if cols[i].Has(dav.VTODO) {
			lists = append(lists, listFrom(&cols[i]))
		}
	}
	return lists, nil
}

func (p *Plugin) listMap(userID string) (map[int64]TaskList, error) {
	lists, err := p.Lists(userID)
	if err != nil {
		return nil, err
	}
	m := make(map[int64]TaskList, len(lists))
	for _, l := range lists {
		m[l.ID] = l
	}
	return m, nil
}

// List returns one of a user's own task lists, or dav.ErrNotFound.
func (p *Plugin) List(userID string, id int64) (*TaskList, error) {
	c, err := p.dav.Collections.ByID(dav.CollectionID(id))
	if err != nil {
		return nil, err
	}
	if c.OwnerID != userID || !c.Has(dav.VTODO) {
		return nil, dav.ErrNotFound
	}
	l := listFrom(c)
	return &l, nil
}

// CreateList creates a task list, a calendar collection that holds tasks,
// named after its display name.
func (p *Plugin) CreateList(userID, displayName, color string) (*TaskList, error) {
	if color == "" {
		color = "#0082c9"
	}
	c := &dav.Collection{
		OwnerID: userID, Home: dav.HomeCalendars, Name: dav.Slug(displayName, "tasks"),
		Components: []dav.Component{dav.VTODO}, DisplayName: displayName, Color: color,
	}
	if err := p.dav.Collections.CreateNamed(c); err != nil {
		return nil, err
	}
	l := listFrom(c)
	return &l, nil
}

// UpdateList renames or recolors one of a user's task lists.
func (p *Plugin) UpdateList(userID string, id int64, displayName, color string) error {
	c, err := p.dav.Collections.ByID(dav.CollectionID(id))
	if err != nil {
		return err
	}
	if c.OwnerID != userID || !c.Has(dav.VTODO) {
		return dav.ErrNotFound
	}
	c.DisplayName, c.Color = displayName, color
	return p.dav.Collections.Update(c)
}

// DeleteList deletes one of a user's task lists with everything in it: a
// collection holding events as well loses those too.
func (p *Plugin) DeleteList(username, userID string, id int64) error {
	c, err := p.dav.Collections.ByID(dav.CollectionID(id))
	if err != nil {
		return err
	}
	if c.OwnerID != userID || !c.Has(dav.VTODO) {
		return dav.ErrNotFound
	}
	return p.dav.DeleteCollection(username, c)
}
