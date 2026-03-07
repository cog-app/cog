package tasks

// CalDAV VTODO handler — not mounted yet.
// Integration with the calendar plugin's calendar-home-set discovery is a follow-up.
// When mounted, task lists will appear as CalDAV collections at:
//   /remote.php/dav/calendars/{username}/tasks-{list_uid}/
// with supported-calendar-component-set = VTODO.

// TODO: Implement CalDAVHandler struct with:
// - PROPFIND: enumerate task lists with VTODO component type
// - PUT/GET/DELETE on individual VTODOs
// - REPORT: calendar-multiget, calendar-query (VTODO filter), sync-collection
// - ETag via MD5 of ICS data, CTag/SyncToken on lists
