package notes

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// md is the goldmark instance with extensions enabled.
var md = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM, // strikethrough, tables, autolinks, task lists
		highlighting.NewHighlighting(
			highlighting.WithStyle("monokai"),
			highlighting.WithFormatOptions(),
		),
	),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
	),
	goldmark.WithRendererOptions(
		html.WithUnsafe(), // allow raw HTML so our LaTeX wrappers pass through
	),
)

// sanitizer is a permissive HTML sanitizer that allows rendered markdown features.
var sanitizer = buildSanitizer()

func buildSanitizer() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	// Allow style attributes for syntax highlighting inline styles
	p.AllowAttrs("style").Globally()
	// Allow class attributes for code blocks, mermaid, katex
	p.AllowAttrs("class").Globally()
	// Allow data attributes for mermaid
	p.AllowAttrs("data-lang").OnElements("code")
	// Allow pre > code structure from highlighting
	p.AllowElements("pre", "code", "span")
	// Allow del/s for strikethrough
	p.AllowElements("del", "s")
	// Allow input for task list checkboxes
	p.AllowAttrs("type", "checked", "disabled").OnElements("input")
	// Allow id for heading anchors
	p.AllowAttrs("id").OnElements("h1", "h2", "h3", "h4", "h5", "h6")
	return p
}

// mermaidRe matches fenced code blocks with language "mermaid" in goldmark's output.
var mermaidRe = regexp.MustCompile(`<pre><code class="language-mermaid">([\s\S]*?)</code></pre>`)

// latexBlockRe matches $$ ... $$ display math blocks.
var latexBlockRe = regexp.MustCompile(`\$\$([\s\S]*?)\$\$`)

// latexInlineRe matches $...$ inline math (not $$, not escaped \$).
var latexInlineRe = regexp.MustCompile(`(?:^|[^$\\])\$([^\n$]+?)\$(?:[^$]|$)`)

// RenderMarkdown converts markdown to sanitized HTML with advanced features.
// LaTeX is preserved as raw text for client-side KaTeX rendering.
// Mermaid code blocks are converted to <pre class="mermaid"> for client-side rendering.
func RenderMarkdown(markdown string) string {
	// Wrap LaTeX in raw HTML tags so goldmark passes them through.
	processed := protectLatex(markdown)

	var buf bytes.Buffer
	if err := md.Convert([]byte(processed), &buf); err != nil {
		return ""
	}
	result := buf.String()

	// Convert mermaid code blocks to <pre class="mermaid"> for mermaid.js
	result = mermaidRe.ReplaceAllStringFunc(result, func(match string) string {
		sub := mermaidRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		content := strings.ReplaceAll(sub[1], "&amp;", "&")
		content = strings.ReplaceAll(content, "&lt;", "<")
		content = strings.ReplaceAll(content, "&gt;", ">")
		content = strings.ReplaceAll(content, "&quot;", "\"")
		return `<pre class="mermaid">` + content + `</pre>`
	})

	return sanitizer.Sanitize(result)
}

// protectLatex wraps LaTeX expressions in raw HTML tags so goldmark
// passes them through without parsing. KaTeX's auto-render picks them
// up client-side via the $$ and $ delimiters inside the elements.
func protectLatex(input string) string {
	// Display math: $$ ... $$ → <div class="math-display">$$...$$</div>
	// Must come before inline to avoid partial matches.
	result := latexBlockRe.ReplaceAllStringFunc(input, func(match string) string {
		return "\n\n<div class=\"math-display\">" + match + "</div>\n\n"
	})

	// Inline math: $...$ → <span class="math-inline">$...$</span>
	// Only replace the $...$ part, preserving surrounding chars.
	result = latexInlineRe.ReplaceAllStringFunc(result, func(match string) string {
		// Find the $ delimiters within the match (which includes optional surrounding chars)
		start := strings.Index(match, "$")
		end := strings.LastIndex(match, "$")
		if start == end || start < 0 {
			return match
		}
		before := match[:start]
		inner := match[start : end+1] // includes the $ delimiters
		after := match[end+1:]
		return before + "<span class=\"math-inline\">" + inner + "</span>" + after
	})

	return result
}

// NeedsKaTeX reports whether the markdown content contains LaTeX expressions.
func NeedsKaTeX(content string) bool {
	return latexBlockRe.MatchString(content) || latexInlineRe.MatchString(content)
}

// NeedsMermaid reports whether the markdown content contains mermaid diagrams.
func NeedsMermaid(content string) bool {
	return strings.Contains(content, "```mermaid")
}

// StripMarkdown converts markdown to plain text (no HTML tags).
func StripMarkdown(markdown string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(markdown), &buf); err != nil {
		return markdown
	}
	return bluemonday.StrictPolicy().Sanitize(buf.String())
}

// ComputeETag returns an MD5 hex digest of the content.
func ComputeETag(content string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(content)))
}
