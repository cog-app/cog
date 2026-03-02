# Frontend Design

## Tech Stack

- **HTMX:** For dynamic interactions without heavy client-side JavaScript.
- **Templ:** For type-safe, compiled Go templates.
- **Vanilla CSS:** Clean, lightweight styling (no heavy frameworks like Bootstrap/Tailwind unless necessary).

## Component Library

Cog exports a Go package `github.com/duckfam/cog/ui` containing `templ` components. This ensures plugins look native.

### Usage Example (Go/Templ)

```go
package myplugin

import "github.com/duckfam/cog/ui"

templ ShowWidget(data string) {
    @ui.PageLayout("My Widget") {
        @ui.Card() {
            <h1>{ data }</h1>
            @ui.Button(ui.ButtonPrimary, "Click Me")
        }
    }
}
```

## NextCloud Compatibility

To support NextCloud apps (which often expect specific DOM structures or endpoints), Cog provides a "Legacy/Compat" layer.

- **OCS API:** Implements Open Collaboration Services API responses.
- **WebDAV:** Standard implementation for file browsing.
- **Theme:** CSS variables will attempt to match standard NC variables where possible to allow simple NC apps to render decently, though perfect visual parity is secondary to Cog's own design.

## Routing

- `/`: Dashboard
- `/files/*`: File browser
- `/apps/{plugin_id}/*`: Plugin routes
- `/settings`: User settings
