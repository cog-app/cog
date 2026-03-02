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

## Routing & Security

Cog uses a structured routing system to balance ease of use with security (CSRF protection).

### Route Patterns

- **APIs (`/api/{plugin_id}/*`):**
    - **Purpose:** Data exchange, mobile apps, third-party integrations.
    - **Format:** Typically JSON.
    - **CSRF:** Bypassed. Auth is handled via Bearer tokens or App Passwords.
- **Pages (`/apps/{plugin_id}/*`):**
    - **Purpose:** User-facing HTML interfaces.
    - **Format:** HTML (rendered via `templ` or Go templates).
    - **CSRF:** GET requests are safe; forms within pages must include a CSRF token for the plugin.
    - **Optimization:** Can use server-side Go templates with direct SQLite access to minimize round-trips for dynamic data.
- **Actions:**
    - **Method:** POST, PUT, DELETE.
    - **Purpose:** State-changing operations (form submissions).
    - **Format:** HTMX fragments.
    - **CSRF:** Strictly enforced. Tokens are namespaced per plugin.
- **Resources (`/apps/{plugin_id}/{resource_name}/{resource_id}`):**
    - **Purpose:** Direct linking to specific plugin objects.
    - **Use Cases:** Global search results, public shares, notifications.

### CSRF Implementation

1.  **Namespacing:** Each plugin has its own CSRF secret to ensure a compromise in one plugin doesn't affect others.
2.  **Middleware:** The Host provides a global CSRF middleware that validates tokens for all `/apps/` and `/settings/` routes.
3.  **HTMX Integration:** HTMX requests automatically include the CSRF token in the `X-CSRF-Token` header via a global event listener configured in the main layout.

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
