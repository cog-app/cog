# RSS / News

The RSS app provides a feed reader for subscribing to and reading RSS and Atom feeds.

## Features

- **Feed subscriptions** -- add RSS/Atom feed URLs
- **Folders** -- organize feeds into categories
- **Read/unread tracking** -- mark articles as read individually or in bulk
- **Starred articles** -- save important articles
- **OPML import/export** -- import and export feed subscriptions

## Adding feeds

1. Go to the RSS app
2. Click "Add Feed"
3. Enter the RSS or Atom feed URL
4. Optionally assign it to a folder

## Background polling

Feeds are polled automatically in the background. The poller uses conditional HTTP requests (ETags and Last-Modified) to minimize bandwidth. Failed feeds are retried with exponential backoff.

## NextCloud News API

The RSS app implements the [NextCloud News API](https://github.com/nextcloud/news/blob/master/docs/api/api-v1-2.md) for compatibility with News reader apps:

- **News API endpoint:** `/index.php/apps/news/api/v1-2/`

Compatible clients include:

- NextCloud News (Android/iOS)
- Newsboat (with NextCloud News plugin)
- FeedMe (Android)

## Import/Export

Import and export feed subscriptions in OPML format.

## Search

RSS articles are indexed for full-text search. Search by article title or content.
