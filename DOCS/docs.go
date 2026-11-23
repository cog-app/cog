// Package docs holds Cog's documentation as Markdown. Every Cog serves it
// at /docs/, and `cog docs` builds it into the website (internal/docs).
//
// A page is a Go template (text/template) before it is Markdown, so it can
// say different things in Cog and on the website: {{ if .Site }} is true on
// the website, {{ .Version }} is the running Cog's version, and {{ skip }}
// leaves the page out (the installation guide, inside an installed Cog).
// Write a literal {{ as {{ "{{" }}.
package docs

import "embed"

// FS is the Markdown pages, at their paths under DOCS/.
//
//go:embed *.md */*.md
var FS embed.FS
