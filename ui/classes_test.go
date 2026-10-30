package ui

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// classAllowlist holds class names templates may use that no stylesheet
// defines, each for a reason. Keep it short: a class that styles nothing is
// usually a typo or a rule that was renamed in one place and not the other,
// which is how undefined classes like .items-end and .form-label once left
// whole forms unstyled.
//
// Prefer a data-* attribute to a class for a JavaScript hook.
var classAllowlist = map[string]string{
	"picker-value": "JS hook: the hidden input ui.Picker writes the chosen ID to",
}

// classPrefixAllowlist holds prefixes of class names styled by third-party
// code that ships its own CSS.
var classPrefixAllowlist = []string{
	"fc-",    // FullCalendar
	"htmx-",  // htmx's own state classes
	"katex",  // KaTeX math rendering
	"chroma", // syntax highlighting, styled inline by the highlighter
}

// varPrefixAllowlist holds custom properties defined by third-party CSS.
var varPrefixAllowlist = []string{
	"--fc-", // FullCalendar's theme variables
}

var (
	cssComment     = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cssClass       = regexp.MustCompile(`\.(-?[A-Za-z_][\w-]*)`)
	cssVarDef      = regexp.MustCompile(`(--[\w-]+)\s*:`)
	cssVarUse      = regexp.MustCompile(`var\(\s*(--[\w-]+)`)
	styleBlock     = regexp.MustCompile(`(?s)<style[^>]*>(.*?)</style>`)
	classStatic    = regexp.MustCompile(`\bclass="([^"]*)"`)
	classExpr      = regexp.MustCompile(`\bclass=\{([^}]*)\}`)
	goStringLit    = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	templInterpCSS = regexp.MustCompile(`\{[^}]*\}`)
)

// TestTemplateClassesAreDefined fails when a template uses a CSS class or a
// custom property that style.css does not define. A class may also be defined
// by a <style> block in the same template file.
func TestTemplateClassesAreDefined(t *testing.T) {
	root := repoRoot(t)

	sheet, err := os.ReadFile(filepath.Join(root, "ui", "static", "style.css"))
	if err != nil {
		t.Fatal(err)
	}
	css := cssComment.ReplaceAllString(string(sheet), "")
	defined := matchSet(cssClass, css)
	vars := matchSet(cssVarDef, css)

	// A custom property used by the stylesheet itself must exist too.
	missing := map[string][]string{}
	for v := range matchSet(cssVarUse, css) {
		if !vars[v] && !hasPrefix(v, varPrefixAllowlist) {
			missing["ui/static/style.css"] = append(missing["ui/static/style.css"], "var("+v+")")
		}
	}

	files := templFiles(t, root)
	// Custom properties a template sets inline (style="--cal-color: …") count
	// as defined for every template.
	for _, f := range files {
		for v := range matchSet(cssVarDef, f.src) {
			vars[v] = true
		}
	}

	for _, f := range files {
		local := map[string]bool{}
		for _, m := range styleBlock.FindAllStringSubmatch(f.src, -1) {
			for c := range matchSet(cssClass, cssComment.ReplaceAllString(m[1], "")) {
				local[c] = true
			}
		}
		for _, c := range templateClasses(f.src) {
			if defined[c] || local[c] || classAllowlist[c] != "" || hasPrefix(c, classPrefixAllowlist) {
				continue
			}
			missing[f.rel] = append(missing[f.rel], "."+c)
		}
		for v := range matchSet(cssVarUse, f.src) {
			if !vars[v] && !hasPrefix(v, varPrefixAllowlist) {
				missing[f.rel] = append(missing[f.rel], "var("+v+")")
			}
		}
	}

	if len(missing) == 0 {
		return
	}
	paths := make([]string, 0, len(missing))
	for p := range missing {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		names := dedupe(missing[p])
		b.WriteString("\n  " + p + ": " + strings.Join(names, " "))
	}
	t.Errorf("templates use CSS that ui/static/style.css does not define (add a rule, fix the name, or for a JS hook use a data-* attribute):%s", b.String())
}

// templateClasses returns the class names a template can put on an element:
// every word of a literal class="…", and every word of the string literals in
// a class={ … } expression. A word ending in "-" is the start of a name built
// at run time ("alert-" + kind) and is skipped.
func templateClasses(src string) []string {
	var out []string
	add := func(s string) {
		for _, c := range strings.Fields(s) {
			// Not a class: a name finished at run time, a URL or a format
			// string passed to a helper inside the expression.
			if strings.HasSuffix(c, "-") || strings.ContainsAny(c, "{}()+$'/%") {
				continue
			}
			out = append(out, c)
		}
	}
	for _, m := range classStatic.FindAllStringSubmatch(src, -1) {
		add(templInterpCSS.ReplaceAllString(m[1], " "))
	}
	for _, m := range classExpr.FindAllStringSubmatch(src, -1) {
		for _, lit := range goStringLit.FindAllStringSubmatch(m[1], -1) {
			add(lit[1])
		}
	}
	return out
}

type templFile struct {
	rel string
	src string
}

func templFiles(t *testing.T, root string) []templFile {
	t.Helper()
	var files []templFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (strings.HasPrefix(name, ".") || name == "tmp" || name == "node_modules" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".templ") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files = append(files, templFile{rel: rel, src: string(b)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("found no .templ files")
	}
	return files
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func matchSet(re *regexp.Regexp, s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	return out
}

func hasPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
