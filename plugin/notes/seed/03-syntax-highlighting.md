---
title: Code and Syntax Highlighting
category: examples
---
# Syntax Highlighting

Code blocks are highlighted with language-aware coloring.

## Go

```go
package main

import "fmt"

func main() {
	ch := make(chan string, 1)
	ch <- "Hello, Notes!"
	fmt.Println(<-ch)
}
```

## Python

```python
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
```

## SQL

```sql
SELECT n.id, n.filename, n.category,
       COUNT(*) OVER (PARTITION BY n.category) AS category_count
FROM notes n
WHERE n.favorite = 1
ORDER BY n.modified DESC
LIMIT 10;
```

## JavaScript

```javascript
const render = async (markdown) => {
  const response = await fetch('/api/render', {
    method: 'POST',
    headers: { 'Content-Type': 'text/markdown' },
    body: markdown,
  });
  return response.text();
};
```

## Inline Code

Use `fmt.Println()` to print or `SELECT *` to query.
