package contacts

import (
	"slices"

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

var (
	_ dav.Backend     = (*backend)(nil)
	_ dav.CardQuerier = (*backend)(nil)
)

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

// QueryCards answers an addressbook-query. The filter is matched against
// each card as Cog writes it, from models loaded in a fixed number of
// queries without reading photo files; only the cards that match are
// rendered.
func (b *backend) QueryCards(owner string, c *dav.Collection, f *dav.CardFilter, withData bool) ([]dav.Resource, bool, error) {
	s, err := b.p.Store(owner)
	if err != nil {
		return nil, false, err
	}
	ids, err := cardIDs(s.DB, c.ID)
	if err != nil {
		return nil, false, err
	}
	cards, err := loadCards(s.DB, nil, ids)
	if err != nil {
		return nil, false, err
	}
	var hits []int64
	truncated := false
	for _, id := range ids {
		card := cards[id]
		if card == nil || !f.Match(card.Encode()) {
			continue
		}
		if f.Limit > 0 && len(hits) == f.Limit {
			truncated = true
			break
		}
		hits = append(hits, id)
	}
	var out []dav.Resource
	for chunk := range slices.Chunk(hits, loadChunk) {
		list, err := b.Query(s.DB, c, withData, "collection_id = ? AND id IN ("+placeholders(len(chunk))+")", append([]any{c.ID}, anys(chunk)...)...)
		if err != nil {
			return nil, false, err
		}
		out = append(out, list...)
	}
	return out, truncated, nil
}
