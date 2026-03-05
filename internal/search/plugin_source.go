package search

import (
	"database/sql"
	"fmt"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin"
)

// PluginSearchSource adapts a plugin.SearchSourceDef into a SearchSource.
type PluginSearchSource struct {
	pluginID string
	def      plugin.SearchSourceDef
	// FilesDir is the user's files directory, set by userPluginSource before Enrich.
	FilesDir string
}

// NewPluginSearchSource creates a SearchSource from a plugin's search definition.
func NewPluginSearchSource(pluginID string, def plugin.SearchSourceDef) *PluginSearchSource {
	return &PluginSearchSource{pluginID: pluginID, def: def}
}

func (s *PluginSearchSource) Name() string     { return s.pluginID + ":" + s.def.PrimaryTable }
func (s *PluginSearchSource) Tables() []string { return []string{s.def.PrimaryTable} }

func (s *PluginSearchSource) Enrich(db *sql.DB, rowIDs []int64) ([]Document, error) {
	if len(rowIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(rowIDs))
	args := make([]any, len(rowIDs))
	for i, id := range rowIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// Replace the placeholder in the enrichment SQL.
	query := strings.Replace(s.def.EnrichSQL, "(?)", "("+strings.Join(placeholders, ",")+")", 1)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("enrich %s: %w", s.Name(), err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("columns %s: %w", s.Name(), err)
	}

	// Build column index for mapping.
	colIdx := make(map[string]int, len(cols))
	for i, c := range cols {
		colIdx[c] = i
	}

	var docs []Document
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan %s: %w", s.Name(), err)
		}

		// rowid must be the first column.
		rowid, ok := vals[0].(int64)
		if !ok {
			continue
		}

		fields := map[string]any{
			"source": s.pluginID,
		}
		for _, f := range s.def.Fields {
			if idx, ok := colIdx[f.Column]; ok {
				if v := vals[idx]; v != nil {
					switch sv := v.(type) {
					case string:
						fields[f.Field] = sv
					case []byte:
						fields[f.Field] = string(sv)
					default:
						fields[f.Field] = fmt.Sprint(sv)
					}
				}
			}
		}

		// If ContentFunc is set, build a cols map and call it.
		if s.def.ContentFunc != nil && s.FilesDir != "" {
			colMap := make(map[string]string, len(cols))
			for ci, c := range cols {
				if v := vals[ci]; v != nil {
					switch sv := v.(type) {
					case string:
						colMap[c] = sv
					case []byte:
						colMap[c] = string(sv)
					default:
						colMap[c] = fmt.Sprint(sv)
					}
				}
			}
			if content := s.def.ContentFunc(s.FilesDir, colMap); content != "" {
				fields["content"] = content
			}
		}

		docs = append(docs, Document{
			ID:     fmt.Sprintf("%s:%d", s.Name(), rowid),
			Fields: fields,
		})
	}
	return docs, rows.Err()
}
