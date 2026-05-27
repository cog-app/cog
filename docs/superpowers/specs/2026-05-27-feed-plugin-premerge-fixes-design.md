# Feed Plugin Pre-Merge Fixes — Design

Date: 2026-05-27
Branch: `feat/feed-plugin`
Status: Approved for planning

## Goal

Five UX/structure fixes to the feed plugin before merging to `master`:

1. Browser history push when the player changes item.
2. Sidebar visible on every feed page (item views, settings, admin).
3. A context bar at the top of each item view: which "playlist" you're viewing + prev/next.
4. A setting to hide folders/subscriptions with no unread items.
5. Split the monolithic settings page into four pages (+ admin tabs).

All work lives under `plugin/feed/`. No changes to the host/plugin framework.

## Background (current state)

- Feed pages: `plugin/feed/pages.go` (handlers), `plugin/feed/pages.templ` (templ), `plugin/feed/store.go` (DB), `plugin/feed/static/player.js`.
- Routes registered in `plugin/feed/feed.go` `Register()`.
- List views (`Index`, `SubscriptionView`, `FolderView`, `StarredView`, `TagView`, `PlaylistView`, `HistoryView`) wrap content in `<div class="sidebar-layout">` + `@feedSidebar(sb)`. Sidebar built by `h.buildSidebar(store, activePath)` → `sidebarData`.
- These templates **lack** the sidebar: `feedItemDetail`, `feedSettingsPage`, `feedSubscriptionSettingsPage`, `feedSystemSettingsPage`, `feedAdminStatusPage`.
- `ItemDetail` (pages.go:609) renders `feedItemDetail`; media items embed `feedPlayer`.
- Navigation today is **global**: `Store.AdjacentMedia(id, dir)` and `Store.NextUnplayedMedia(excludeID)` order across ALL media items by `(published_at, id)`, ignoring the view the user came from. `player.js` advances by HTMX-swapping the `#feed-player` fragment via `GET /items/{id}/adjacent?dir=` and `POST /items/{id}/complete`; the URL never changes, so there is no history entry.
- View item lists are produced by `Store.ListItemsForView(spec ViewSpec, limit)` (+ `applyQueryFilter(store, items, q)` for playlist/all-search). `HistoryView` uses `ListPlaybackHistory()` (returns `[]PlayedItem`).
- Per-user player prefs (`PlayerPrefs`) and global system settings live in the `feed_settings` table; loaded/saved via `LoadPlayerPrefs`/`SavePlayerPrefs` (store.go:~1520) and the `Setting` accessors.
- Settings is one page (`Settings` handler, pages.go:80 → `feedSettingsPage`) with cards for Subscriptions, Folders, Filters, Playlists, plus OPML/other imports. System (whisper) settings are a separate framework settings page (`RegisterSettingsPage` → `SystemSettings`/`SaveSystemSettings`, served at `/apps/feed/admin/system-settings`). Orchestrator status is `GET /apps/feed/admin/status` → `AdminStatus` → `feedAdminStatusPage`, only linked from the system-settings page (easy to miss).

## Design

### 1. Sidebar on every page

Wrap the five sidebar-less templates in the same `sidebar-layout` structure used by list views, and have their handlers build `sb := h.buildSidebar(store, activePath)` and pass it in.

- `feedItemDetail(sb, ip, tags, prefs, artwork, csrf)` — `ItemDetail` builds the sidebar with `activePath` set to the originating view path (derived from ctx, see §3) so the correct sidebar entry stays highlighted; fall back to `/apps/feed/` when no ctx.
- `feedSettingsPage` → replaced by the four split pages (§6), each wrapped.
- `feedSubscriptionSettingsPage`, `feedSystemSettingsPage`, `feedAdminStatusPage` — each gains an `sb sidebarData` param and the wrapper; handlers (`SubscriptionSettings`, `SystemSettings`, `AdminStatus`) call `buildSidebar`.

No new layout CSS; reuse `.sidebar-layout` / `.sidebar-content`.

### 2. View context descriptor

A compact descriptor identifies which view ("playlist") an item was opened from, carried as query params on item links and player API calls.

Params:
- `ctx` ∈ {`all`, `starred`, `history`, `folder`, `sub`, `tag`, `playlist`}
- `cid` — numeric id for `folder`/`sub`/`playlist`
- `cname` — tag name for `tag`
- `cq` — search query string for the `all` view (Index search box)

Go representation in `pages.go`:

```go
type viewCtx struct {
    Kind string // all|starred|history|folder|sub|tag|playlist; "" => default all
    ID   int64
    Name string // tag name
    Q    string // all-view search query
}

func parseViewCtx(r *http.Request) viewCtx          // from query params
func (c viewCtx) encode() string                    // "ctx=folder&cid=3" (url.Values)
func (c viewCtx) label(store *Store) string          // "All", "Starred", "History",
                                                      // folder name, sub title,
                                                      // "Tag: x", "Playlist: x"
func (c viewCtx) path() string                        // originating view path for sidebar active highlight
```

