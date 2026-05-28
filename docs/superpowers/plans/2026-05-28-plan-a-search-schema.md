# Plan A: Unified `query.Schema` + Global-Search Richening

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Define a per-plugin search schema in `internal/query` (one declarative struct, no plugin-specific knowledge in `query`), refactor the global bleve search to consume it, and have plugins register fields including computed states (`is:unread`, `is:downloaded`, `tag:*`). After this, `is:downloaded` actually works in the global search; Plan B (separate doc) reuses the same Schema to compile the cog-grammar to SQL.

**Why Plan A first:** the global search currently has a fixed bleve mapping (`_id/name/subtext/content/source`). Plugin field queries like `is:downloaded` or `tag:weekly` pass through `toBleveQuery` and reference fields the index doesn't know about — so they silently return nothing. Fixing this requires plugins to declare their searchable fields, the indexer to build the bleve mapping from that declaration, and sources to populate the corresponding document values. The same declaration also drives Plan B's SQL compiler — defining it once here avoids two parallel schemas.

**Tech Stack:** Go, SQLite, bleve v2 (`github.com/blevesearch/bleve/v2`), `internal/query`, `internal/search`, plugin SDK (`plugin/`, `internal/plugin/`).

---

## Conventions

- Tests via `internal/testing/cogtest`/`require`. Bleve index tests live in `internal/search`.
- `query.Schema` lives in `internal/query` — **no** imports from `plugin/` or `internal/search`. Pure data + small helpers.
- WE DO NOT SWALLOW ERRORS (CLAUDE.md). Wrap+return or `slog`.
- Commit per task.
- Backwards-compat: plugins not yet migrated keep the legacy `SearchSourceDef{PrimaryTable, EnrichSQL}` path; new plugins (and feed, in this plan) move to `Schema`-based registration. The legacy path remains until all plugins migrate (out of scope for this plan).

## Background (current state)

- `plugin.SearchSourceDef{PrimaryTable string; EnrichSQL string}` — positional SQL: rowid, name, subtext, content+.
- `internal/search/plugin_source.go` adapts that into a `SearchSource{Name, Tables, Enrich}` returning `Document{ID, Fields: map[string]any}` with only `name`, `subtext`, `content`, `source` populated.
- `internal/search/index.go buildMapping` hardcodes the bleve field set; `executeSearch` requests fields `_id/name/subtext/content/source`.
- `internal/search/bleve_query.go toBleveQuery` translates cog-grammar `FieldEqExpr`/`FieldInExpr` to `bleve.NewTermQuery(value).SetField(field)`. If the bleve index doesn't have that field, queries silently miss — this is the root reason `is:downloaded`, `is:unread`, `is:starred`, `tag:*` don't work in global search.

## File structure (what changes)

- **Create:** `internal/query/schema.go` — `FieldType`, `FieldSpec`, `Schema`. Pure data + tiny helpers.
- **Create:** `internal/query/schema_test.go` — schema validation table tests.
- **Modify:** `plugin/types.go` (or wherever `SearchSourceDef` lives — verify with `grep -n 'type SearchSourceDef' plugin/`) — add an optional `Schema query.Schema` field. Keep `EnrichSQL` for the legacy path.
- **Modify:** `internal/search/index.go` — build the bleve `DocumentMapping` from registered schemas (one mapping; merge fields across sources, namespacing collisions by source name). Update `executeSearch` to request all registered fields, not the hardcoded five.
- **Modify:** `internal/search/plugin_source.go` — when `def.Schema` is set, use it to enrich; the plugin supplies values for every declared field at index time.
- **Modify:** `plugin/feed/feed.go` — register a `query.Schema` describing item fields (title/author/content/url/type/kind/subscription/folder/tag/is + published).
- **Modify:** `plugin/feed/store.go` — add an Enrich-shaped method that materializes the schema fields for a set of item IDs (computes `tag`, `is`, `folder`).

## The schema in `internal/query`

