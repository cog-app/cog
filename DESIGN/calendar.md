# Calendar Plugin Design

The Calendar plugin is a core system plugin (`id: calendar`) that provides full-featured scheduling, recurrence support, and NextCloud-compatible CalDAV access.

## 1. Data Model

The plugin manages multiple calendars per user, each containing a collection of events.

- **Calendars:** The primary container.
    - Properties: `name`, `color` (hex), `description`, `order`.
    - Resource Type: `calendar:calendar`.
- **Events:** Individual entries within a calendar.
    - Properties: `summary`, `description`, `location`, `start_time`, `end_time`, `is_all_day`, `status`.
    - **Recurrence:** Supports complex recurrence rules (RRULE) via the iCalendar (RFC 5545) standard.
    - **Exceptions:** Supports individual instance overrides or deletions for recurring series.
- **Reminders:** Optional notifications for events.

## 2. CalDAV Compatibility

The plugin implements the CalDAV (RFC 4791) and iCalendar standards to ensure compatibility with NextCloud apps, iOS/Android calendars, and Thunderbird.

- **Discovery:** Responds to `PROPFIND` on `remote.php/dav/calendars/{username}/`.
- **Principal URL:** `/remote.php/dav/principals/users/{username}/`.
- **Data Format:** Events are stored as raw iCalendar (.ics) blobs in the database to ensure no data loss during sync, with indexed columns for fast querying.

## 3. CalDAV Provider

The Calendar plugin registers itself as a `CalDAVProvider` with the Host. It enumerates all of a user's calendars as DAV collections.

- **Enumeration:** For each user, the plugin returns a list of collections corresponding to their rows in the `calendars` table.
- **Handler:** Provides the standard DAV operations (`PROPFIND`, `GET`, `PUT`, `DELETE`, `REPORT`) for `VEVENT` components within those collections.

## 4. Sharing & Resources

Calendars are registered as Cog **Resources**, allowing users to share an entire calendar with other users or groups.

- **Resource ID:** The UUID of the calendar.
- **Permissions:** 
    - `READ`: View events.
    - `WRITE`: Add/Edit/Delete events.
    - `SHARE`: Manage calendar sharing.
- **Implicit Access:** Sharing a calendar implicitly grants access to all events within it.

## 4. UI & Frontend

- **Pages:** Provides a full-month/week/day view using a lightweight JS-based calendar component (e.g., FullCalendar) integrated via HTMX.
- **Actions:** 
    - `POST /apps/calendar/event`: Create/Update event.
    - `DELETE /apps/calendar/event/{id}`: Remove event.
- **Templates:** Uses `RegisterTemplPage` to render the initial calendar state and settings using direct SQLite queries for performance.

## 5. Search Indexing

The Calendar plugin registers a `SearchProvider` to make events discoverable via the global search bar.
- **Enrichment Query:** Fetches `summary` as the name and `start_time` + `location` as the subtext.
- **Indexing:** Maps `description` and `summary` as searchable text fields in Bleve.