Single resolver mirroring each view handler's exact list build, so prev/next equals the on-screen list:

```go
// resolveViewItems returns the same ordered slice the originating view rendered.
func (h *Pages) resolveViewItems(store *Store, c viewCtx) ([]ItemWithSubscription, error)
```

Implementation per kind (same limits the view handlers use, currently 200):
- `all`: `ListItemsForView(ViewSpec{All:true}, limit)`, then `applyQueryFilter(store, items, c.Q)` when `c.Q != ""`.
- `starred`: `ListItemsForView(ViewSpec{Starred:true}, limit)`.
- `folder`: `ListItemsForView(ViewSpec{FolderID: valid(c.ID)}, limit)`.
- `sub`: `ListItemsForView(ViewSpec{SubscriptionID: valid(c.ID)}, limit)`.
- `tag`: `ListItemsForView(ViewSpec{TagName: c.Name}, limit)`.
- `playlist`: load playlist, `ListItemsForView(ViewSpec{All:true}, limit)` + `applyQueryFilter(store, items, pl.Query)`.
- `history`: `ListPlaybackHistory()` mapped to the same `ItemWithSubscription` shape (id + title + type suffice for neighbor computation).
- empty/unknown: treat as `all` with no query.

The view handlers must emit `c.encode()` consistently. Each list view handler constructs its own `viewCtx` and threads it into `feedItemList` so the links are correct.

### 3. Top context bar on item views + history push

**Bar (full-page, all item types).** `ItemDetail` parses ctx, calls `resolveViewItems`, finds the current item's index, and computes prev/next neighbors (id, title, type). Renders a new `feedItemContextBar(c, store, prev, next)` above the card showing:
- the context label (`c.label(store)`), and
- Prev / Next buttons as anchors to `/apps/feed/items/{neighborID}?<ctx>`, each showing the neighbor's title; disabled (rendered as plain text, no anchor) at the ends.

`feedItemList` item links change from `/apps/feed/items/{id}` to `/apps/feed/items/{id}?<ctx>`.

**Player (media), context-aware + history push.** Move neighbor logic from global to context-list based:

- New store/handler helpers compute neighbors **within the resolved context list**:
  - player prev/next button → adjacent item of type `audio`/`video` in the context list (skip text items).
  - autoplay-next (on completion) → next playable media item *after* the current index in the context list.
- `GET /items/{id}/adjacent` (`AdjacentPlayer`) and `POST /items/{id}/complete` (`MarkComplete`) accept the ctx params, resolve the context list, and pick the neighbor by index instead of calling `AdjacentMedia`/`NextUnplayedMedia`. The rendered player fragment includes the ctx (data attribute) and enough info for the client to update the bar + URL.
- `Store.AdjacentMedia` / `Store.NextUnplayedMedia` may remain for now (callers switch to the context-based path); remove only if no caller remains after the change.

`player.js` changes:
- Read a `data-ctx` attribute (the encoded ctx string) on `#feed-player`; include it on `/adjacent` and `/complete` requests.
- On every successful player change (autoplay, prev, next), call
  `history.pushState({itemId}, '', '/apps/feed/items/' + id + '?' + ctx)`
  and update the context bar's label/prev/next links from data provided in the swapped fragment (a small `data-*` payload or an out-of-band swap of the bar).
- Add a `window.onpopstate` handler that reloads to `location.href` (back/forward returns to the prior item). Reload is acceptable; no SPA state to preserve.

Edge cases: item not present in the resolved list (e.g. became read and the view filters reads, or list truncated by limit) → bar shows label with both buttons disabled; player falls back to no-autoplay (empty body / 204 as today).

### 4. Hide-empty setting

New per-user boolean `hide_empty` (default `false`) stored in `feed_settings` next to the player prefs. Add `HideEmpty bool` to the loaded prefs (or a small dedicated `LoadDisplayPrefs`/`SaveDisplayPrefs`; reuse the player-prefs row to avoid a second round-trip).

- Sidebar gains a small toggle at its top: a checkbox labelled "Hide read" (folders/subs with no unread), `hx-post=/apps/feed/player/prefs` (extend `SavePlayerPrefs` to accept `hide_empty`, or add a dedicated `display/prefs` action) with `hx-target=#feed-sidebar` `hx-swap=outerHTML` to refresh the sidebar. Reuses the existing folder open/close preservation script.
- `buildSidebar` accepts/loads `hideEmpty`. When true:
  - a subscription link is omitted when `UnreadCounts[sub.ID] == 0`;
  - a folder is omitted when its folder-unread sum `== 0`;
  - **errored subs/folders with 0 unread are also hidden** (errors do not force visibility) — per decision.