```go
// FieldType classifies what kind of value a schema field holds, which
// determines both how the bleve indexer maps it and how the SQL compiler
// (Plan B) emits a predicate for it.
type FieldType int

const (
	Text       FieldType = iota // free-text; tokenized in bleve; LIKE %v% in SQL; included in bare-word scan
	Keyword                     // exact match; not tokenized; = in SQL; NOT bare-word-scanned
	Bool                        // 0/1; bleve BooleanFieldMapping; SQL = 0/1
	Number                      // numeric; bleve NumericFieldMapping; SQL = (and future >, <)
	DateText                    // RFC3339 string; bleve DateTimeFieldMapping; SQL strftime equality
	ManyToMany                  // multi-valued via join table (e.g. tag); bleve multi-valued keyword; SQL EXISTS subquery
	EnumPredicate               // pseudo-field with per-value SQL predicate (e.g. is); bleve multi-valued keyword
)

// FieldSpec describes one field in a plugin's search schema. Different types
// use different subsets of fields — kept in one struct for simplicity rather
// than a type-tagged union.
type FieldSpec struct {
	Type FieldType

	// Used by Text, Keyword, Bool, Number, DateText:
	Expr  string   // SQL column expression (Plan B), e.g. "i.title". Unused in bleve.
	Joins []string // names of schema joins required by Expr (Plan B), e.g. ["folders"].

	// Used by ManyToMany (e.g. tag):
	Join        string // join-table name, e.g. "item_tags"
	JoinFK      string // FK column on join-table referring to the primary table, e.g. "item_id"
	ValueTable  string // table holding the value, e.g. "tags"
	ValueFK     string // FK column on join-table referring to ValueTable, e.g. "tag_id"
	ValueColumn string // value column on ValueTable, e.g. "name"

	// Used by EnumPredicate (e.g. is):
	Values map[string]string // value-name -> SQL boolean predicate, e.g. "unread" -> "i.is_read = 0"
}

// Schema is one plugin's declaration of its searchable fields. Two consumers:
// the bleve indexer (this plan) and the SQL compiler (Plan B).
type Schema struct {
	// PrimaryAlias is the SQL alias the FieldSpec.Expr expressions reference.
	// Most schemas use "i" for the primary item table. The SQL compiler does
	// not interpret this; it's documentation for FieldSpec authors.
	PrimaryAlias string

	// Fields is the set of queryable field names. Keys are the names that
	// appear in cog-grammar `:field=value` syntax.
	Fields map[string]FieldSpec

	// Joins maps a join-name (referenced by FieldSpec.Joins) to a literal SQL
	// JOIN fragment, e.g. "LEFT JOIN folders f ON f.id = s.folder_id". Used
	// by the SQL compiler (Plan B). Unused by the bleve indexer.
	Joins map[string]string
}

// DefaultTextFields returns the field names included in bare-word OR-LIKE scans.
// Convention (matches existing bleve behavior): every Text field is included.
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

// Validate reports structural problems (missing required FieldSpec keys per
// type, unknown join references, duplicate field names across types).
func (s Schema) Validate() error { ... }
```

`PrimaryAlias` is purely documentary — the SQL compiler will not parse Exprs. Plugin authors keep aliases consistent across their FieldSpecs (i for items, s for the joined parent table, etc.) by convention.

## How bleve consumes Schema

For each registered Schema, the indexer maps fields:

| `FieldType`     | bleve mapping                                                                                          |
| --------------- | ------------------------------------------------------------------------------------------------------ |
| `Text`          | `NewTextFieldMapping()`, `Store: true`                                                                 |
| `Keyword`       | `NewKeywordFieldMapping()`, `Store: true`                                                              |
| `Bool`          | `NewBooleanFieldMapping()`, `Store: true`                                                              |
| `Number`        | `NewNumericFieldMapping()`, `Store: true`                                                              |
| `DateText`      | `NewDateTimeFieldMapping()`, `Store: true`                                                             |
| `ManyToMany`    | `NewKeywordFieldMapping()`, `Store: true` — value emitted as a `[]string` per doc                      |
| `EnumPredicate` | `NewKeywordFieldMapping()`, `Store: true` — value emitted as a `[]string` of active value names per doc |

Field name collisions across sources: prefix by source name (e.g. `feed.is`, `users.name`). Plugin authors don't write the prefix; the indexer applies it. Search field references in queries get the same prefix lookup at search time — see "Cross-source query routing" below.

## How sources populate Schema fields at index time

Replace the positional `EnrichSQL` with a typed enrichment when `def.Schema` is set:

```go
// SchemaEnricher is the interface a plugin's search source implements to
// supply per-field values for the documents it owns. Called by the indexer
// when (re)indexing rowIDs. Each returned Document.Fields map keys match the
// Schema.Fields keys exactly (the indexer prefixes with the source name).
type SchemaEnricher interface {
	EnrichSchema(db *sql.DB, rowIDs []int64) ([]Document, error)
}
```

