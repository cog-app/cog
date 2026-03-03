# Tasks Plugin Design

The Tasks plugin (`id: tasks`) provides a modern, hierarchical task management system that integrates with the Calendar via hooks to expose lists as CalDAV (VTODO) collections.

## 1. Data Model

The Tasks plugin stores its data in `app_data/tasks/plugin.db`.

- **Task Lists:** Containers for tasks.
    - Properties: `id`, `name`, `color`, `order`.
    - Resource Type: `tasks:list`.
- **Tasks:** Individual items within a list.
    - Properties: `summary`, `description`, `priority`, `percent_complete`, `completed_at`, `due_at`, `start_at`.
    - **Hierarchy:** Supports subtasks via a `parent_id` column.
    - **Sync Data:** Stores raw iCalendar (RFC 5545) `VTODO` blobs for lossless CalDAV sync.

## 2. Calendar & CalDAV Integration

### CalDAV Exposure
The Tasks plugin registers as a `CalDAVProvider` directly with the Host.

- **Enumeration:** For each user, the plugin returns its task lists as DAV collections.
- **Collection URI:** `/remote.php/dav/calendars/{username}/tasks-{list_id}/`
- **Component Type:** `VTODO`.
- **Handler:** The Tasks plugin implements a DAV handler interface to manage `PROPFIND`, `PUT`, `GET`, `REPORT`, and `DELETE` requests for `VTODO` objects.

### Calendar View Integration (UI Only)
- **Hook: `calendar:get_events`**: The Tasks plugin provides a callback to the `calendar` plugin (if active), allowing it to display tasks with due dates directly on the user's calendar grid.

## 3. Sharing & Resources

Task lists are Cog **Resources**, enabling granular sharing.
- **Implicit Access:** Sharing a Task List implicitly grants access to all tasks and subtasks within it.
- **Permissions:** Standard `READ`, `WRITE`, and `SHARE` bitmasks.

## 4. UI & Frontend

- **Pages:** A dedicated task dashboard with drag-and-drop ordering and hierarchical views.
- **Actions:** 
    - `POST /apps/tasks/task`: Create/Update task.
    - `POST /apps/tasks/toggle`: Mark a task as complete/incomplete.
    - `DELETE /apps/tasks/task/{id}`: Remove task.
- **Templates:** Uses `RegisterTemplPage` to render task lists and detail views using direct SQLite queries for high performance.

## 5. Search Indexing

The Tasks plugin registers a `SearchProvider` to make tasks discoverable.
- **Enrichment Query:** Fetches `summary` as the name and `due_at` + `status` as the subtext.
- **Indexing:** Maps `description` and `summary` as searchable text fields in Bleve.
- **Change Detection:** Uses `sqlite3_update_hook` to trigger re-indexing on any task change.