- The toggle state is reflected (checked/unchecked) in the rendered sidebar.

### 5. Orchestrator status discoverability

`/apps/feed/admin/status` joins the settings tab nav (see §6) as an admin-only tab, and `feedAdminStatusPage` gets the sidebar wrap. Route unchanged.

### 6. Settings split into four pages (+ admin tabs)

Replace the single `Settings` page with four pages sharing a tab bar.

Routes (registered in `feed.go`):
- `GET /apps/feed/settings` → redirect (303) to `/apps/feed/settings/subscriptions` (default).
- `GET /apps/feed/settings/subscriptions` → `SettingsSubscriptions`
- `GET /apps/feed/settings/bulk-delete` → `SettingsBulkDelete`
- `GET /apps/feed/settings/filters` → `SettingsFilters`
- `GET /apps/feed/settings/playlists` → `SettingsPlaylists`

A shared `feedSettingsTabs(active string, isAdmin bool)` templ renders the tab bar:
`Subscriptions | Bulk Delete | Filters | Playlists` and, when admin, `System | Status`
(System → `/apps/feed/admin/system-settings`, Status → `/apps/feed/admin/status`).

Page contents:
- **Subscriptions** (`SettingsSubscriptions` → `feedSettingsSubscriptionsPage`): add-subscription form, add-folder form, OPML import + export, other-format imports, and the current subscription/folder lists (with their existing per-row links: per-sub settings, delete).
- **Bulk Delete** (`SettingsBulkDelete` → `feedSettingsBulkDeletePage`): folders + subscriptions listed with checkboxes; submit deletes selected. New action `POST /apps/feed/settings/bulk-delete` → `BulkDelete` handler; new store method `DeleteSubscriptions([]int64)` / `DeleteFolders([]int64)` (or loop existing single-delete methods inside one call). PRG: set flash, redirect back.
- **Filters** (`SettingsFilters` → `feedSettingsFiltersPage`): existing filter add (with live `filter-preview`), list, delete.
- **Playlists** (`SettingsPlaylists` → `feedSettingsPlaylistsPage`): existing playlist add, list, delete.

Existing POST actions (`subscriptions`, `folders`, `filters`, `playlists`, their `{id}/delete`, `import`, `opml/import`, `opml/export`) are unchanged; their PRG redirects target the appropriate new settings page (set redirect URLs via `setFlashURL`).

The sidebar "Settings" link points to `/apps/feed/settings` (which redirects to Subscriptions). `buildSidebar` active-path matching for Settings should treat any `/apps/feed/settings*` (and the two admin paths) as the Settings entry being active.

The old `feedSettingsPage` template and `Settings` handler body are removed/replaced.

## Components & boundaries

- `viewCtx` + `parseViewCtx`/`encode`/`label`/`path` + `resolveViewItems` — pure context plumbing in `pages.go`; the one place that maps a context to an item list. Testable without HTTP.
- Context-based neighbor helper — given a resolved list + current id + mode (any|media), returns prev/next. Pure function, unit-testable.
- Settings pages — independent handlers sharing only `feedSettingsTabs`.
- Sidebar filtering — contained in `buildSidebar`; toggle is one pref field.

## Data / schema

No schema migration required: `hide_empty` is another key in the existing `feed_settings` key/value store, read/written with the player prefs.

## Error handling

- `resolveViewItems` returns errors (wrapped) to the handler; on error the item view still renders with a label and disabled prev/next (no crash).
- Bulk delete: collect per-id errors, log them, surface a flash summarizing failures; never swallow (`WE DO NOT SWALLOW ERRORS`).
- `player.js`: `/adjacent` 204 → disable that direction; `/complete` empty body → stop (no autoplay), as today.

## Testing

`internal/cogtest`-style tests in `plugin/feed`:
- `resolveViewItems` returns the same ordering as each corresponding view handler for all/starred/folder/sub/tag/playlist/history.
- Neighbor helper: prev/next at boundaries (first/last), media-only mode skips text, item-absent-from-list yields no neighbor.
- `ItemDetail` renders the context bar with correct label and neighbor links for a representative ctx.
- `buildSidebar` with `hideEmpty=true` omits zero-unread subs/folders including errored ones, and keeps unread ones.
- Settings: `/apps/feed/settings` redirects to `/settings/subscriptions`; each of the four pages renders with the tab bar; admin tabs present only for admin users; bulk delete removes selected subs/folders and redirects with a flash.
- All five sidebar-wrapped pages contain `feed-sidebar` in the response.

Manual: history push/back across player advance; popstate reload; sidebar hide-read toggle refresh.

## Out of scope

- No new player features (speed/skip/etc. unchanged).
- No schema migration / per-view limit changes.
- No restyling beyond reusing existing layout/sidebar/tab CSS.
