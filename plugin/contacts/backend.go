package contacts

import (
	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
)

// backend serves cards to the DAV core. Cards are stored as rows and their
// text is generated on read (rows.go).
type backend struct {
	dav.Table
	p *Plugin
}

var _ dav.Backend = (*backend)(nil)

func newBackend(p *Plugin) *backend {
	b := &backend{p: p}
	b.Table = dav.Table{Name: "cards", Component: dav.VCARD, DB: p.DB, Render: b.render}
	return b
}

func (b *backend) Key() string { return "c" }

func (b *backend) render(q davsql.Querier, c *dav.Collection, ids []int64) (map[int64][]byte, error) {
	owner, err := b.p.username(c.OwnerID)
	if err != nil {
		return nil, err
	}
	return renderIDs(q, b.p.photoStore(owner), ids)
}

func (b *backend) Put(owner string, c *dav.Collection, req *dav.PutRequest) (*dav.PutResult, error) {
	s, err := b.p.Store(owner)
	if err != nil {
		return nil, err
	}
	st, err := s.put(int64(c.ID), req.URI, vcard.Decode(req.Object), req.Conditions, b.p.ctx.Now())
	if err != nil {
		return nil, err
	}
	return &dav.PutResult{Version: st.Version, Created: st.Created, Data: st.Data}, nil
}

func (b *backend) Delete(owner string, c *dav.Collection, uri string, cond davsql.Conditions) error {
	s, err := b.p.Store(owner)
	if err != nil {
		return err
	}
	return s.remove(int64(c.ID), uri, cond, b.p.ctx.Now())
}

// Purge removes a deleted collection's cards and the photos only they used.
func (b *backend) Purge(owner string, id dav.CollectionID) error {
	s, err := b.p.Store(owner)
	if err != nil {
		return err
	}
	return s.purge(int64(id))
}