`plugin.SearchSourceDef` gains:

```go
type SearchSourceDef struct {
	PrimaryTable string
	EnrichSQL    string          // legacy positional path; ignored when Schema != nil
	Schema       *query.Schema   // optional declarative schema
	Enricher     SchemaEnricher  // required when Schema != nil; called instead of EnrichSQL
}
```

The feed plugin implements `Enricher.EnrichSchema` against its store: given item IDs, return Documents whose Fields populate every Schema field (computed `is` = `[]string{"unread"?, "starred"?, "downloaded"?}`, `tag` = the item's tag names, `folder` = parent folder name, etc.).

## Cross-source query routing

Today's search accepts a single string and routes via bleve's field-aware query. With per-source field namespacing, a user query of `is:downloaded` is ambiguous (which source's `is`?). Two operating modes:

- **Global** (current default): `is:downloaded` resolves to **every** registered source's `is.downloaded`. The query is rewritten by the indexer into a disjunction across all sources that declare that field: `(feed.is:downloaded OR other.is:downloaded OR ...)`. Sources that don't declare the field contribute nothing.
- **Scoped**: `source:feed is:downloaded` — `source:` field already exists in the current mapping; combine with the field query as an AND.

The disjunction rewrite happens inside `toBleveQuery`: when a `FieldEqExpr`/`FieldInExpr` references a field that exists in multiple sources (post-prefixing), the indexer emits a bleve `DisjunctionQuery` over the prefixed variants. If only one source has it, the single prefixed query is emitted directly. If no source has it, the term is dropped with a warning (or made into a no-op match-none; document the choice).

## Validation rules

`Schema.Validate` enforces:

- Field name is non-empty, lowercase ASCII identifier (`[a-z][a-z0-9_]*`).
- `Text|Keyword|Bool|Number|DateText` require `Expr != ""`.
- `ManyToMany` requires `Join`, `JoinFK`, `ValueTable`, `ValueFK`, `ValueColumn` all non-empty.
- `EnumPredicate` requires `Values` non-empty; each predicate non-empty.
- Every `Joins` entry referenced by some `FieldSpec.Joins` must exist in `Schema.Joins` (and vice-versa — declared joins must be referenced; else they're dead).

---

## Task 1: Define `query.Schema`

**Files:**
- Create: `internal/query/schema.go`
- Create: `internal/query/schema_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/query/schema_test.go`:

```go
package query

import (
	"reflect"
	"strings"
	"testing"
)

func textSchema() Schema {
	return Schema{
		PrimaryAlias: "i",
		Fields: map[string]FieldSpec{
			"title":  {Type: Text, Expr: "i.title"},
			"author": {Type: Text, Expr: "i.author"},
			"type":   {Type: Keyword, Expr: "i.type"},
			"folder": {Type: Keyword, Expr: "f.name", Joins: []string{"folders"}},
			"tag": {
				Type:        ManyToMany,
				Join:        "item_tags", JoinFK: "item_id",
				ValueTable:  "tags", ValueFK: "tag_id", ValueColumn: "name",
			},
			"is": {
				Type: EnumPredicate,
				Values: map[string]string{
					"unread":     "i.is_read = 0",
					"starred":    "i.is_starred = 1",
					"downloaded": "EXISTS (SELECT 1 FROM downloads d WHERE d.item_id = i.id AND d.state = 'ready')",
				},
			},
		},
		Joins: map[string]string{
			"folders": "LEFT JOIN folders f ON f.id = s.folder_id",
		},
	}
}

func TestSchemaValidateOK(t *testing.T) {
	if err := textSchema().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaDefaultTextFields(t *testing.T) {
	got := textSchema().DefaultTextFields()
	want := []string{"author", "title"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSchemaValidateFieldNameRules(t *testing.T) {
	s := Schema{Fields: map[string]FieldSpec{"BadName": {Type: Text, Expr: "x"}}}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "field name") {
		t.Fatalf("want field-name error, got %v", err)
	}
}

func TestSchemaValidateRequiresExpr(t *testing.T) {
	s := Schema{Fields: map[string]FieldSpec{"title": {Type: Text}}}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "Expr") {
		t.Fatalf("want Expr error, got %v", err)
	}
}

func TestSchemaValidateManyToManyRequiresAll(t *testing.T) {
	s := Schema{Fields: map[string]FieldSpec{"tag": {Type: ManyToMany, Join: "item_tags"}}}
	if err := s.Validate(); err == nil {
		t.Fatal("want ManyToMany missing-field error")
	}
}

func TestSchemaValidateEnumPredicateRequiresValues(t *testing.T) {
	s := Schema{Fields: map[string]FieldSpec{"is": {Type: EnumPredicate}}}
	if err := s.Validate(); err == nil {
		t.Fatal("want EnumPredicate empty-Values error")
	}
}

func TestSchemaValidateJoinReferencesMustExist(t *testing.T) {
	s := Schema{
		Fields: map[string]FieldSpec{"folder": {Type: Keyword, Expr: "f.name", Joins: []string{"missing"}}},
		Joins:  map[string]string{},
	}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("want join-ref error, got %v", err)
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`Schema`/`FieldSpec`/etc. undefined).

`go test ./internal/query`

- [ ] **Step 3: Implement `internal/query/schema.go`**

```go
package query

import (
	"fmt"
	"regexp"
	"sort"
)

type FieldType int

const (
	Text FieldType = iota
	Keyword
	Bool
	Number
	DateText
	ManyToMany
	EnumPredicate
)

type FieldSpec struct {
	Type FieldType

	Expr  string
	Joins []string

	Join        string
	JoinFK      string
	ValueTable  string
	ValueFK     string
	ValueColumn string

	Values map[string]string
}

type Schema struct {
	PrimaryAlias string
	Fields       map[string]FieldSpec
	Joins        map[string]string
}

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

var fieldNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

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
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/query`

- [ ] **Step 5: Commit**

```bash
git add internal/query/schema.go internal/query/schema_test.go
git commit -m "query: unified Schema/FieldSpec for plugin search metadata"
```

---

## Task 2: Bleve indexer consumes Schema

**Files:**
- Modify: `internal/search/index.go`
- Modify: `internal/search/source.go` (add `SchemaEnricher` interface and the Schema-driven index path)
- Create: `internal/search/schema_mapping.go` — turn one or more `query.Schema`s into a `mapping.IndexMapping`
- Create: `internal/search/schema_mapping_test.go`

The indexer now accepts a set of `(sourceName, *query.Schema)` registrations. The schema-driven path coexists with the legacy fixed mapping until all plugins migrate.

- [ ] **Step 1: Write failing test for the mapping builder**

`internal/search/schema_mapping_test.go`:

```go
package search

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/query"
)

func TestBuildSchemaMappingMapsTextAndKeyword(t *testing.T) {
	sc := query.Schema{
		Fields: map[string]query.FieldSpec{
			"title": {Type: query.Text, Expr: "i.title"},
			"type":  {Type: query.Keyword, Expr: "i.type"},
		},
	}
	m, err := buildSchemaMapping(map[string]*query.Schema{"feed": &sc})
	if err != nil {
		t.Fatal(err)
	}
	// The "feed.title" field must be a text-mapped child; "feed.type" keyword.
	// (Exact assertion: walk the bleve DocumentMapping; verify field analyzers.)
	// Minimum sanity: the resulting IndexMapping isn't nil.
	if m == nil {
		t.Fatal("nil mapping")
	}
}
```

(Bleve's introspection on a built mapping is verbose; the deeper check is the round-trip test in Task 4. Keep this test as a smoke test.)

- [ ] **Step 2: Run — expect FAIL** (`buildSchemaMapping` undefined).

- [ ] **Step 3: Implement `internal/search/schema_mapping.go`**

```go
package search

import (
	"fmt"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	"git.duckfam.us/jj-software/cog/internal/query"
)

// buildSchemaMapping turns one or more plugin Schemas into a single bleve
// IndexMapping. Field names are prefixed by source name (e.g. "feed.title")
// to namespace across plugins. A "source" keyword field is added for
// scoping.
func buildSchemaMapping(schemas map[string]*query.Schema) (mapping.IndexMapping, error) {
	doc := bleve.NewDocumentMapping()
	doc.AddFieldMappingsAt("_id", bleve.NewKeywordFieldMapping())
	doc.AddFieldMappingsAt("source", bleve.NewKeywordFieldMapping())
	for src, sc := range schemas {
		if sc == nil {
			continue
		}
		for name, f := range sc.Fields {
			fm, err := bleveMappingFor(f)
			if err != nil {
				return nil, fmt.Errorf("schema %q field %q: %w", src, name, err)
			}
			doc.AddFieldMappingsAt(src+"."+name, fm)
		}
	}
	m := bleve.NewIndexMapping()
	m.DefaultMapping = doc
	return m, nil
}

func bleveMappingFor(f query.FieldSpec) (*mapping.FieldMapping, error) {
	switch f.Type {
	case query.Text:
		fm := bleve.NewTextFieldMapping()
		fm.Store = true
		return fm, nil
	case query.Keyword, query.ManyToMany, query.EnumPredicate:
		fm := bleve.NewKeywordFieldMapping()
		fm.Store = true
		return fm, nil
	case query.Bool:
		fm := bleve.NewBooleanFieldMapping()
		fm.Store = true
		return fm, nil
	case query.Number:
		fm := bleve.NewNumericFieldMapping()
		fm.Store = true
		return fm, nil
	case query.DateText:
		fm := bleve.NewDateTimeFieldMapping()
		fm.Store = true
		return fm, nil
	default:
		return nil, fmt.Errorf("unknown field type %d", f.Type)
	}
}
```

- [ ] **Step 4: Wire it into `Index.Open` (or wherever the mapping is currently built)**

Today `buildMapping()` (index.go:132) returns a fixed mapping. Add a constructor variant:

```go
// OpenWithSchemas opens (or creates) an index whose mapping derives from the
// given per-source schemas. Falls back to the legacy mapping when schemas is
// empty.
func OpenWithSchemas(path string, schemas map[string]*query.Schema) (*Index, error) {
	if len(schemas) == 0 {
		return Open(path) // legacy
	}
	m, err := buildSchemaMapping(schemas)
	if err != nil {
		return nil, fmt.Errorf("schema mapping: %w", err)
	}
	idx, err := bleve.New(path, m)
	if err == bleve.ErrorIndexPathExists {
		idx, err = bleve.Open(path)
	}
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}
	return &Index{idx: idx}, nil
}
```

The caller (likely `internal/web` or `services`) constructs the schemas map by walking registered plugins' `SearchSourceDef.Schema` values. Wiring change deferred to Task 4 once `SearchSourceDef.Schema` exists.

- [ ] **Step 5: Run smoke test — expect PASS**

- [ ] **Step 6: Commit**

```bash
git add internal/search/schema_mapping.go internal/search/schema_mapping_test.go internal/search/index.go
git commit -m "search: build bleve mapping from per-source query.Schema"
```

---

## Task 3: Extend `plugin.SearchSourceDef` with Schema + Enricher

**Files:**
- Modify: `plugin/types.go` (or wherever `SearchSourceDef` is defined — confirm with `grep -n 'type SearchSourceDef' plugin/`).
- Modify: `internal/plugin/plugin.go` (it currently `type SearchSourceDef = cogplugin.SearchSourceDef`).
- Modify: `internal/search/source.go` — add `SchemaEnricher` interface.
- Modify: `internal/search/plugin_source.go` — when `Schema != nil`, use `Enricher.EnrichSchema` instead of running `EnrichSQL`.

- [ ] **Step 1: Write failing test exercising the new path**

`internal/search/schema_source_test.go`:

```go
package search

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/query"
)

