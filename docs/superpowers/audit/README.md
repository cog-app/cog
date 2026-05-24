# Cog Codebase Audit

Audit performed on `master` (commit `0c018ef`), 2026-05-23.

Reports live in this directory:

- [`security.md`](security.md) — auth, sessions, CSRF, federation, WebDAV, sharing, crypto.
- [`plugin-system.md`](plugin-system.md) — privilege boundary, gRPC bridge, lifecycle, registry, design-doc drift.
- [`bugs-quality.md`](bugs-quality.md) — concurrency, error handling, resource leaks, SQL, HTTP semantics.
- [`test-coverage.md`](test-coverage.md) — package coverage numbers and missing-coverage targets.
- [`top-findings.md`](top-findings.md) — cross-cutting high-impact summary.

Findings are claims to verify, not adjudicated truth. Treat each as a starting point.

## How findings were produced

Four parallel agents each read code under their assigned area and reported `file:line` citations
for concrete issues. No automated tools were run beyond `go test -cover` for the coverage report.

## Scope

In-scope: everything under `internal/`, `plugin/`, `vfs/`, `ui/`, `cog*.go`.
Out-of-scope: `DOCS/`, `DESIGN/` (used as reference only), generated `*_templ.go`, `*.proto`-derived
files (except `plugin.proto` itself).
