// Verify tool for Cog. Usage: go tool verify [-cover-only] [-continue]
//
// Runs code quality checks in sequence: generate, fmt, fix, vet, and tests
// with cross-package coverage. Reports uncovered handler functions at the end.
//
// Use -cover-only to skip generate/fmt/vet and jump straight to tests.
// Use -continue to keep going after a step fails.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	coverOnly := flag.Bool("cover-only", false, "skip generate/fmt/vet, run tests with coverage only")
	cont := flag.Bool("continue", false, "continue on failure instead of stopping")
	flag.Parse()

	coverProfile := filepath.Join("tmp", "coverage.out")
	os.MkdirAll("tmp", 0o755)

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
		"./...",
	); !ok && !*cont {
		os.Exit(1)
	}

	if _, err := os.Stat(coverProfile); err == nil {
		reportCoverage(coverProfile)
	}

	if failed {
		fmt.Println("\n\033[1;31mVerification failed.\033[0m")
		os.Exit(1)
	}
	fmt.Println("\n\033[1;32mAll checks passed.\033[0m")
}

func reportCoverage(profile string) {
	fmt.Printf("\n\033[1;34m▶ Coverage report\033[0m\n")

	cmd := exec.Command("go", "tool", "cover", "-func="+profile)
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  cover -func failed: %v\n", err)
		return
	}

	// Print total line
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	var totalLine string
	var uncovered []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "total:") {
			totalLine = line
			continue
		}
		if !strings.HasSuffix(strings.TrimSpace(line), "0.0%") {
			continue
		}
		// Skip templates, test infrastructure, CLI commands, generated code
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
		// Only report handler-like functions
		lower := strings.ToLower(line)
		if containsAny(lower, "handle", "serve", "page", "view",
			"create", "update", "delete", "list", "get",
			"import", "export", "search", "download", "upload",
			"toggle", "star", "render", "show", "browse",
			"dispatch", "respond", "settings") {
			uncovered = append(uncovered, strings.TrimSpace(line))
		}
	}

	if totalLine != "" {
		fmt.Printf("  %s\n", totalLine)
	}

	if len(uncovered) == 0 {
		fmt.Println("  No uncovered handler functions found.")
		return
	}

	fmt.Printf("\n  \033[1;33mUncovered handler functions (%d):\033[0m\n", len(uncovered))
	for _, line := range uncovered {
		fmt.Printf("    %s\n", line)
	}
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