type fakeEnricher struct{ docs []Document }

func (f *fakeEnricher) EnrichSchema(_ *sql.DB, ids []int64) ([]Document, error) {
	return f.docs, nil
}

func TestPluginSearchSourceUsesSchemaPath(t *testing.T) {
	sc := query.Schema{Fields: map[string]query.FieldSpec{"title": {Type: query.Text, Expr: "i.title"}}}
	en := &fakeEnricher{docs: []Document{{ID: "feed:1", Fields: map[string]any{"title": "X"}}}}
	s := NewPluginSearchSource("feed", plugin.SearchSourceDef{Schema: &sc, Enricher: en})
	got, err := s.Enrich(nil, []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Fields["title"] != "X" {
		t.Fatalf("got %+v", got)
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Extend `SearchSourceDef`**

In `plugin/types.go` (or equivalent):

```go
type SearchSourceDef struct {
	PrimaryTable string
	EnrichSQL    string

	// New (optional): when both Schema and Enricher are set, the search
	// indexer uses the schema-driven path (bleve mapping from Schema; docs
	// from Enricher). EnrichSQL is ignored on that path.
	Schema   *query.Schema
	Enricher SchemaEnricher
}

// SchemaEnricher is implemented by sources that supply Documents whose
// Fields populate the registered Schema's fields directly.
type SchemaEnricher interface {
	EnrichSchema(db *sql.DB, rowIDs []int64) ([]Document, error)
}
```

Confirm where `Document` is defined; if it's in `internal/search` not `plugin/`, choose an interface location that doesn't create import cycles. Likely move `SchemaEnricher` (and its `Document` shape) into `plugin/` as a thin contract type, and have `internal/search` consume it.

If `internal/search`'s `Document` type already matches what's needed (`{ID string; Fields map[string]any}`), re-export the relevant minimal type into `plugin/` to keep import direction clean (`plugin/` ← consumed by `internal/search` and other consumers; `internal/search` may import `plugin/` but not vice-versa).

- [ ] **Step 4: Update `plugin_source.go`**

```go
func (s *PluginSearchSource) Enrich(db *sql.DB, rowIDs []int64) ([]Document, error) {
	if s.def.Schema != nil && s.def.Enricher != nil {
		docs, err := s.def.Enricher.EnrichSchema(db, rowIDs)
		if err != nil {
			return nil, fmt.Errorf("enrich schema %s: %w", s.pluginID, err)
		}
		// Prefix every field key with the source name (no collision across plugins).
		for i := range docs {
			pref := make(map[string]any, len(docs[i].Fields)+1)
			pref["source"] = s.pluginID
			for k, v := range docs[i].Fields {
				pref[s.pluginID+"."+k] = v
			}
			docs[i].Fields = pref
		}
		return docs, nil
	}
	// ... existing positional EnrichSQL path unchanged ...
}
```

- [ ] **Step 5: Run + commit**

```bash
git add plugin/types.go internal/plugin/plugin.go internal/search/source.go internal/search/plugin_source.go internal/search/schema_source_test.go
git commit -m "search: SearchSourceDef gains Schema + SchemaEnricher; plugin_source uses schema path"
```

---

## Task 4: Cross-source query routing in `toBleveQuery`

**Files:**
- Modify: `internal/search/bleve_query.go` — accept a registry that knows which sources declare which fields.
- Modify: `internal/search/index.go` — pass that registry to `toBleveQuery`.
- Add tests for the rewrite.

The query `is:downloaded` must become `feed.is:downloaded OR <other source>.is:downloaded` (across all sources that declare `is`). Otherwise the field is not in the index.

- [ ] **Step 1: Write the failing test**

```go
func TestToBleveQueryFanOutsFieldEqAcrossSources(t *testing.T) {
	schemas := map[string]*query.Schema{
		"feed":  {Fields: map[string]query.FieldSpec{"is": {Type: query.EnumPredicate, Values: map[string]string{"downloaded": "x"}}}},
		"other": {Fields: map[string]query.FieldSpec{"is": {Type: query.EnumPredicate, Values: map[string]string{"downloaded": "y"}}}},
	}
	reg := newSchemaRegistry(schemas)
	expr := &query.FieldEqExpr{Field: "is", Value: "downloaded"}
	q := toBleveQueryWithSchema(expr, reg)
	// Expect a DisjunctionQuery over feed.is and other.is term queries.
	// Bleve doesn't expose disjuncts as a public slice; introspect via reflect or
	// match on the query string representation. Use whichever bleve exposes.
}
```

- [ ] **Step 2: Implement `schemaRegistry` + `toBleveQueryWithSchema`**

```go
type schemaRegistry struct {
	fieldSources map[string][]string // field name -> sorted []source names that declare it
}

func newSchemaRegistry(schemas map[string]*query.Schema) *schemaRegistry {
	r := &schemaRegistry{fieldSources: map[string][]string{}}
	for src, sc := range schemas {
		if sc == nil {
			continue
		}
		for name := range sc.Fields {
			r.fieldSources[name] = append(r.fieldSources[name], src)
		}
	}
	for k := range r.fieldSources {
		sort.Strings(r.fieldSources[k])
	}
	return r
}

func (r *schemaRegistry) prefixed(field string) []string { return r.fieldSources[field] }
```

Modify `toBleveQuery` to accept the registry; the `FieldEqExpr`/`FieldInExpr` branches fan out:

```go
case *query.FieldEqExpr:
	srcs := reg.prefixed(e.Field)
	if len(srcs) == 0 {
		return bleve.NewMatchNoneQuery()
	}
	if len(srcs) == 1 {
		q := bleve.NewTermQuery(strings.ToLower(e.Value))
		q.SetField(srcs[0] + "." + e.Field)
		return q
	}
	qs := make([]bleveQuery.Query, len(srcs))
	for i, s := range srcs {
		q := bleve.NewTermQuery(strings.ToLower(e.Value))
		q.SetField(s + "." + e.Field)
		qs[i] = q
	}
	return bleve.NewDisjunctionQuery(qs...)
```

`source:<plugin>` remains as today (top-level keyword field, not per-source-prefixed).

- [ ] **Step 3: Run + commit**

```bash
git add internal/search/bleve_query.go internal/search/bleve_query_test.go internal/search/index.go
git commit -m "search: cross-source query routing via schema registry"
```

---

## Task 5: Feed plugin registers a `query.Schema` + EnrichSchema

**Files:**
- Modify: `plugin/feed/feed.go` (`Register` block — replace `EnrichSQL` registration with `Schema + Enricher`).
- Modify: `plugin/feed/store.go` (new method `EnrichSchema(ids []int64) ([]Document, error)` returning Documents whose Fields include `title`, `author`, `content`, `url`, `type`, `kind`, `subscription`, `folder`, `tag`, `is`, `published`).

```go
ctx.RegisterSearchSource(plugin.SearchSourceDef{
	PrimaryTable: "items",
	Schema: &query.Schema{
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
			"tag":          {Type: query.ManyToMany, Join: "item_tags", JoinFK: "item_id", ValueTable: "tags", ValueFK: "tag_id", ValueColumn: "name"},
			"is": {Type: query.EnumPredicate, Values: map[string]string{
				"unread":     "i.is_read = 0",
				"starred":    "i.is_starred = 1",
				"downloaded": "EXISTS (SELECT 1 FROM downloads d WHERE d.item_id = i.id AND d.state = 'ready')",
			}},
			"published": {Type: query.DateText, Expr: "i.published_at"},
		},
		Joins: map[string]string{
			"folders": "LEFT JOIN folders f ON f.id = s.folder_id",
		},
	},
	Enricher: enricherFor(p), // returns SchemaEnricher backed by per-user Store
})
```

`enricherFor(p)` returns an adapter that, given `(db, rowIDs)`, loads the items by ID via the user's `Store` and produces Documents. Field values:

- `title`/`author`/`content`/`url`/`type`/`kind`/`subscription`/`folder`: direct columns / joined values.
- `tag`: `Store.ItemTags(id)` (returns `[]string`).
- `is`: build `[]string` containing `"unread"` when `!IsRead`, `"starred"` when `IsStarred`, `"downloaded"` when `dl.State == "ready"`.
- `published`: `time.Unix(it.PublishedAt, 0).UTC().Format(time.RFC3339)`.

Steps:
- [ ] Write tests exercising the new Enricher (white-box `package feed` ok) — feed an item ID and assert fields.
- [ ] Implement `EnrichSchema` on the store / plugin.
- [ ] Switch `RegisterSearchSource` in `feed.go`.
- [ ] Commit.

```bash
git commit -m "feed: register Schema and EnrichSchema for global search"
```

---

## Task 6: Integration — confirm `is:downloaded` works end-to-end

**Files:** test only.

- [ ] Cogtest spinning up the env, seeding a feed item with a `downloads` row state='ready', triggering an index sync, then searching `is:downloaded` and asserting the item appears.
- [ ] Negative test: an item without a downloads row does not appear.
- [ ] Commit.

```bash
git commit -m "search: end-to-end test for is:downloaded via feed plugin"
```

---

## Final: full verify

- [ ] `go tool verify` — expect "All checks passed."

## Self-review notes (coverage map)

- Schema type → Task 1.
- bleve mapping consumes Schema → Task 2.
- Plugin def carries Schema + Enricher → Task 3.
- Query routes across sources → Task 4.
- Feed plugin populates Schema → Task 5.
- End-to-end works → Task 6.

## Out of scope (handled in Plan B)

- The SQL compiler that consumes `query.Schema`.
- Migration of feed's `Index`/`PlaylistView`/`resolveViewItems` to compile-and-push instead of `applyQueryFilter`.
- Other Go→SQL pushdowns (sort subs, bulk delete, history join, sidebar counts).
