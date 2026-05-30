# Full Search Parity: SQL-Prefilter + Go-Exact-Refine (bleve unchanged)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. `- [ ]` checkboxes.

**Goal:** Feed search (SQL + Go) returns the *same rows* as global search (bleve), for every query. **Bleve is left exactly as-is** — feed conforms to it. SQL stays a permissive *superset*; the feed runs a bleve-exact Go matcher as a final filter, but only when the query contains a bare-word/phrase term (the only nodes SQL evaluates loosely). All other predicates (`:field=`, `IN`, ranges, `is`, `tag`) SQL already evaluates exactly, so they skip the refine pass.

**Parity mechanism:** the Go-refine matcher tokenizes by **borrowing bleve's own standard analyzer** (UAX#29 segmentation + lowercase + English stop-words). No hand-rolled tokenizer, no bleve change → guaranteed agreement.

## Decisions

1. **Bleve unchanged.** No analyzer change, no stop-word change, no reindex. Global search behaves exactly as today.
2. **Bare-word scope = Type=Text fields only** (title/author/content). This **reverts Plan-B P2(a)** (which broadened SQL/feed bare-word to all scalar columns). It conforms *feed* to bleve's `_all` (text-only) — it does **not** change bleve. After: `audio` no longer matches via `type=audio`; use `:type=audio`.
3. **`query.Eval(e, r, schema, tokenize)`** — new pure, schema-aware matcher with an **injected** tokenizer (keeps `internal/query` free of any bleve dependency). Text term = doc has a token equal to the analyzed term **or** a token with the raw-lowercased term as prefix (mirrors bleve's match+prefix disjunction). Phrase = consecutive analyzed-token subsequence (mirrors bleve `MatchPhrase`). Non-text nodes delegate to existing `(Expr).Match`.
4. **`internal/search` owns the bleve-backed tokenizer**: `StandardTokenize(string) []string` (runs the same standard analyzer the index uses) and `ExactMatch(e, r, schema) bool = query.Eval(e, r, schema, StandardTokenize)`. Feed (in-process privileged plugin) imports `internal/search` for the refine. No import cycle (feed → search → plugin; search does not import feed).
5. **SQL superset**: phrase → `AND` of per-word `%word%` over text fields (superset of adjacency); bare word → OR of `%term%` over text fields (superset of token/prefix). Field/range/`is`/`tag` unchanged (already exact).
6. **Refine gate**: feed refines only when the parsed expr contains a `TextExpr`/`PhraseExpr`. Otherwise SQL result is exact — no Record rebuild, no extra queries.

**Tech Stack:** Go, SQLite, bleve v2.5.7, `internal/query`, `internal/search`, `plugin/feed`.

## Conventions
- TDD each task. WE DO NOT SWALLOW ERRORS. Commit per task. `go build`/`go vet`/`go test` per task; `go tool verify` at end. IGNORE stale LSP feed* arg-count diagnostics.

---

## Task 1: `query.Eval` — schema-aware, injected tokenizer

**Files:** `internal/query/eval.go` (new), `internal/query/eval_test.go` (new). `internal/query` gains NO new imports beyond stdlib.

```go
package query

import "strings"

// Tokenizer splits a string into normalized tokens. Injected so internal/query
// stays free of any search-engine dependency; internal/search supplies one
// backed by bleve's analyzer for exact parity.
type Tokenizer func(string) []string

// Eval reports whether e matches record r under schema s, using the given
// tokenizer for text/phrase matching. Semantics mirror the bleve query built
// in internal/search: a bare word matches a Type=Text field whose tokens
// contain the analyzed term OR a token prefixed by the raw-lowercased term;
// a phrase matches a consecutive analyzed-token subsequence of a Type=Text
// field. All non-text nodes evaluate identically to (Expr).Match.
func Eval(e Expr, r Record, s Schema, tok Tokenizer) bool {
	switch x := e.(type) {
	case *TextExpr:
		return matchTerm(x.Value, r, s, tok)
	case *PhraseExpr:
		return matchPhrase(x.Value, r, s, tok)
	case *NotExpr:
		return !Eval(x.Inner, r, s, tok)
	case *AndExpr:
		return Eval(x.Left, r, s, tok) && Eval(x.Right, r, s, tok)
	case *OrExpr:
		return Eval(x.Left, r, s, tok) || Eval(x.Right, r, s, tok)
	case *MatchAllExpr:
		return true
	default: // FieldEqExpr, FieldInExpr, FieldCompareExpr
		return e.Match(r)
	}
}

func matchTerm(term string, r Record, s Schema, tok Tokenizer) bool {
	terms := tok(term)
	prefix := strings.ToLower(term)
	for _, field := range s.DefaultTextFields() {
		for _, v := range r[field] {
			toks := tok(v)
			// match: any analyzed term token present
			for _, qt := range terms {
				for _, dt := range toks {
					if dt == qt {
						return true
					}
				}
			}
			// prefix: any doc token starts with the raw lowercased term
			for _, dt := range toks {
				if strings.HasPrefix(dt, prefix) {
					return true
				}
			}
		}
	}
	return false
}

func matchPhrase(phrase string, r Record, s Schema, tok Tokenizer) bool {
	want := tok(phrase)
	if len(want) == 0 {
		return false
	}
	for _, field := range s.DefaultTextFields() {
		for _, v := range r[field] {
			if containsSubsequence(tok(v), want) {
				return true
			}
		}
	}
	return false
}

func containsSubsequence(have, want []string) bool {
	if len(want) == 0 {
		return true
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

### Tests (`internal/query/eval_test.go`)
Use a simple test tokenizer `func(s string) []string { return strings.Fields(strings.ToLower(s)) }` (whitespace+lower; the real bleve one is tested in Task 3). Schema: `title`/`author`/`content` Text, `type` Keyword, `is` EnumPredicate.

- bare word token-exact: `cat` matches title "the cat".
- bare word prefix: `cat` matches title "Category list" (token "category" prefixed by "cat").
- text-only scope: bare word `audio` does NOT match when only `type`(Keyword)=audio (Eval ignores non-text fields for bare words).
- phrase adjacency: `"hello world"` matches "say hello world"; NOT "world hello"; NOT "hello big world".
- compound: `:type=audio "hello world"` (And) — true only when both hold; `:type=audio OR "x"` delegates correctly.
- NOT: `NOT "x"` negates.

Commit: `git commit -m "query: schema-aware Eval with injected tokenizer (text token+prefix, phrase adjacency)"`

---

## Task 2: SQL superset — phrase all-words, bare-word text-fields (revert P2a)

**Files:** `internal/query/sql.go`, `internal/query/schema.go`, `internal/query/sql_test.go`.

- Route `*TextExpr` → `likeTerm` (single term, OR `%term%` over `DefaultTextFields`). Route `*PhraseExpr` → `likeAllWords` (split on whitespace; per word OR `%word%` over text fields; AND the words). Both via a shared `orLikeAcrossText(value)` helper that uses `DefaultTextFields()`. Delete the old `likeAcrossText` (which used `ScalarFields`).
- Remove `Schema.ScalarFields()` (added in P2a) if no other caller — grep first.

(See the prior plan revision for the exact `orLikeAcrossText` / `likeAllWords` bodies; reuse them, keyed on `DefaultTextFields()`.)

### Tests
- `TestCompileBareWordTextFieldsOnly`: bare word emits `LOWER(i.title) LIKE ?` etc. but NOT `LOWER(i.type) LIKE` (type is Keyword) and pulls NO folders join.
- `TestCompilePhraseAllWordsSuperset`: `&PhraseExpr{Value:"hello world"}` → `AND` of two text-field OR-groups; not a single `%hello world%`.
- Update/remove the P2a tests `TestCompileBareWordScansKeywordFields` and `TestCompileBareWordPullsFolderJoin` (their premise is reverted).

Commit: `git commit -m "query: SQL bare-word back to text fields, phrase=all-words superset (revert P2a scan-all)"`

---

## Task 3: `internal/search` bleve-backed tokenizer + `ExactMatch`

**Files:** `internal/search/exactmatch.go` (new), `internal/search/exactmatch_test.go` (new).

Borrow the *same* analyzer the index uses. The index mapping's Text fields use bleve's default ("standard") analyzer. Build a standard analyzer once and tokenize through it.

```go
package search

import (
	"git.duckfam.us/jj-software/cog/internal/query"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	"github.com/blevesearch/bleve/v2/registry"
)

var standardAnalyzer = func() analysisAnalyzer { ... }()
// Obtain via registry.Cache: c := registry.NewCache(); a, _ := c.AnalyzerNamed(standard.Name)

// StandardTokenize runs bleve's standard analyzer (UAX#29 segmentation +
// lowercase + English stop-word removal) and returns the token terms in order.
// This is the SAME analysis the bleve index applies to Type=Text fields, so a
// Go match using these tokens agrees with global search exactly.
func StandardTokenize(s string) []string {
	stream := standardAnalyzer.Analyze([]byte(s))
	out := make([]string, len(stream))
	for i, t := range stream {
		out[i] = string(t.Term)
	}
	return out
}

// ExactMatch evaluates expr against rec with bleve-exact text semantics.
func ExactMatch(e query.Expr, rec query.Record, s query.Schema) bool {
	return query.Eval(e, rec, s, StandardTokenize)
}
```
VERIFY the exact bleve API to obtain a `standard` analyzer instance: likely
```go
cache := registry.NewCache()
a, err := cache.AnalyzerNamed(standard.Name) // standard.Name == "standard"
```
and `a.Analyze([]byte) analysis.TokenStream`, each token has `.Term []byte`. Adjust types/imports to the real signatures (`analysis.Analyzer`, `analysis.TokenStream`). Handle the constructor error at package init (panic is acceptable for a missing built-in analyzer, or lazy-init with sync.Once returning error-free since standard always registers).

### Tests (`internal/search/exactmatch_test.go`)
Assert tokenization matches what the bleve INDEX does:
- `StandardTokenize("Hello, World!")` == `["hello","world"]` (punctuation dropped).
- stop words removed: `StandardTokenize("the cat")` == `["cat"]`.
- digits kept: `StandardTokenize("rss2 feed")` contains `"rss2"`.
- `ExactMatch` end-to-end: phrase `"hello world"` true for a Record with content "say hello world", false for "world hello"; bare word `cat` true for "category" (prefix). Build the Record + a feed-like schema inline (Text fields title/content).
- **Ground-truth cross-check (optional but valuable):** index the same docs into an in-memory bleve index (`OpenMemory` + a schema), run the global search for the same query string, and assert the doc set equals the set `ExactMatch` accepts over the same docs. This is the real "feed == global" proof at the unit level.

Commit: `git commit -m "search: bleve-backed StandardTokenize + ExactMatch for parity refine"`

---

## Task 4: Feed two-stage — SQL prefilter + ExactMatch refine

**Files:** `plugin/feed/pages.go`, `plugin/feed/store.go` (batch loaders), tests.

### Refine gate
```go
// queryNeedsRefine reports whether e contains a TextExpr/PhraseExpr — the only
// nodes SQL evaluates as a superset. False => SQL result is already exact.
func queryNeedsRefine(e query.Expr) bool {
	switch x := e.(type) {
	case *query.TextExpr, *query.PhraseExpr:
		return true
	case *query.NotExpr:
		return queryNeedsRefine(x.Inner)
	case *query.AndExpr:
		return queryNeedsRefine(x.Left) || queryNeedsRefine(x.Right)
	case *query.OrExpr:
		return queryNeedsRefine(x.Left) || queryNeedsRefine(x.Right)
	default:
		return false
	}
}
```
(The AST nodes are exported from `internal/query`; confirm `TextExpr` etc. are exported — they are.)

### Batch loaders (`store.go`), one query each
- `TagsByItems(ids []int64) (map[int64][]string, error)`
- `DownloadStateByItems(ids []int64) (map[int64]string, error)`
- Folder name: if `ItemWithSubscription` already carries it, reuse; else `FolderNamesByItems(ids) (map[int64]string, error)` joining items→subscriptions→folders. (Check the struct first.)

### Refine in handlers
In `Index`, `PlaylistView`, and `resolveViewItems` (the sites that build a user/playlist `expr`): after `ListItemsForView` returns the superset, if `expr != nil && queryNeedsRefine(expr)`:
1. collect result IDs (preserve order),
2. batch-load tags + download-state (+ folder if needed),
3. for each row build a `query.Record` (reuse `BuildRecord` with the row's item/sub + batched tags/download/folder),
4. keep rows where `search.ExactMatch(expr, rec, *SearchSchema())`.
Else keep the SQL result unchanged.

Add `internal/search` import to pages.go.

### Tests
- e2e phrase: index items with content "hello world", "hello, world", "world hello"; `?q="hello world"` (URL-encoded) → first two present (`>title<`), third absent.
- e2e bare-word prefix vs infix: item "Category", item "scatter"; `?q=cat` → Category present, scatter absent (token/prefix, not substring).
- e2e refine-skip regression: `?q=:type=audio` returns correct rows; assert no behavioral change (and ideally that the path doesn't invoke the batch loaders — e.g. via a seam or by trusting `queryNeedsRefine` unit test).
- unit: `queryNeedsRefine` truth table (`x`→true, `"p"`→true, `:type=audio`→false, `:published>2026-01-01`→false, `is:unread`→false, `:type=audio "x"`→true).

Commit: `git commit -m "feed: SQL-prefilter + bleve-exact refine for text/phrase queries"`

---

## Task 5: Migrate ingest filter + FilterPreview; cleanup

**Files:** `plugin/feed/filter.go`, `plugin/feed/pages.go` (FilterPreview), tests.

- `filter.go` (`expr.Match(rec)`) and FilterPreview (`expr.Match(rec)`) → `search.ExactMatch(expr, rec, *SearchSchema())`, so ingest rules + live preview use the same bleve-exact semantics as search. Both already build a full Record via `BuildRecord` (no batching needed).
- Grep remaining `\.Match(` callers of `query.Expr`. If `(Expr).Match` is now unused outside Eval's `default` delegation, KEEP it (Eval relies on it for non-text nodes) — it is NOT dead. Do not delete.

### Final
- `go tool verify` → all pass.
- Manual: global search and feed search return identical rows for `cat`, `"hello world"`, `:type=audio`, `is:unread`, `:published>-7d`, and a compound `audio "deep dive"` (note: `audio` bare word now hits text fields only).

Commit any verify fixes.

## Self-review map
- Go Eval (injected tokenizer) → T1
- SQL superset + P2a revert → T2
- bleve-backed tokenizer + ExactMatch → T3
- feed two-stage refine + gate + batch loaders → T4
- ingest/preview migration → T5

## Open verification points
- Exact bleve API for obtaining the `standard` analyzer + `Analyze`/`TokenStream`/`Term` types (T3) — grep module cache `github.com/blevesearch/bleve/v2@v2.5.7/analysis/` and `registry/`.
- `ScalarFields` removal: confirm no caller outside sql.go (T2).
- Whether `ItemWithSubscription` carries folder name; else add batch folder loader (T4).
- Confirm `query.TextExpr`/`PhraseExpr`/`AndExpr`/`OrExpr`/`NotExpr` are exported for `queryNeedsRefine` (T4) — they are.
- bleve's standard analyzer DOES remove English stop words — confirm via the T3 test (`"the cat"`→`["cat"]`); this is the behavior feed must mirror, and it now does because both global search and the refine use the same analyzer.
