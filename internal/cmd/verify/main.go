// Verify tool for Cog. Usage: go tool verify [-cover-only] [-continue] [-snapshots]
//
// Runs code quality checks in sequence: generate, fmt, fix, vet, and tests
// with cross-package coverage. Reports uncovered handler functions at the end.
// Generates an HTML snapshot gallery at tmp/snapshots.html.
//
// Use -cover-only to skip generate/fmt/vet and jump straight to tests.
// Use -continue to keep going after a step fails.
// Use -snapshots to only regenerate the snapshot gallery without running tests.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	coverOnly := flag.Bool("cover-only", false, "skip generate/fmt/vet, run tests with coverage only")
	cont := flag.Bool("continue", false, "continue on failure instead of stopping")
	snapshotsOnly := flag.Bool("snapshots", false, "only regenerate the snapshot gallery")
	flag.Parse()

	if err := os.MkdirAll("tmp", 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir tmp: %v\n", err)
		os.Exit(1)
	}

	if *snapshotsOnly {
		generateGallery()
		return
	}

	coverProfile := filepath.Join("tmp", "coverage.out")

	failed := false
	run := func(name string, args ...string) bool {
		fmt.Printf("\n\033[1;34m▶ %s\033[0m\n", name)
		start := time.Now()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		elapsed := time.Since(start).Round(time.Millisecond)
		if err != nil {
			fmt.Printf("\033[1;31m✗ %s failed (%s)\033[0m\n", name, elapsed)
			failed = true
			return false
		}
		fmt.Printf("\033[1;32m✓ %s (%s)\033[0m\n", name, elapsed)
		return true
	}

	if !*coverOnly {
		if ok := run("go generate", "go", "generate", "./..."); !ok && !*cont {
			os.Exit(1)
		}
		if ok := run("go fmt", "go", "fmt", "./..."); !ok && !*cont {
			os.Exit(1)
		}
		if ok := run("templ fmt", "go", "tool", "templ", "fmt", "."); !ok && !*cont {
			os.Exit(1)
		}
		if ok := run("go fix", "go", "fix", "./..."); !ok && !*cont {
			os.Exit(1)
		}
		if ok := run("go vet", "go", "vet", "./..."); !ok && !*cont {
			os.Exit(1)
		}
	}

	if ok := run("go test (cross-package coverage)",
		"go", "test",
		"-coverpkg=./...",
		"-coverprofile="+coverProfile,
		"-count=1",
		"-timeout=30m", // browser tests queue for Chrome slots (webtest)
		"./...",
	); !ok && !*cont {
		os.Exit(1)
	}

	if _, err := os.Stat(coverProfile); err == nil {
		reportCoverage(coverProfile)
	}

	generateGallery()

	if failed {
		fmt.Println("\n\033[1;31mVerification failed.\033[0m")
		os.Exit(1)
	}
	fmt.Println("\n\033[1;32mAll checks passed.\033[0m")
}

// snapshot pairs a desktop screenshot with its optional mobile variant.
type snapshot struct {
	Name    string // test name derived from filename
	Desktop string // relative path to desktop PNG
	Mobile  string // relative path to mobile PNG (empty if none)
}

