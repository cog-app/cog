# DAV fixture corpus

Real iCalendar and vCard output from real clients, for the tests in `DESIGN/dav-storage.md` section 10. Every file is a byte-exact copy of its upstream (`.gitattributes` stops git normalising line endings); provenance, licence and the client that wrote each file are in `ics/SOURCES.md` and `vcf/SOURCES.md`.

| Directory | Contents |
|---|---|
| `ics/<source>/`, `vcf/<source>/` | Output that follows the RFCs, more or less |
| `ics/quirks/`, `vcf/quirks/` | Real output that breaks the RFCs in a way clients actually do; `SOURCES.md` says how |
| `ics/invalid/` | Files that must be rejected; `TestFixturesInvalid` checks they are |
| `ics/cog-production/`, `vcf/cog-production/` | Anonymised samples of the data on cog.duckfam.us |

Only permissively licensed sources are committed (BSD, MIT, Apache-2.0, MPL-2.0). GPL, LGPL and AGPL test data (Radicale, ical4android, vcard4android, Tine 2.0, Roundcube, Nextcloud via calcard) is useful but stays local. Fetch it, pinned by commit in `internal/cmd/davfixtures/gpl-manifest.txt`, with:

```bash
go tool davfixtures gpl          # 326 files into tmp/dav-fixtures-gpl/
```

Personal exports stay local too; only anonymised copies (`*/cog-production/`) are committed:

```bash
COG_USER=jonathan COG_PASS="$(pass show cog.duckfam.us/jonathan | head -n1)" go tool davfixtures export   # into tmp/dav-export/
go tool davfixtures anonymise -out testdata/dav/ics/cog-production -prefix event tmp/dav-export/calendars/jonathan/*/X.ics ...
```

`export` also writes each collection as one file under `tmp/dav-import/`, ready for `/settings/data`. For the production cut-over (`DESIGN/dav-storage.md` section 8.2), `go tool davfixtures tasks -db tasks.db` writes the old server's task lists, which it had no CalDAV for, and `go tool davfixtures compare -got after/ before/` checks a server's export after an import against what was imported.

The round-trip tests pick up `tmp/dav-fixtures-gpl/` and `tmp/dav-export/` when present. Read anonymised output before committing it: the anonymiser only replaces the properties it knows.

## Clients with no real output here yet

iCalendar: iOS/macOS Reminders (VTODO with `X-APPLE-SORT-ORDER`), iOS/macOS Calendar VTODO, Outlook 365 via CalDAV Synchronizer, tasks.org/OpenTasks, Etar or the Android calendar through DAVx5, jtx Board app exports, Evolution 3.x, GNOME Calendar, Fantastical, BusyCal, Fastmail, Proton, Samsung Calendar, new Outlook/OWA.

vCard: iOS 7 and later, macOS Contacts 10.15–14, current Google Contacts export or CardDAV, current Android/Samsung, DAVx5, Outlook 2010 and later/365/Exchange, Thunderbird's built-in address book (102+, vCard 4.0), KAddressBook, a real client writing vCard 4.0 `KIND:group` + `MEMBER`.
