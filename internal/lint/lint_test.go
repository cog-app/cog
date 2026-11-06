package lint_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// root is the module root, relative to this package.
const root = "../.."

// sourceFiles parses every hand-written, non-test Go file in the module.
func sourceFiles(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "tmp" || name == "testdata" || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_templ.go") || strings.HasSuffix(name, ".pb.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = f
		return nil
	})
	if err != nil {
		t.Fatalf("walk source: %v", err)
	}
	return fset, files
}

// isCall reports whether e calls pkg.name.
func isCall(e ast.Expr, pkg, name string) (*ast.CallExpr, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return nil, false
	}
	id, ok := sel.X.(*ast.Ident)
	return call, ok && id.Name == pkg
}

// nonNumericVerb matches a format verb other than a number's, so a format
// it does not match cannot produce a quote or a line break.
var nonNumericVerb = regexp.MustCompile(`%[-+# 0-9.*]*[^-+# 0-9.*dxXob%]`)

// TestNoFormattedHeaderValues refuses header values built with fmt.Sprintf
// from anything but numbers. A string formatted into a header can end the
// value or start another header (email header injection, H12; an unquoted
// Content-Disposition filename). Use the typed builders instead:
// internal/httphdr, mime.FormatMediaType, internal/email.Message.
func TestNoFormattedHeaderValues(t *testing.T) {
	fset, files := sourceFiles(t)
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Set" && sel.Sel.Name != "Add") {
				return true
			}
			recv, ok := sel.X.(*ast.CallExpr)
			if !ok {
				return true
			}
			if rs, ok := recv.Fun.(*ast.SelectorExpr); !ok || rs.Sel.Name != "Header" {
				return true
			}
			sprintf, ok := isCall(call.Args[1], "fmt", "Sprintf")
			if !ok || len(sprintf.Args) == 0 {
				return true
			}
			lit, ok := sprintf.Args[0].(*ast.BasicLit)
			if ok {
				format, err := strconv.Unquote(lit.Value)
				if err == nil && !nonNumericVerb.MatchString(format) {
					return true
				}
			}
			t.Errorf("%s: header value built with fmt.Sprintf; use a typed builder", fset.Position(call.Pos()))
			return true
		})
	}
}

// TestHTTPClientsAreSafe refuses outbound HTTP that bypasses safehttp: the
// default client, its http.Get-style helpers, and hand-made clients and
// transports. Their URLs usually come from users or remote servers, and only
// safehttp keeps those off loopback, the LAN and cloud metadata. A client for
// a URL an admin configured may say so with a "trusted URL:" comment on its
// line or the one above.
func TestHTTPClientsAreSafe(t *testing.T) {
	fset, files := sourceFiles(t)
	for path, f := range files {
		if strings.HasPrefix(path, "internal/safehttp/") || strings.HasPrefix(path, "internal/cmd/") {
			continue
		}
		trusted := map[int]bool{}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.Contains(c.Text, "trusted URL:") {
					line := fset.Position(c.Pos()).Line
					trusted[line], trusted[line+1] = true, true
				}
			}
		}
		flag := func(n ast.Node, what string) {
			if pos := fset.Position(n.Pos()); !trusted[pos.Line] {
				t.Errorf("%s: %s bypasses safehttp; use safehttp.Client or ClientWith", pos, what)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := n.X.(*ast.Ident); ok && id.Name == "http" {
					switch n.Sel.Name {
					case "DefaultClient", "DefaultTransport", "Get", "Post", "Head", "PostForm":
						flag(n, "http."+n.Sel.Name)
					}
				}
			case *ast.CompositeLit:
				if sel, ok := n.Type.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "http" && (sel.Sel.Name == "Client" || sel.Sel.Name == "Transport") {
						flag(n, "http."+sel.Sel.Name+" literal")
					}
				}
			}
			return true
		})
	}
}

