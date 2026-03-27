# Calendar

The Calendar app provides event management with full CalDAV compatibility.

## Features

- Multiple calendars per user with custom names and colors
- Day, week, and month views
- Recurring events with recurrence rules (RFC 5545 RRULE)
- Event exceptions (modify or delete individual occurrences)
- Reminders
- All-day events
- Event location and description

## CalDAV

The calendar is fully accessible via CalDAV (RFC 4791), making it compatible with:

- **Thunderbird** (built-in calendar)
- **Apple Calendar** (macOS/iOS)
- **DAVx5** (Android)
- **GNOME Calendar**
- **NextCloud desktop/mobile clients**

### CalDAV URLs

- Discovery: `/.well-known/caldav`
- Principal: `/remote.php/dav/principals/users/<username>/`
- Calendars: `/remote.php/dav/calendars/<username>/`

Events are stored as iCalendar (.ics) blobs in the database for lossless sync with CalDAV clients.

## Sharing

Calendars can be shared with users or groups:

- **Read** -- view events
- **Write** -- add, edit, and delete events
- **Reshare** -- manage sharing

Sharing a calendar grants access to all events within it.

## Search

Calendar events are indexed for full-text search. Search by event summary, description, or location.
