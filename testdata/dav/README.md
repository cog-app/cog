# DAV fixture corpus

Real iCalendar and vCard output from real clients, for the tests in `DESIGN/dav-storage.md` section 10. Every file is a byte-exact copy of its upstream (`.gitattributes` stops git normalising line endings); provenance, licence and the client that wrote each file are in `ics/SOURCES.md` and `vcf/SOURCES.md`.

| Directory | Contents |
|---|---|
| `ics/<source>/`, `vcf/<source>/` | Output that follows the RFCs, more or less |
| `ics/quirks/`, `vcf/quirks/` | Real output that breaks the RFCs in a way clients actually do; `SOURCES.md` says how |
| `ics/invalid/` | Files that must be rejected; `TestFixturesInvalid` checks they are |

Only permissively licensed sources are committed (BSD, MIT, Apache-2.0, MPL-2.0). GPL, LGPL and AGPL test data (Radicale, ical4android, vcard4android, Tine 2.0, Roundcube, Nextcloud via calcard) is useful but stays local: put it in `tmp/dav-fixtures-gpl/{ics,vcf}/` and the round-trip tests pick it up. Personal exports go in `tmp/dav-export/` the same way; only anonymised copies are committed.

## Clients with no real output here yet

iCalendar: iOS/macOS Reminders (VTODO with `X-APPLE-SORT-ORDER`), iOS/macOS Calendar VTODO, Outlook 365 via CalDAV Synchronizer, tasks.org/OpenTasks, Etar or the Android calendar through DAVx5, jtx Board app exports, Evolution 3.x, GNOME Calendar, Fantastical, BusyCal, Fastmail, Proton, Samsung Calendar, new Outlook/OWA.

vCard: iOS 7 and later, macOS Contacts 10.15–14, current Google Contacts export or CardDAV, current Android/Samsung, DAVx5, Outlook 2010 and later/365/Exchange, Thunderbird's built-in address book (102+, vCard 4.0), KAddressBook, a real client writing vCard 4.0 `KIND:group` + `MEMBER`.
