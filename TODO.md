These are tasks for CLAUDE to complete in priority order. Be sure to check the ./DESIGN/ documents too and update plans if you get more information from the dev. Remove them when you're done.

- Files
  - Drag-and-drop should work anywhere on the main area of the page (perhaps the whole page). Pure JS implementation. If the drop is on a folder, upload inside of it.
  - The file input already uses the Upload component but drag-and-drop needs the same integration.
  - Allow editing plain text documents.
  - Allow renaming files/folders.
  - Allow bulk file operations.
  - Preview images where their icons are
  - Generate preview PDF for complex documents (MS Office/Libreoffice)
- Plugins
  - We should have generic migrator code for Go plugins to share. They all do it a little differently.
- Dashboard; these plugins should register widgets.
  - News: Recent articles
  - Calendar: Upcoming events
  - Files: Recent updates
  - Contacts: Starred contacts
  - Dashboard: User's Groups
