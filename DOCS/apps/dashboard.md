# Dashboard

The Dashboard is the landing page when you log into Cog. It displays an overview of your apps and recent activity.

## App grid

The top of the dashboard shows a grid of cards linking to each enabled app (Files, Calendar, Notes, etc.). Disabled apps do not appear.

## Widgets

Below the app grid, plugins can contribute widgets showing relevant information:

- **Recent Files** -- your most recently modified files
- **Upcoming Events** -- next calendar events
- **Recent Bookmarks** -- recently added bookmarks
- Other plugins may add their own widgets

Widgets are ordered by priority (set by each plugin) and render server-side on each page load.

## Plugin management

Admins can enable or disable plugins from **Admin > Plugins**. The Dashboard and Files plugins are protected and always enabled.