// TestProgramsAreSandboxed refuses exec.Command in the plugins outside
// internal/sandbox: the programs plugins run parse what users upload and
// feeds supply. A program that reads none of it says why on the line before,
// with "// unsandboxed:".
func TestProgramsAreSandboxed(t *testing.T) {
	fset, files := sourceFiles(t)
	for path, f := range files {
		if !strings.HasPrefix(path, "plugin/") {
			continue
		}
		exempt := map[int]bool{}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.Contains(c.Text, "unsandboxed:") {
					line := fset.Position(c.Pos()).Line
					exempt[line], exempt[line+1] = true, true
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
				if pos := fset.Position(sel.Pos()); !exempt[pos.Line] {
					t.Errorf("%s: exec.%s runs a program unsandboxed; use sandbox.CommandContext", pos, sel.Sel.Name)
				}
			}
			return true
		})
	}
}

var publicDirective = regexp.MustCompile(`(^|[ ,])public([ ,]|$)`)

// TestNoPublicCaching refuses Cache-Control: public outside the static
// assets. Every other response is for a signed-in user, or names something by
// an ID that means a different thing to each user (audit M17); a shared cache
// would serve it to the next client.
func TestNoPublicCaching(t *testing.T) {
	fset, files := sourceFiles(t)
	for path, f := range files {
		if strings.HasPrefix(path, "internal/assets/") {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err == nil && publicDirective.MatchString(v) && strings.Contains(v, "max-age") {
				t.Errorf("%s: Cache-Control public outside internal/assets", fset.Position(lit.Pos()))
			}
			return true
		})
	}
}

// dataDirOwners are the files that own a piece of the data directory's
// layout, and why. Everything else asks one of them: plugins get
// plugin.Storage, databases come from the registry, and nothing else
// builds a path into the data directory, so the layout can change in one
// place and no hand-built path is a traversal risk (D8, C1, M12).
var dataDirOwners = map[string]string{
	"internal/storage/":                   "plugin storage, user and legacy directories",
	"internal/db/plugindb.go":             "where plugin databases live",
	"internal/plugin/registry.go":         "per-user directories, root databases",
	"internal/search/manager.go":          "search indexes",
	"internal/ssh/hostkey.go":             "the SSH host key",
	"internal/services/services.go":       "the master secret",
	"plugin/plugcmd/trusted.go":           "a trusted process plugin's mirror of the registry",
	"internal/testing/cogtest/cogtest.go": "the test server's system database",
}

// mentionsDataDir reports whether e names a data directory: an identifier
// or field called dataDir, DataDir, rootDataDir and the like.
func mentionsDataDir(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && strings.Contains(strings.ToLower(id.Name), "datadir") {
			found = true
		}
		return !found
	})
	return found
}

// hasSlash reports whether e is a string literal holding a path separator.
func hasSlash(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && strings.Contains(lit.Value, "/")
}

// TestNoDataDirPaths refuses paths built from the data directory outside
// the files that own its layout (dataDirOwners).
func TestNoDataDirPaths(t *testing.T) {
	fset, files := sourceFiles(t)
	for name, f := range files {
		owned := false
		for prefix := range dataDirOwners {
			if strings.HasPrefix(name, prefix) {
				owned = true
			}
		}
		if owned {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				_, fp := isCall(n, "filepath", "Join")
				_, pp := isCall(n, "path", "Join")
				if fp || pp {
					for _, arg := range n.Args {
						if mentionsDataDir(arg) {
							t.Errorf("%s: path built from the data directory; use internal/storage or plugin.Storage", fset.Position(n.Pos()))
						}
					}
				}
			case *ast.BinaryExpr:
				if n.Op == token.ADD && (mentionsDataDir(n.X) && hasSlash(n.Y) || hasSlash(n.X) && mentionsDataDir(n.Y)) {
					t.Errorf("%s: path built from the data directory; use internal/storage or plugin.Storage", fset.Position(n.Pos()))
				}
			}
			return true
		})
	}
}
