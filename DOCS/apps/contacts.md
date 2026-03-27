# Contacts

The Contacts app provides address book management with full CardDAV compatibility.

## Features

- Multiple address books per user
- vCard storage with all standard fields (name, email, phone, address, etc.)
- Import and export vCards
- Duplicate detection
- Contact search

## CardDAV

The contacts app implements CardDAV (RFC 6352) for sync with:

- **Apple Contacts** (macOS/iOS)
- **DAVx5** (Android)
- **Thunderbird** (with CardBook or built-in)
- **GNOME Contacts**
- **NextCloud desktop/mobile clients**

### CardDAV URLs

- Discovery: `/.well-known/carddav`
- Principal: `/remote.php/dav/principals/users/<username>/`
- Address books: `/remote.php/dav/addressbooks/<username>/`

Sync uses incremental sync tokens -- clients only fetch changes since their last sync.

## Sharing

Address books can be shared with users or groups:

- **Read** -- view contacts
- **Write** -- add, edit, and delete contacts
- **Reshare** -- manage sharing

## Import/Export

Import and export contacts in vCard format from the web UI.

## Search

Contacts are indexed for full-text search. Search by name, email, phone number, or other vCard fields.
