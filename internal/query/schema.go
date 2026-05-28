package query

import (
	"fmt"
	"regexp"
	"sort"
)

// FieldType classifies what kind of value a schema field holds. Determines
// how the bleve indexer maps it (Plan A) and how the SQL compiler (Plan B)
// emits a predicate for it.
type FieldType int

const (
	Text          FieldType = iota // free-text; tokenized in bleve; LIKE %v% in SQL; bare-word scan
	Keyword                        // exact match; bleve keyword; = in SQL
	Bool                           // 0/1
	Number                         // numeric
	DateText                       // RFC3339 string
	ManyToMany                     // multi-valued via join table (e.g. tag)
	EnumPredicate                  // pseudo-field with per-value SQL predicate (e.g. is)
)

// FieldSpec describes one queryable field. Different types use different
// subsets of fields; kept in one struct for simplicity.
type FieldSpec struct {
	Type FieldType

	// For Text, Keyword, Bool, Number, DateText:
	Expr  string   // SQL column expression (Plan B), e.g. "i.title". Unused in bleve.
	Joins []string // names of schema joins required by Expr (Plan B).

	// For ManyToMany (e.g. tag):
	Join        string // join-table name
	JoinFK      string // FK on join-table referring to the primary table
	ValueTable  string // table holding the value
	ValueFK     string // FK on join-table referring to ValueTable
	ValueColumn string // value column on ValueTable

	// For EnumPredicate (e.g. is):
	Values map[string]string // value-name -> SQL boolean predicate
}

// Schema is one plugin's declaration of its searchable fields.
type Schema struct {
	// PrimaryAlias is the SQL alias FieldSpec.Expr expressions reference. Most
	// schemas use "i" for the primary item table. The SQL compiler does not
	// interpret this; it is documentation for FieldSpec authors.
	PrimaryAlias string

	// Fields is the set of queryable field names (keys match cog-grammar
	// `:field=value` syntax).
	Fields map[string]FieldSpec

	// Joins maps a join-name (referenced by FieldSpec.Joins) to a literal SQL
	// JOIN fragment, e.g. "LEFT JOIN folders f ON f.id = s.folder_id".
	Joins map[string]string
}

// DefaultTextFields returns the field names included in bare-word OR-LIKE
// scans, sorted for determinism. Convention: every Text field is included
// (mirrors bleve's _all behavior).
func (s Schema) DefaultTextFields() []string {
	out := make([]string, 0, len(s.Fields))
	for name, f := range s.Fields {
		if f.Type == Text {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ScalarFields returns the names of fields backed by a single SQL column
// expression (Text, Keyword, DateText), sorted. Used by the SQL compiler for
// bare-word substring scans across all column-backed fields — matching the Go
// matcher, which scans every record value. ManyToMany and EnumPredicate are
// excluded (no scalar column to LIKE against).
func (s Schema) ScalarFields() []string {
	out := make([]string, 0, len(s.Fields))
	for name, f := range s.Fields {
		switch f.Type {
		case Text, Keyword, DateText:
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

var fieldNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Validate reports structural problems.
func (s Schema) Validate() error {
	referenced := map[string]bool{}
	for name, f := range s.Fields {
		if !fieldNameRE.MatchString(name) {
			return fmt.Errorf("schema: field name %q does not match [a-z][a-z0-9_]*", name)
		}
		switch f.Type {
		case Text, Keyword, Bool, Number, DateText:
			if f.Expr == "" {
				return fmt.Errorf("schema: field %q (%v) requires Expr", name, f.Type)
			}
		case ManyToMany:
			if f.Join == "" || f.JoinFK == "" || f.ValueTable == "" || f.ValueFK == "" || f.ValueColumn == "" {
				return fmt.Errorf("schema: field %q (ManyToMany) requires Join, JoinFK, ValueTable, ValueFK, ValueColumn", name)
			}
		case EnumPredicate:
			if len(f.Values) == 0 {
				return fmt.Errorf("schema: field %q (EnumPredicate) requires non-empty Values", name)
			}
			for v, pred := range f.Values {
				if pred == "" {
					return fmt.Errorf("schema: field %q value %q has empty predicate", name, v)
				}
			}
		default:
			return fmt.Errorf("schema: field %q has unknown type %d", name, f.Type)
		}
		for _, j := range f.Joins {
			referenced[j] = true
			if _, ok := s.Joins[j]; !ok {
				return fmt.Errorf("schema: field %q references missing join %q", name, j)
			}
		}
	}
	for j := range s.Joins {
		if !referenced[j] {
			return fmt.Errorf("schema: declared join %q is never referenced", j)
		}
	}
	return nil
}
