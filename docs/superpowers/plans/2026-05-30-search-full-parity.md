# Full Search Parity: Shared Tokenizer + SQL-Prefilter/Go-Exact-Refine

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. `- [ ]` checkboxes.

**Goal:** Make the three search evaluators agree exactly. Define ONE tokenizer used by the Go matcher and mirrored by a custom bleve analyzer. SQL stays a permissive *superset* (substring/all-words); the feed search runs the exact Go matcher as a final filter over SQL results — but only when the query contains a bare-word/phrase term (the sole permissive nodes). Everything else SQL evaluates exactly, so most queries skip the refine pass entirely.

## Decisions (REVIEW THESE — they change existing behavior)

1. **Bare-word scope = Type=Text fields only** (`title`, `author`, `content`) across all three engines. This **reverts Plan-B P2(a)** (SQL `ScalarFields` scan of all columns) back to `DefaultTextFields`. Rationale: bleve's `_all` is text-only; that is the parity target. Searching `audio` will no longer match via `type=audio` — use `:type=audio` for that.
2. **Shared tokenizer** (`internal/query/tokenize.go`): lowercase, split on every rune that is not a Unicode letter or digit, **no stop-word removal**. Deterministic, dependency-free.
3. **bleve Text fields use a custom analyzer** equivalent to the shared tokenizer (unicode/letter tokenizer + lowercase, **no** stop filter) so bleve and Go agree by construction. Requires an index rebuild (dev: delete `.search` dirs / FullSync recreates).
4. **Keep prefix matching** for bare words: a field matches term T if any token == T **or** any token has prefix T. (Matches today's bleve match+prefix UX.) SQL `%T%` substring is a superset of token-or-prefix.
5. **Phrase** = consecutive token subsequence (Go + bleve `MatchPhrase`). SQL phrase → `AND` of per-word `%word%` (superset of adjacency).
6. **Schema-aware evaluation**: new `query.Eval(e Expr, r Record, s Schema) bool`. `TextExpr`/`PhraseExpr` consult `s.DefaultTextFields()` + the shared tokenizer; all other nodes evaluate as today. The old `Expr.Match(Record)` stays (back-compat for callers that want the loose all-fields scan) but feed migrates to `Eval`.
7. **Feed two-stage**: SQL prefilter (superset) → if the parsed expr contains any `TextExpr`/`PhraseExpr`, batch-load tags/folders/download-state for the result-page IDs, build full Records, keep rows where `query.Eval(expr, rec, schema)` is true. No text/phrase node → no refine (SQL was exact).

**Tech Stack:** Go, SQLite, bleve v2.5.7, `internal/query`, `internal/search`, `plugin/feed`.

## Conventions
- TDD each task. WE DO NOT SWALLOW ERRORS. Commit per task. `go build`/`go vet`/`go test` per task; `go tool verify` at end. IGNORE stale LSP feed* arg-count diagnostics.

---

## Task 1: Shared tokenizer + bleve-exact Go `Eval`

**Files:** `internal/query/tokenize.go` (new), `internal/query/eval.go` (new), tests.

### Tokenizer
```go
package query

import (
	"strings"
	"unicode"
)

// Tokenize lowercases s and splits on every rune that is not a Unicode letter
// or digit, returning the non-empty tokens. This is the canonical tokenizer
// shared by the Go matcher (Eval) and mirrored by the bleve analyzer, so all
// search engines agree on word boundaries. No stop-word removal.
func Tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}
```

### Eval (schema-aware, bleve-exact text/phrase)
```go
// Eval reports whether e matches record r under schema s, using bleve-exact
// semantics: bare words match a token (or token-prefix) in any Type=Text
// field; phrases match a consecutive token subsequence in a Type=Text field.
// Non-text nodes (field equality, ranges, IN, is/tag membership) evaluate
// identically to (Expr).Match.
func Eval(e Expr, r Record, s Schema) bool {
	switch x := e.(type) {
	case *TextExpr:
		return matchTermInText(x.Value, r, s)
	case *PhraseExpr:
		return matchPhraseInText(x.Value, r, s)
	case *NotExpr:
		return !Eval(x.Inner, r, s)
	case *AndExpr:
		return Eval(x.Left, r, s) && Eval(x.Right, r, s)
	case *OrExpr:
		return Eval(x.Left, r, s) || Eval(x.Right, r, s)
	case *MatchAllExpr:
		return true
	default:
		// FieldEqExpr, FieldInExpr, FieldCompareExpr: identical to Match.
		return e.Match(r)
	}
}

// matchTermInText: term matches if any token in any Type=Text field equals it
// or has it as a prefix (bleve match + prefix). Term is tokenized too; a
// multi-token term (shouldn't happen — lexer splits on space) requires all its
// tokens present as a contiguous run, i.e. delegate to phrase.
func matchTermInText(term string, r Record, s Schema) bool {
	toks := Tokenize(term)
	if len(toks) == 0 {
		return false
	}
	if len(toks) > 1 {
		return matchPhraseTokens(toks, r, s)
	}
	t := toks[0]
	for _, field := range s.DefaultTextFields() {
		for _, v := range r[field] {
			for _, tok := range Tokenize(v) {
				if tok == t || strings.HasPrefix(tok, t) {
					return true
				}
			}
		}
	}
	return false
}

func matchPhraseInText(phrase string, r Record, s Schema) bool {
	toks := Tokenize(phrase)
	if len(toks) == 0 {
		return false
	}
	return matchPhraseTokens(toks, r, s)
}

// matchPhraseTokens: want is a consecutive subsequence of some Type=Text
// field's tokens. Phrase tokens match exactly (no prefix), matching bleve
// MatchPhrase.
func matchPhraseTokens(want []string, r Record, s Schema) bool {
	for _, field := range s.DefaultTextFields() {
		for _, v := range r[field] {
			have := Tokenize(v)
			if containsSubsequence(have, want) {
				return true
			}
		}
	}
	return false
}

func containsSubsequence(have, want []string) bool {
	if len(want) == 0 || len(have) < len(want) {
		return len(want) == 0
	}
	for i := 0; i+len(want) <= len(have); i++ {
		ok := true
		for j := range want {
			if have[i+j] != want[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
```
Add `strings` import to eval.go.

### Tests (`internal/query/eval_test.go`)
Cover: bare word token-exact, prefix (`cat`→`category`), no-infix (`atter` does NOT match `scatter` since `atter` is not a token nor a prefix — proves token semantics, not substring), phrase adjacency (`"hello world"` matches `"say hello world"`, matches `"hello, world"` (punctuation tokenized away), does NOT match `"world hello"` or `"hello big world"`), text-field-only scope (a bare word equal to a `type` keyword value does NOT match via Eval), and compound `tag:x OR "phrase"` delegating correctly. Tokenizer unit tests for punctuation/unicode/empty.

Commit: `git commit -m "query: shared tokenizer + bleve-exact schema-aware Eval"`

---

## Task 2: SQL phrase → all-words superset; revert bare-word to text fields

**Files:** `internal/query/sql.go`, `internal/query/schema.go` (drop or keep `ScalarFields`), tests.

- `likeAcrossText` (used for both TextExpr and PhraseExpr currently): change to use `DefaultTextFields()` (revert P2a's `ScalarFields`). For a single-word value → OR of `%word%` across text fields (superset of token+prefix). For a **phrase** value containing spaces → split into words and emit, per word, an OR-across-text-fields, then AND the words together (superset of adjacency). Implement by routing `PhraseExpr` to a new `likeAllWords` and `TextExpr` to the existing single-term `likeAcrossText`.
- Remove `Schema.ScalarFields()` if now unused (grep first; it was added in Plan B P2a and only sql.go used it).

Concretely in `sql.go`:
```go
case *TextExpr:
	return c.likeTerm(x.Value)      // single word, OR across text fields
case *PhraseExpr:
	return c.likeAllWords(x.Value)  // AND of per-word OR-across-text-fields
```
```go
func (c *sqlCompiler) likeTerm(value string) (string, error) {
	return c.orLikeAcrossText(value)
}

func (c *sqlCompiler) likeAllWords(phrase string) (string, error) {
	words := strings.Fields(phrase)
	if len(words) == 0 {
		return "0", nil
	}
	parts := make([]string, 0, len(words))
	for _, w := range words {
		frag, err := c.orLikeAcrossText(w)
		if err != nil {
			return "", err
		}
		parts = append(parts, frag)
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return "(" + strings.Join(parts, " AND ") + ")", nil
}

// orLikeAcrossText emits `(LOWER(f1) LIKE ? OR LOWER(f2) LIKE ? ...)` over the
// schema's Type=Text fields, binding "%lower(value)%" per field.
func (c *sqlCompiler) orLikeAcrossText(value string) (string, error) {
	names := c.schema.DefaultTextFields()
	if len(names) == 0 {
		return "0", nil
	}
	like := "%" + strings.ToLower(value) + "%"
	parts := make([]string, 0, len(names))
	for _, name := range names {
		f := c.schema.Fields[name]
		c.requireJoins(f.Joins)
		parts = append(parts, fmt.Sprintf("LOWER(%s) LIKE ?", f.Expr))
		c.args = append(c.args, like)
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", nil
}
```
Delete the old `likeAcrossText`.

### Tests (`internal/query/sql_test.go`)
- Bare word now scans only text fields (title/author/content) — NOT `type` (Keyword). Update `TestCompileBareWordScansKeywordFields` (from P2a) to assert the OPPOSITE: `LOWER(i.type) LIKE` is **absent**; title/author present. Rename to `TestCompileBareWordTextFieldsOnly`.
- `TestCompileBareWordPullsFolderJoin` (P2a): folder is Keyword — no longer in bare-word scan → it should NOT pull the folders join anymore. Update/remove.
- New `TestCompilePhraseAllWordsSuperset`: `&PhraseExpr{Value:"hello world"}` → contains `AND` and two sets of LIKEs; not a single `%hello world%`.

Commit: `git commit -m "query: SQL phrase=all-words superset, bare-word back to text fields"`

---

## Task 3: bleve custom analyzer matching the shared tokenizer

**Files:** `internal/search/schema_mapping.go`, tests; register a custom analyzer on the index mapping.

Goal: Text fields tokenize like `query.Tokenize` (unicode letter/digit boundaries, lowercase, no stop words). bleve ships a "letter" tokenizer (splits on non-letter) — but we need letter+digit. Use bleve's `unicode` tokenizer + `to_lower` filter with NO stop filter, OR register a custom analyzer. The `web`/`unicode` tokenizer segments on Unicode boundaries (keeps alphanumerics together) which is the closest match.

Implementation: in `buildSchemaMapping`, register a custom analyzer (e.g. name `"cog_simple"`) on the `*mapping.IndexMappingImpl`:
```go
m := bleve.NewIndexMapping()
err := m.AddCustomAnalyzer("cog_simple", map[string]any{
	"type":      custom.Name,           // "custom"
	"tokenizer": unicode.Name,          // "unicode"
	"token_filters": []string{lowercase.Name}, // "to_lower"
})
```
(import the bleve registry packages: `analysis/analyzer/custom`, `analysis/tokenizer/unicode`, `analysis/token/lowercase`. Verify exact package paths in the module cache.) Then for Text field mappings set `fm.Analyzer = "cog_simple"`. Keyword fields keep keyword mapping (exact, lowercased at index by the enricher per Plan-A P2b).

Set `m.DefaultAnalyzer = "cog_simple"` too so query-time analysis matches.

### Behavior to verify (`internal/search` test, real in-memory index)
Build an index with one text field, index docs, search:
- `cat` matches doc "Category list" (prefix) and "the cat" (token) — confirm "the" stop word now ALSO indexed (search `the` matches "the cat"), proving stop words are no longer dropped.
- phrase `"hello world"` matches "say hello world" and "hello, world"; not "world hello".
This is the ground-truth that bleve now equals `query.Eval`.

Commit: `git commit -m "search: custom cog_simple analyzer (unicode+lowercase, no stopwords) for text fields"`

---

## Task 4: Feed two-stage — SQL prefilter + Go exact refine

**Files:** `plugin/feed/pages.go`, `plugin/feed/store.go` (batch loaders), tests.

### Helper: does an expr need refining?
```go
// queryNeedsRefine reports whether e contains a TextExpr/PhraseExpr — the only
// nodes SQL evaluates permissively. If false, the SQL result is already exact.
func queryNeedsRefine(e query.Expr) bool { ... recursive walk ... }
```
(Put in pages.go or a small feed helper; walk And/Or/Not children.)

### Batch Record build for a result page
Add store methods to fetch, for a set of item IDs in ONE query each:
- `TagsByItems(ids []int64) (map[int64][]string, error)`
- `DownloadStateByItems(ids []int64) (map[int64]string, error)`
- folder name is already obtainable via the subscription join already in `ItemWithSubscription`? If not, `FolderNameByItems` or reuse subscription→folder. (Check what `ItemWithSubscription` carries; folder may need a small join.)

Then a feed helper builds `query.Record` per row from the row columns + the batched maps, reusing the existing `BuildRecord` shape.

### Wire into the handlers
In `Index`, `PlaylistView`, and `resolveViewItems` (the sites that parse a user/playlist query): after `ListItemsForView` returns the SQL-prefiltered superset, if `expr != nil && queryNeedsRefine(expr)`:
1. collect result IDs,
2. batch-load tags + download-state,
3. for each row build a Record and keep it only if `query.Eval(expr, rec, *SearchSchema())`.
Preserve order. When no refine needed, use the SQL result as-is.

NOTE the limit interaction: SQL applies `LIMIT feedViewLimit` to the *superset*. Refining shrinks it. Acceptable (same as before for hide-read). Document that a refined search may return < limit even when more matches exist beyond the window — pre-existing behavior.

### Tests (`plugin/feed/...`)
- e2e: index two items "hello world" and "hello, world" and "world hello"; query `?q="hello world"` (URL-encoded) returns the first two, not the third. (Proves phrase adjacency end-to-end via SQL superset + Go refine.)
- e2e: bare word prefix — item "Category"; query `?q=cat` returns it; item "scatter" only does NOT (proves token/prefix, not substring, after refine).
- unit: `queryNeedsRefine` true for `"x"`/bare word, false for `:type=audio`, `:published>2026-01-01`, `is:unread`.
- regression: `:type=audio` query path returns correct rows AND skips refine (no batch calls) — assert via behavior.

Commit: `git commit -m "feed: SQL-prefilter + Go-exact refine for text/phrase queries"`

---

## Task 5: Migrate ingest filter + FilterPreview to Eval; final verify

**Files:** `plugin/feed/filter.go`, `plugin/feed/pages.go` (FilterPreview), tests.

`filter.go:66` and `pages.go:1135` call `expr.Match(rec)`. For consistency with feed search, switch them to `query.Eval(expr, rec, *feed.SearchSchema())` so ingest filter rules and the live preview use the same bleve-exact semantics. (These already build a full Record via `BuildRecord`, so no batching needed.)

Confirm `Expr.Match` is still used somewhere or remove it: grep. If nothing uses the loose `Match` after migration, consider deleting it + its tests to avoid two divergent matchers. DECISION POINT: keep `Match` only if a caller needs the loose all-fields scan; otherwise delete for a single source of truth. Report which.

### Final
- `go tool verify` → all checks pass.
- Manual: global search and feed search return identical results for: `cat`, `"hello world"`, `:type=audio`, `is:unread`, `:published>-7d`.

Commit any verify fixes.

## Self-review map
- shared tokenizer + Go Eval → T1
- SQL superset (phrase all-words, bare-word text-only) → T2
- bleve analyzer parity → T3
- feed two-stage refine → T4
- ingest/preview migration + cleanup → T5

## Open verification points
- Exact bleve package paths for custom analyzer registration (`custom`, `unicode` tokenizer, `lowercase` filter) — grep module cache; confirm `unicode` tokenizer keeps digits with letters (it segments on Unicode word boundaries — verify `abc123` stays one token; if it splits, pick a tokenizer/regex that matches `query.Tokenize`, or switch `query.Tokenize` to match the chosen bleve tokenizer so the two AGREE — agreement matters more than the exact rule).
- Whether reverting `ScalarFields` breaks other callers (grep).
- Whether `ItemWithSubscription` carries folder name for Record build, else add a batch folder loader.
- Decide keep/delete `Expr.Match` after T5.
