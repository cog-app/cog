# Tasks

The Tasks app provides task management with CalDAV VTODO integration.

## Features

- **Task lists** -- organize tasks into separate lists with names and colors
- **Subtasks** -- create hierarchical task trees
- **Due dates** -- set deadlines on tasks
- **Priorities** -- mark task importance
- **Progress tracking** -- track percentage complete
- **Drag-and-drop ordering** -- reorder tasks visually

## CalDAV integration

Task lists are exposed as CalDAV VTODO collections, compatible with:

- **Thunderbird** (Tasks)
- **Apple Reminders** (macOS/iOS)
- **DAVx5 + OpenTasks** (Android)
- **GNOME To Do**

### CalDAV URL

Task lists appear alongside calendars at:

```
/remote.php/dav/calendars/<username>/tasks-<list_id>/
```

Tasks are stored as iCalendar VTODO objects for lossless sync.

## Calendar view

Tasks with due dates can appear on the calendar grid (if the Calendar app is enabled), giving you a unified view of events and deadlines.

## Sharing

Task lists can be shared with users or groups:

- **Read** -- view tasks
- **Write** -- add, edit, and complete tasks
- **Reshare** -- manage sharing

Sharing a task list grants access to all tasks and subtasks within it.

## Search

Tasks are indexed for full-text search. Search by task summary or description.