// generateGallery walks the repo for testdata/snapshots/ dirs and writes tmp/snapshots.html.
func generateGallery() {
	fmt.Printf("\n\033[1;34m▶ Snapshot gallery\033[0m\n")

	// pkg path → snapshots
	groups := map[string][]snapshot{}
	var pkgOrder []string

	walkErr := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Report and carry on: one unreadable directory should not
			// cost the rest of the gallery.
			fmt.Fprintf(os.Stderr, "  walk %s: %v\n", path, err)
			return nil
		}
		// Skip hidden dirs and tmp (but not the root ".").
		base := filepath.Base(path)
		if info.IsDir() && path != "." && (strings.HasPrefix(base, ".") || base == "tmp" || base == "vendor" || base == "node_modules") {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			return nil
		}
		if filepath.Base(path) != "snapshots" {
			return nil
		}
		// Expect parent to be testdata.
		if filepath.Base(filepath.Dir(path)) != "testdata" {
			return nil
		}

		// Package path is two levels up from testdata/snapshots.
		pkg := filepath.Dir(filepath.Dir(path))

		entries, err := os.ReadDir(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  read %s: %v\n", path, err)
			return nil
		}

		// Collect all .png files, index mobile variants.
		mobileSet := map[string]bool{}
		var desktopFiles []string
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".png") {
				continue
			}
			// Skip failure artifacts.
			if strings.Contains(name, ".actual.") || strings.Contains(name, ".diff.") {
				continue
			}
			if strings.HasSuffix(name, "_mobile.png") {
				mobileSet[name] = true
			} else {
				desktopFiles = append(desktopFiles, name)
			}
		}

		sort.Strings(desktopFiles)
		var snaps []snapshot
		for _, f := range desktopFiles {
			testName := strings.TrimSuffix(f, ".png")
			mobileFile := testName + "_mobile.png"
			s := snapshot{
				Name:    testName,
				Desktop: filepath.Join(path, f),
			}
			if mobileSet[mobileFile] {
				s.Mobile = filepath.Join(path, mobileFile)
			}
			snaps = append(snaps, s)
		}

		if len(snaps) > 0 {
			groups[pkg] = snaps
			pkgOrder = append(pkgOrder, pkg)
		}
		return nil
	})
	if walkErr != nil {
		fmt.Fprintf(os.Stderr, "  walk: %v\n", walkErr)
	}

	sort.Strings(pkgOrder)

	totalSnaps := 0
	for _, snaps := range groups {
		totalSnaps += len(snaps)
	}

	outPath := filepath.Join("tmp", "snapshots.html")
	file, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  create %s: %v\n", outPath, err)
		return
	}
	// Write through a bufio.Writer: its error is sticky, so the Flush at the
	// end reports any write that failed along the way.
	f := bufio.NewWriter(file)

	// Write HTML.
	fmt.Fprintf(f, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Cog — Snapshot Gallery</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,-apple-system,sans-serif;background:#f5f5f5;color:#222;padding:2rem}
h1{margin-bottom:0.25rem}
.summary{color:#666;margin-bottom:2rem;font-size:0.9rem}
details{margin-bottom:1.5rem;background:#fff;border-radius:8px;box-shadow:0 1px 3px rgba(0,0,0,0.1)}
summary{cursor:pointer;padding:1rem 1.25rem;font-size:1.1rem;font-weight:600;user-select:none;list-style:none}
summary::-webkit-details-marker{display:none}
summary::before{content:'▸ ';color:#999}
details[open]>summary::before{content:'▾ '}
summary .count{color:#999;font-weight:400;font-size:0.85rem;margin-left:0.5rem}
.grid{display:grid;gap:1.5rem;padding:1rem 1.25rem;grid-template-columns:repeat(auto-fill,minmax(500px,1fr))}
.snap{border:1px solid #e0e0e0;border-radius:6px;overflow:hidden}
.snap-title{padding:0.5rem 0.75rem;background:#fafafa;border-bottom:1px solid #e0e0e0;font-size:0.8rem;font-family:monospace;color:#555}
.snap-pair{display:flex;gap:0}
.snap-pair img{display:block}
.snap-desktop{flex:1;min-width:0}
.snap-mobile{flex:0 0 auto;border-left:1px solid #e0e0e0}
.snap-desktop img,.snap-mobile img{width:100%%;height:auto}
.label{text-align:center;font-size:0.7rem;color:#999;padding:2px 0;background:#fafafa;border-top:1px solid #eee}
</style>
</head>
<body>
<h1>Snapshot Gallery</h1>
<p class="summary">%d snapshots across %d packages — generated %s</p>
`, totalSnaps, len(pkgOrder), time.Now().Format("2006-01-02 15:04:05"))

	for _, pkg := range pkgOrder {
		snaps := groups[pkg]
		fmt.Fprintf(f, `<details>
<summary>%s<span class="count">(%d)</span></summary>
<div class="grid">
`, html.EscapeString(pkg), len(snaps))

		for _, s := range snaps {
			// Paths relative to tmp/ where the HTML lives.
			desktopRel, _ := filepath.Rel("tmp", s.Desktop)
			fmt.Fprintf(f, `<div class="snap">
<div class="snap-title">%s</div>
<div class="snap-pair">
<div class="snap-desktop"><a href="%s"><img src="%s" loading="lazy" alt="%s"></a><div class="label">desktop</div></div>
`, html.EscapeString(s.Name), html.EscapeString(desktopRel), html.EscapeString(desktopRel), html.EscapeString(s.Name))

			if s.Mobile != "" {
				mobileRel, _ := filepath.Rel("tmp", s.Mobile)
				fmt.Fprintf(f, `<div class="snap-mobile"><a href="%s"><img src="%s" loading="lazy" alt="%s mobile"></a><div class="label">mobile</div></div>
`, html.EscapeString(mobileRel), html.EscapeString(mobileRel), html.EscapeString(s.Name))
			}

			fmt.Fprintf(f, `</div>
</div>
`)
		}

		fmt.Fprintf(f, `</div>
</details>
`)
	}

	fmt.Fprintf(f, `</body>
</html>
`)
	if err := errors.Join(f.Flush(), file.Close()); err != nil {
		fmt.Fprintf(os.Stderr, "  write %s: %v\n", outPath, err)
		return
	}

	fmt.Printf("  wrote %s (%d snapshots, %d packages)\n", outPath, totalSnaps, len(pkgOrder))
}

func reportCoverage(profile string) {
	fmt.Printf("\n\033[1;34m▶ Coverage report\033[0m\n")

	pkgCov, totalCovered, totalStmts, err := perPackageStatementCoverage(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  parse profile: %v\n", err)
	} else {
		printPerPackageTable(pkgCov)
		if totalStmts > 0 {
			pct := 100 * float64(totalCovered) / float64(totalStmts)
			fmt.Printf("\n  \033[1mtotal: %d/%d statements (%.1f%%)\033[0m\n",
				totalCovered, totalStmts, pct)
		}
	}

	// Also list uncovered handler-like functions from `go tool cover -func`.
	cmd := exec.Command("go", "tool", "cover", "-func="+profile)
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  cover -func failed: %v\n", err)
		return
	}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	var uncovered []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "total:") {
			continue
		}
		if !strings.HasSuffix(strings.TrimSpace(line), "0.0%") {
			continue
		}
		if strings.Contains(line, "_templ.go") ||
			strings.Contains(line, "plugcmd") ||
			strings.Contains(line, "plugpb") ||
			strings.Contains(line, "cmd/cog") ||
			strings.Contains(line, "cmd/dev") ||
			strings.Contains(line, "cmd/docs") ||
			strings.Contains(line, "cmd/verify") ||
			strings.Contains(line, "cmd/migratesql") ||
			strings.Contains(line, "testing/") ||
			strings.Contains(line, "tmp/dev") {
			continue
		}
		lower := strings.ToLower(line)
		if containsAny(lower, "handle", "serve", "page", "view",
			"create", "update", "delete", "list", "get",
			"import", "export", "search", "download", "upload",
			"toggle", "star", "render", "show", "browse",
			"dispatch", "respond", "settings") {
			uncovered = append(uncovered, strings.TrimSpace(line))
		}
	}

	if len(uncovered) == 0 {
		fmt.Println("\n  No uncovered handler functions found.")
		return
	}

	fmt.Printf("\n  \033[1;33mUncovered handler functions (%d):\033[0m\n", len(uncovered))
	for _, line := range uncovered {
		fmt.Printf("    %s\n", line)
	}
}

// pkgCoverage holds statement-level coverage for one package.
type pkgCoverage struct {
	pkg            string
	stmts, covered int
}

// perPackageStatementCoverage reads a Go coverage profile and aggregates
// statement-level coverage per package. The profile contains duplicate blocks
// when multiple test binaries cover the same source (each `go test` run
// emits its own copy under -coverpkg=./...); we dedupe by block location and
// OR the counts (per "mode: set" semantics) before summing. Skips generated
// _templ.go files.
// Returns (rows, totalCovered, totalStmts, err).
func perPackageStatementCoverage(profile string) ([]pkgCoverage, int, int, error) {
	f, err := os.Open(profile)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()

	// First pass: dedupe block lines, keep the maximum count seen for each
	// unique <file>:<range> key.
	type blockInfo struct {
		filePath string
		numStmts int
		hit      bool
	}
	blocks := map[string]*blockInfo{}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}

		// Format: "<file>:<startL>.<startC>,<endL>.<endC> <numStmts> <count>"
		space := strings.LastIndexByte(line, ' ')
		if space < 0 {
			continue
		}
		count := atoiOr(line[space+1:], 0)
		rest := line[:space]
		space2 := strings.LastIndexByte(rest, ' ')
		if space2 < 0 {
			continue
		}
		numStmts := atoiOr(rest[space2+1:], 0)
		key := rest[:space2] // "<file>:<startL>.<startC>,<endL>.<endC>"

		before, _, ok := strings.Cut(key, ":")
		if !ok {
			continue
		}
		filePath := before
		if strings.HasSuffix(filePath, "_templ.go") {
			continue
		}

		b := blocks[key]
		if b == nil {
			b = &blockInfo{filePath: filePath, numStmts: numStmts}
			blocks[key] = b
		}
		if count > 0 {
			b.hit = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, 0, err
	}

	// Second pass: aggregate per package.
	const modulePrefix = "git.duckfam.us/jj-software/cog/"
	pkgs := map[string]*pkgCoverage{}
	var totalCovered, totalStmts int
	for _, b := range blocks {
		path := strings.TrimPrefix(b.filePath, modulePrefix)
		slash := strings.LastIndexByte(path, '/')
		pkg := "."
		if slash >= 0 {
			pkg = path[:slash]
		}
		p := pkgs[pkg]
		if p == nil {
			p = &pkgCoverage{pkg: pkg}
			pkgs[pkg] = p
		}
		p.stmts += b.numStmts
		totalStmts += b.numStmts
		if b.hit {
			p.covered += b.numStmts
			totalCovered += b.numStmts
		}
	}

	rows := make([]pkgCoverage, 0, len(pkgs))
	for _, p := range pkgs {
		rows = append(rows, *p)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].pkg < rows[j].pkg })
	return rows, totalCovered, totalStmts, nil
}

func printPerPackageTable(rows []pkgCoverage) {
	fmt.Println("\n  Per-package statement coverage (cross-package profile):")
	for _, r := range rows {
		pct := 0.0
		if r.stmts > 0 {
			pct = 100 * float64(r.covered) / float64(r.stmts)
		}
		color := "\033[0;32m"
		switch {
		case pct < 30:
			color = "\033[0;31m"
		case pct < 60:
			color = "\033[0;33m"
		}
		fmt.Printf("    %s%5.1f%%\033[0m  %-40s (%d/%d stmts)\n",
			color, pct, r.pkg, r.covered, r.stmts)
	}
}

func atoiOr(s string, def int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return def
	}
	return n
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
