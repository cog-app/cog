//go:build seed

package notes

import (
	"fmt"
	"log"
)

// Seed creates sample notes for a user demonstrating advanced markdown features.
func (p *Plugin) Seed(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return fmt.Errorf("seed notes: %w", err)
	}

	// Check if already seeded.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&count)
	if count > 0 {
		return nil
	}

	notesDir := p.NotesDir(username)
	if err := EnsureNotesDir(notesDir); err != nil {
		return fmt.Errorf("seed notes: ensure dir: %w", err)
	}

	type seedNote struct {
		title    string
		category string
		favorite bool
		content  string
	}

	seeds := []seedNote{
		{
			title:    "Welcome to Notes",
			category: "",
			favorite: true,
			content: `# Welcome to Notes

This is your **notes** app. Notes are stored as Markdown files in your ` + "`Notes/`" + ` directory.

## Features

- **Bold**, *italic*, and ~~strikethrough~~ text
- [Links](https://example.com)
- Task lists, tables, code blocks, and more

> Tip: Click **Edit** to see the raw Markdown source for any note.
`,
		},
		{
			title:    "Strikethrough and GFM",
			category: "examples",
			content: `# GitHub Flavored Markdown

## Strikethrough

This is ~~no longer relevant~~ and this is ~~deleted text~~.

Corrections: The meeting is on ~~Tuesday~~ **Wednesday**.

## Task Lists

- [x] Enable GFM extensions
- [x] Strikethrough support
- [ ] Autosave (coming soon)
- [ ] Revision history

## Tables

| Feature        | Status    | Notes                |
|----------------|-----------|----------------------|
| Strikethrough  | Done      | Uses ` + "`~~text~~`" + `      |
| Tables         | Done      | GFM pipe tables      |
| Task lists     | Done      | ` + "`- [x]`" + ` syntax       |
| Autolinks      | Done      | https://example.com  |

## Autolinks

Visit https://example.com or email user@example.com — automatically linked.
`,
		},
		{
			title:    "Code and Syntax Highlighting",
			category: "examples",
			content: `# Syntax Highlighting

Code blocks are highlighted with language-aware coloring.

## Go

` + "```go" + `
package main

import "fmt"

func main() {
	ch := make(chan string, 1)
	ch <- "Hello, Notes!"
	fmt.Println(<-ch)
}
` + "```" + `

## Python

` + "```python" + `
from dataclasses import dataclass

@dataclass
class Note:
    title: str
    content: str
    favorite: bool = False

    def word_count(self) -> int:
        return len(self.content.split())

notes = [Note("First", "Hello world"), Note("Second", "Another note")]
print(f"Total notes: {len(notes)}")
` + "```" + `

## SQL

` + "```sql" + `
SELECT n.id, n.filename, n.category,
       COUNT(*) OVER (PARTITION BY n.category) AS category_count
FROM notes n
WHERE n.favorite = 1
ORDER BY n.modified DESC
LIMIT 10;
` + "```" + `

## JavaScript

` + "```javascript" + `
const render = async (markdown) => {
  const response = await fetch('/api/render', {
    method: 'POST',
    headers: { 'Content-Type': 'text/markdown' },
    body: markdown,
  });
  return response.text();
};
` + "```" + `

## Inline Code

Use ` + "`fmt.Println()`" + ` to print or ` + "`SELECT *`" + ` to query.
`,
		},
		{
			title:    "LaTeX Math",
			category: "examples",
			content: `# LaTeX Math Rendering

Math is rendered using KaTeX. Use ` + "`$$...$$`" + ` for display math and ` + "`$...$`" + ` for inline.

## Inline Math

The quadratic formula gives $x = \frac{-b \pm \sqrt{b^2 - 4ac}}{2a}$ for any $ax^2 + bx + c = 0$.

Euler's identity: $e^{i\pi} + 1 = 0$.

## Display Math

The Gaussian integral:

$$\int_{-\infty}^{\infty} e^{-x^2} \, dx = \sqrt{\pi}$$

Maxwell's equations in differential form:

$$\nabla \cdot \mathbf{E} = \frac{\rho}{\varepsilon_0}$$

$$\nabla \cdot \mathbf{B} = 0$$

$$\nabla \times \mathbf{E} = -\frac{\partial \mathbf{B}}{\partial t}$$

$$\nabla \times \mathbf{B} = \mu_0 \mathbf{J} + \mu_0 \varepsilon_0 \frac{\partial \mathbf{E}}{\partial t}$$

## Matrix Notation

$$A = \begin{pmatrix} a_{11} & a_{12} & \cdots & a_{1n} \\ a_{21} & a_{22} & \cdots & a_{2n} \\ \vdots & \vdots & \ddots & \vdots \\ a_{m1} & a_{m2} & \cdots & a_{mn} \end{pmatrix}$$

## Summation and Limits

$$\sum_{n=1}^{\infty} \frac{1}{n^2} = \frac{\pi^2}{6}$$

$$\lim_{n \to \infty} \left(1 + \frac{1}{n}\right)^n = e$$
`,
		},
		{
			title:    "Mermaid Diagrams",
			category: "examples",
			content: `# Mermaid Diagrams

Diagrams are rendered using Mermaid. Use a fenced code block with language ` + "`mermaid`" + `.

## Flowchart

` + "```mermaid" + `
flowchart TD
    A[Write Markdown] --> B{Has diagrams?}
    B -->|Yes| C[Render Mermaid]
    B -->|No| D[Render HTML]
    C --> D
    D --> E[Display Note]
` + "```" + `

## Sequence Diagram

` + "```mermaid" + `
sequenceDiagram
    participant User
    participant Browser
    participant Server
    participant DB

    User->>Browser: Edit note
    Browser->>Server: POST /notes/{id}
    Server->>DB: UPDATE notes SET ...
    DB-->>Server: OK
    Server->>Server: Write .md file
    Server-->>Browser: 303 Redirect
    Browser->>Server: GET /note/{id}
    Server-->>Browser: Rendered HTML
` + "```" + `

## Entity Relationship

` + "```mermaid" + `
erDiagram
    USER ||--o{ NOTE : writes
    NOTE {
        int id PK
        string filename
        string category
        bool favorite
        string etag
        int modified
    }
    NOTE }o--|| CATEGORY : belongs_to
    CATEGORY {
        string name
    }
` + "```" + `

## Pie Chart

` + "```mermaid" + `
pie title Notes by Category
    "examples" : 5
    "recipes" : 3
    "work" : 2
    "uncategorized" : 1
` + "```" + `
`,
		},
		{
			title:    "Everything Combined",
			category: "examples",
			favorite: true,
			content: `# Kitchen Sink

A note using ~~all~~ **every** advanced feature together.

## Status

| Feature       | Syntax               | Works? |
|---------------|----------------------|--------|
| Strikethrough | ` + "`~~text~~`" + `          | ~~yes~~ Done |
| Code          | Triple backtick      | See below    |
| LaTeX         | ` + "`$...$`" + ` / ` + "`$$...$$`" + ` | $\checkmark$ |
| Mermaid       | ` + "` ```mermaid `" + `     | See below    |
| Task lists    | ` + "`- [x]`" + `            | Below        |

## Checklist

- [x] Strikethrough via GFM
- [x] Syntax-highlighted code blocks
- [x] LaTeX math rendering
- [x] Mermaid diagrams
- [ ] Rich text editor

## A Bit of Math

The area under a standard normal curve: $\frac{1}{\sqrt{2\pi}} \int_{-\infty}^{\infty} e^{-x^2/2} \, dx = 1$

The Fibonacci recurrence:

$$F_n = F_{n-1} + F_{n-2}, \quad F_0 = 0,\ F_1 = 1$$

## A Bit of Code

` + "```go" + `
// Render converts markdown to safe HTML.
func Render(src string) string {
    var buf bytes.Buffer
    md.Convert([]byte(src), &buf)
    return sanitizer.Sanitize(buf.String())
}
` + "```" + `

## A Bit of Diagram

` + "```mermaid" + `
flowchart LR
    MD[Markdown] --> GM[Goldmark]
    GM --> HL[Syntax Highlighting]
    GM --> HTML[HTML Output]
    HTML --> BM[Bluemonday Sanitizer]
    BM --> Page[Rendered Page]
    Page --> KaTeX[KaTeX Client]
    Page --> Mermaid[Mermaid Client]
` + "```" + `
`,
		},
	}

	for _, s := range seeds {
		filename := FilenameFromTitle(s.title)
		etag := ComputeETag(s.content)

		if err := WriteContent(notesDir, filename, s.content); err != nil {
			log.Printf("seed note %s: write: %v", s.title, err)
			continue
		}

		note, err := CreateNoteMeta(db, filename, s.category, etag)
		if err != nil {
			log.Printf("seed note %s: meta: %v", s.title, err)
			continue
		}

		if s.favorite {
			ToggleFavorite(db, note.ID)
		}

		log.Printf("Seeded note: %s", s.title)
	}

	return nil
}
