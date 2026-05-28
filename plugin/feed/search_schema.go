package feed

import "git.duckfam.us/jj-software/cog/internal/query"

// searchSchema is the feed plugin's declarative search schema. Used by the
// host's bleve index (Plan A) and by the SQL compiler in this package (Plan B).
var searchSchema = &query.Schema{
	PrimaryAlias: "i",
	Fields: map[string]query.FieldSpec{
		"title":        {Type: query.Text, Expr: "i.title"},
		"author":       {Type: query.Text, Expr: "i.author"},
		"content":      {Type: query.Text, Expr: "i.content_html"},
		"url":          {Type: query.Keyword, Expr: "i.url"},
		"type":         {Type: query.Keyword, Expr: "i.type"},
		"kind":         {Type: query.Keyword, Expr: "s.kind"},
		"subscription": {Type: query.Keyword, Expr: "s.title"},
		"folder":       {Type: query.Keyword, Expr: "f.name", Joins: []string{"folders"}},
		"tag": {
			Type:        query.ManyToMany,
			Join:        "item_tags",
			JoinFK:      "item_id",
			ValueTable:  "tags",
			ValueFK:     "tag_id",
			ValueColumn: "name",
		},
		"is": {
			Type: query.EnumPredicate,
			Values: map[string]string{
				"unread":     "i.is_read = 0",
				"starred":    "i.is_starred = 1",
				"downloaded": "EXISTS (SELECT 1 FROM downloads d WHERE d.item_id = i.id AND d.state = 'ready')",
			},
		},
		"published": {Type: query.DateText, Expr: "i.published_at"},
	},
	Joins: map[string]string{
		"folders": "LEFT JOIN folders f ON f.id = s.folder_id",
	},
}

// SearchSchema returns the feed plugin's search schema. Used by the host's
// global search index (registered via SearchSourceDef.Schema) and by the SQL
// compiler at query time (Plan B).
func SearchSchema() *query.Schema { return searchSchema }
