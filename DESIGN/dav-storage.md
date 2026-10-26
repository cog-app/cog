# DAV Storage: Calendars, Tasks and Contacts

Status: proposed. Follows MR !22 (TZID parsing and calendar timezones), which fixed the immediate bug but kept the current storage model.

Delivery: **one MR** covering everything in this document. Section 11 orders the work as milestones within that MR; each milestone leaves the tree building with tests passing, so the branch can be reviewed commit by commit.

## 1. Goals

- **SQL is the source of truth** for events, tasks and contacts. The web UI, agents and DAV clients all read and write the same rows.
- **No client data loss.** Anything Cog doesn't model is kept exactly as received and written back out.
- **iCalendar/vCard text is generated on read**, deterministically, from the rows.
- One shared design for **CalDAV (events, tasks and journals)** and **CardDAV (contacts)**, plus every DAV extension and JSON representation listed in section 9.
- Correct incremental sync, permissions, scheduling and timezones are part of the model rather than added on later.

Non-goals: storing the original bytes of each resource; a general-purpose iCalendar library; VAVAILABILITY (RFC 7953) and the items listed as out of scope in section 9.

## 2. Current state (2026-10)

| Plugin | Storage | DAV |
|---|---|---|
| calendar | `events` row with indexed columns + `ics_data` blob (source of truth) | CalDAV: PUT/GET/DELETE, PROPFIND, multiget, a sync-collection that returns everything |
| tasks | `tasks` row with columns + `ics_data` (often empty; web is the source) | `plugin/tasks/caldav.go` is a 13-line stub |
| contacts | `cards` row with columns + `*_json` arrays + `vcard_data` blob | CardDAV implemented (`carddav.go`, ~700 lines) |

Problems with the blob-plus-index model:

- **Web edits are lossy.** `UpdateEvent` regenerates the whole ICS from form fields, dropping EXDATE, RECURRENCE-ID overrides and unknown properties. Contacts and tasks have the same risk.
- **Two sources of truth.** The columns are derived from the blob by ad hoc parsers. A parser bug means wrong data until a repair runs; see `RepairEventTimes`, added in MR !22.
- **Parsing on read.** RRULE expansion, alarms and the edit modal re-parse `ics_data` on every request.
- **Sync is fake.** `EventsChangedSince` returns every event; clients diff by etag.
- **Partial modelling.** Only the first VEVENT is read, so overrides of single occurrences are invisible to the web UI.
- `.well-known/caldav` and `.well-known/carddav` redirects exist (`internal/compat/stubs.go`).

## 3. Architecture

```
            DAV PUT                                   DAV GET / REPORT
               │                                             ▲
               ▼                                             │
     ┌──────────────────┐                          ┌──────────────────┐
     │ contentline.Parse│                          │ contentline.Write│
     └────────┬─────────┘                          └────────▲─────────┘
              │ generic tree                                │ generic tree
              ▼                                             │
     ┌──────────────────┐   rows + leftovers     ┌──────────────────┐
     │ per-type decoder │ ─────────────────────▶ │ per-type encoder │
     │ (event/task/card)│        SQL             │ (event/task/card)│
     └──────────────────┘   (source of truth)    └──────────────────┘
              ▲                                             │
              │ web forms, agents, JSON API                 │
              └──────────────── same store API ─────────────┘
```

Three layers, each owned by Cog:

1. **`contentline`** (`internal/dav/contentline`): a small generic package for the shared RFC 5545 / RFC 6350 line format.
2. **Per-type models and codecs**: events, tasks and contacts map between the generic tree and SQL rows plus leftovers.
3. **DAV core** (`internal/dav`, SQL helpers in `internal/dav/davsql`): collections, sync, permissions and discovery, shared by CalDAV and CardDAV. The core owns `/remote.php/dav/`, principals, the homes and the collections themselves; plugins implement a backend interface that supplies the resources inside them.

These live under `internal/` because every DAV plugin is in-module; they can move to `plugin/` if an out-of-process plugin ever needs them.

### 3.0 Collections and plugins

**Collections belong to the user, not to a plugin.** The core keeps them in the system database (`dav_collections`, section 4); a plugin supplies the resources of the component types it handles:

| Plugin | Components | Resources live in |
|---|---|---|
| calendar | VEVENT | calendar plugin DB |
| tasks | VTODO, VJOURNAL | tasks plugin DB |
| contacts | VCARD | contacts plugin DB |

Each user has a **calendar home** and an **address book home**. A calendar collection may hold any set of calendar components (RFC 4791 allows it; it is what Nextcloud and SabreDAV do), so the default `personal` calendar holds VEVENT and VTODO and shows up in both the calendar and the tasks UI. A *resource* still holds exactly one component type (plus its VTIMEZONEs), as CalDAV requires, so every resource belongs to exactly one plugin. `supported-calendar-component-set` advertises the collection's set intersected with the components of the enabled backends; MKCALENDAR accepts any set that has a backend for each type (absent means VEVENT and VTODO).

The plugins discover collections through the core (the calendar UI lists collections that include VEVENT, the tasks UI those with VTODO or VJOURNAL) and key their rows by the collection's ID. Names are unique per home by a constraint in one table, so plugins can't collide.

Because a collection's resources can span databases, its sync token is **composite** (section 6). A URI or UID is unique within a collection across backends; the core checks the other backends before a PUT. Two concurrent PUTs of the same new URI with different component types could both succeed; that needs a client racing itself with different content, and is accepted.

Deleting a collection removes its row first (clients discover collections through it, so they never see a half-deleted one), then asks each backend to drop its rows. A backend that fails or is disabled leaves orphans keyed by a collection ID that no longer exists; they are invisible and are removed the next time the backend purges.

Considered and rejected: one collection table per plugin, with single-component collections. Every plugin's default `personal` collided in the shared calendar home, and Nextcloud-style mixed collections, which clients expect, were impossible.

### 3.1 `contentline` (no library)

Deliberately thin, and with no domain types. It only knows syntax.

```go
type Component struct {
	Name       string       // VCALENDAR, VEVENT, VTODO, VALARM, VTIMEZONE, VCARD, ...
	Props      []Property   // in input order
	Components []*Component // nested, in input order
}

type Property struct {
	Group  string  // vCard group: "item1" in "item1.EMAIL"
	Name   string  // as received; compare with EqualFold
	Params []Param // in input order; values unquoted, RFC 6868 carets decoded
	Value  string  // raw value text (escaping is type-specific; codecs decode it)
}

type Param struct {
	Name   string
	Values []string // multi-valued, e.g. MEMBER="a","b"
}
```

- `Parse(r) ([]*Component, error)` handles unfolding, CRLF/LF (and iOS 6's CR CR LF), quoting, multi-valued params, vCard quirks (`TYPE=a,b`, vCard 2.1 nameless params such as `TEL;HOME;VOICE`, quoted-printable soft line breaks, blank lines after base64), UTF-16 input, and preserves order.
- Names keep the spelling they were received with (`X-ABLabel`), so leftovers are written back exactly as sent.
- Real clients send broken lines (Apple's bare `X-APPLE-RADIUS=…`, Outlook's `TZID="Zone:20200609T090000"`). A line that is not valid syntax inside a component is kept as a **malformed property** (`Name == ""`, the whole line in `Value`) and written back verbatim, so it is preserved as a leftover. A property after the last `END` joins the component before it. Unbalanced BEGIN/END and content before the first BEGIN are errors.
- `Write(w, []*Component)` handles folding at 75 octets (not splitting UTF-8 sequences), CRLF, quoted-printable soft breaks for `ENCODING=QUOTED-PRINTABLE` values, and quoting params when needed. Within a component it writes properties before subcomponents, as the RFC grammars require. It is deterministic: the same tree always produces the same bytes.
- Text value escaping (`\,` `\;` `\n`) is applied by codecs per property type, not here, because it depends on the value type (TEXT vs URI vs structured N/ADR).
- **Round-trip property:** `Write(Parse(x))` equals `x` up to line endings, folding, blank lines, redundant parameter quoting and caret escapes, and properties that followed a subcomponent moving ahead of it, for every fixture. `Parse(Write(t))` equals `t` exactly. This is tested before anything else is built on it.

### 3.2 Leftovers

Each modelled component (a VEVENT, VTODO, VALARM, VCARD, or one occurrence override) keeps what the codec didn't claim:

- **Unknown properties**: whole `Property` values, including params, in original order.
- **Unknown parameters on known properties**, e.g. `SUMMARY;LANGUAGE=de`, `ATTENDEE;X-NUM-GUESTS=2`. Stored per modelled property, keyed so they re-attach to the same property on output.
- **Unknown sub-components**, e.g. a vendor component inside VEVENT.

Storage: a `leftovers` column (JSON array of properties and components) on each component row, plus a `params` column (JSON) on child-table rows such as attendees, emails and phones. Encoders write modelled properties first, in a fixed canonical order, then leftovers in their original order.

#### Dependent leftovers

Some extensions duplicate a modelled field. Keeping them after that field is edited means keeping wrong data. Each codec owns an **invalidation table**:

| When this changes | Drop these leftovers |
|---|---|
| LOCATION | `X-APPLE-STRUCTURED-LOCATION` (its `X-TITLE` parameter goes with it) |
| DESCRIPTION | `X-ALT-DESC` (Outlook's HTML copy) |
| DTSTART/DTEND/DUE/DURATION/RRULE, all-day | `X-MICROSOFT-CDO-ALLDAYEVENT`, `X-FUNAMBOL-ALLDAY`, `X-MOZ-GENERATION`, `X-MOZ-LASTACK`, `X-MOZ-SNOOZE-TIME*`, `X-NSCP-ORIGINAL-DTSTART`, `X-NSCP-DTSTART-TZID` |
| an alarm's TRIGGER | that alarm's `X-MOZ-LASTACK`, `X-MOZ-SNOOZE-TIME`, `X-APPLE-DEFAULT-ALARM`, `ACKNOWLEDGED` |
| STATUS (task) | none: no client in the corpus shadows STATUS, COMPLETED or PERCENT-COMPLETE |
| FN/N (contact) | `X-PHONETIC-FIRST-NAME`, `-MIDDLE-NAME`, `-LAST-NAME`, `SORT-STRING`, and N's `SORT-AS` parameter |
| ORG (contact) | `X-PHONETIC-ORG` and ORG's `SORT-AS` parameter |

The tables are code (`ical.dependents`, `vcard.dependents`), grown from fixtures. A web edit that changes a field applies its rows. A DAV PUT replaces the component wholesale, so it doesn't need them.

`Leftovers` and TEXT escaping live in `internal/dav/codec`, which both codecs use; neither codec imports the other. Leftovers are stored as JSON, and `contentline` writes text that is not UTF-8 (a `CHARSET=SHIFT_JIS` value, a Latin-1 export) as base64 there, because `encoding/json` would replace those bytes.

#### Edit bookkeeping

Any change made by Cog (web, agent, API), not a DAV PUT, must bump `SEQUENCE` when scheduling-relevant fields change (times, RRULE, location, attendees), and always update `DTSTAMP` and `LAST-MODIFIED`. Otherwise clients and invite recipients ignore the change.

## 4. Data model

Collections, in the system database:

```
dav_collections(
  id, owner_user_id, home,           -- home: 'calendars' | 'addressbooks'
  name,                              -- path segment, UNIQUE(owner_user_id, home, name)
  components,                        -- 'VEVENT,VTODO', 'VCARD', ...
  display_name, description, color, sort_order,
  timezone,                          -- IANA; floating times and new events
  dead_props,                        -- JSON: PROPPATCHed properties Cog doesn't model
  props_version,                     -- bumped by PROPPATCH; part of getctag
  created_at, updated_at
)
```

Sync state, in every plugin database that holds resources (`internal/dav/davsql` runs the SQL in the caller's transaction):

```
dav_sync(collection_id PRIMARY KEY, token, floor)   -- floor: highest pruned tombstone version
dav_tombstones(collection_id, uri, version, deleted_at, PRIMARY KEY(collection_id, uri))
```

Common to every resource table:

- `collection_id`, the `dav_collections` ID (no foreign key; it is in another database).
- `uid` and `uri` (resource name in the collection; usually `<uid>.ics` or `.vcf`, but clients choose it and it must be kept). Both unique per collection.
- `version INTEGER`, set from the plugin's `dav_sync.token` for the collection on every write (section 6).
- `created_at`, `updated_at`.
- `leftovers TEXT` (JSON).

Times are stored as **local wall time + zone**, never only as a UTC instant, because recurrence and DST depend on local time:

- `*_local TEXT` holds `20261025T090000`, or `20261025` for DATE values.
- `*_tzid TEXT` is the resolved IANA name, `''` for floating, or `'UTC'`.
- `*_utc INTEGER` is a derived instant, indexed for range queries, and recomputed when the zone or the calendar's floating zone changes.
- `*_is_date INTEGER` marks DATE values (all-day).

### 4.1 Timezones

- A TZID that resolves to IANA (directly, via the Windows map, X-LIC-LOCATION, or a path suffix; `tz.Resolve`) is stored as the IANA name, and written back as that name with a generated VTIMEZONE.
- A TZID that doesn't resolve keeps its name, and the client's **VTIMEZONE is stored** per collection in `vtimezones` (`collection_id`, `tzid`, the component as JSON). Its `*_utc` values come from that VTIMEZONE's own rules: `tz.FromRules` expands its STANDARD/DAYLIGHT observances (RRULE, RDATE) to 2100 and builds a `*time.Location` from them (TZif data for `time.LoadLocationFromTZData`), so everything downstream treats it like any other zone. A later PUT may name the zone without defining it again; the stored definition is written back out.
- A TZID with no definition at all reads in the collection's zone and is written back as received, without a VTIMEZONE (as the client sent it).
- Output includes a VTIMEZONE for every non-UTC TZID used, unless the client asked for timezones by reference (RFC 7809: `CalDAV-Timezones: F`; the server advertises `calendar-no-timezone`), in which case only clients' own definitions are sent. For IANA zones `tz.VTimezone` builds the component from Go's zone data, starting at the earliest year the resource uses: transitions are probed up to 2037, runs of the same yearly rule become RRULEs (the last open-ended, so it covers later years), and irregular changes become RDATEs. The output depends only on the zone data, never on the current date. A test generates every common kind of zone (northern and southern DST, half-hour offsets, abolished DST, Morocco's Ramadan changes) and reads it back through `FromRules` hour by hour.

### 4.2 Events (CalDAV, VEVENT)

```
events(                                -- one row per CalDAV resource
  id, collection_id, uid, uri, version,
  size,                                -- length of the text served, for getcontentlength
  color,                               -- Cog's own colour; not part of the iCalendar data
  start_utc, range_end_utc,            -- first instance start, last instance end (NULL = unbounded)
  props, comps,                        -- VCALENDAR leftovers (JSON)
  created_at, updated_at)

vevents(                               -- one row per VEVENT: the master and each override
  id, event_id, position,
  recurrence_id_local, recurrence_id_tzid, recurrence_id_is_date, this_and_future,
  summary, description, location, url, status, class, transp, priority, sequence,
  dtstart_local, dtstart_tzid, dtstart_is_date, dtstart_utc,
  dtend_local, dtend_tzid, dtend_is_date, end_utc, duration,
  rrule, rdates, exdates,              -- RDATE/EXDATE values as JSON, each with its zone and parameters
  dtstamp, created, last_modified, organizer, categories,  -- JSON
  leftovers)                           -- JSON: properties, components, and parameters on modelled properties

event_attendees(id, vevent_id, position, address, email, cn, partstat, role, rsvp, cutype, params)
event_alarms(id, vevent_id, position, action, trigger, related, absolute, repeat, repeat_duration,
             description, summary, leftovers)
vtimezones(collection_id, tzid, vtimezone)
```

The codec is `internal/dav/ical` (`Decode(cal, kind)`, `Object.Encode`); it holds no SQL and models VEVENT, VTODO and VJOURNAL with one `Component` type, claiming per kind only the properties RFC 5545 allows in it. The rows are written and read by `internal/dav/icalsql`, shared with tasks: the component, attendee and alarm tables have the same columns in both plugins (task columns stay empty for events). One resource is one `events` row and its `vevents`, master first.

- **Overrides are whole components**, stored like the master in `vevents`, not as nullable "inherit" columns: RFC 5545 makes an override replace its occurrence, and a property absent from an override is absent from that occurrence. Inheriting would invent data on output.
- **What the codec claims**: a property is modelled only when it fits the model exactly; anything else is a leftover and written back in its original order. So a second SUMMARY, an empty one, a DTSTART Cog cannot read, a CATEGORIES with parameters, or a property with a group stays as received. Parameters Cog does not model on a modelled property are kept with it (`SUMMARY;LANGUAGE=de`, `ATTENDEE;X-NUM-GUESTS=2`, an attendee's whole parameter list in order).
- **Output** is canonical: modelled properties in a fixed order, then leftovers in received order; RDATE and EXDATE values are regrouped by zone and type; text is re-escaped (`Room 1, Floor 2` gains its missing backslash). `TestFixturesEventsRoundTrip` checks, for all 1471 event resources in the corpus (with the GPL set present), that decode(encode(x)) equals x, that encoding is deterministic and a fixed point, and that no property name a client sent is missing.
- **Orphaned overrides** are shown as standalone instances. **Override plus EXDATE**: EXDATE wins for display; both are kept. **RANGE=THISANDFUTURE**: stored; later occurrences take the override's properties and its shift in time. A DATE EXDATE on a timed series removes that day, as some clients mean it.
- **Range queries** use `start_utc < end AND (range_end_utc IS NULL OR range_end_utc >= start)`, then expand in Go (`Object.Instances`). A series with more than 100 000 instances, or none it can count, is unbounded.
- A recurrence rule Cog cannot read leaves the event as its first occurrence; the rule is kept and written back.
- **Web edits** (`applyForm`) change only what the form shows, on the first component: overrides, exceptions, alarms, attendees' other parameters, attendees without a mailto address and all leftovers stay. Changed fields drop their dependent leftovers (section 3.2; `X-ALT-DESC` joined the table for DESCRIPTION), DTSTAMP and LAST-MODIFIED always move, and SEQUENCE is bumped when times, the rule, the location or the attendees changed. Times that did not change keep their TZID. Removing the recurrence drops the overrides and exceptions.
- **Reminders**: `notifications` are derived from the alarms of the first instance that has not ended, on every write.
- ATTACH, GEO, COMMENT, CONTACT, RESOURCES, RELATED-TO, COLOR and CONFERENCE are leftovers for now; managed attachments (milestone 7) will model ATTACH.

### 4.3 Tasks and journals (CalDAV, VTODO and VJOURNAL)

Task lists are calendar collections that include VTODO; the default `personal` calendar is one. Journal entries live in collections that include VJOURNAL. Both are stored by the tasks plugin, with the same layout as events (`internal/dav/icalsql`):

```
tasks(                                 -- one row per CalDAV resource, VTODO or VJOURNAL
  id, collection_id, component, uid, uri, version, size,
  start_utc, range_end_utc, props, comps, created_at, updated_at)

task_components(                       -- same columns as vevents: the master and each override
  id, object_id, position, recurrence_id_*, this_and_future,
  summary, description, location, url, status, class, priority, sequence, percent_complete,
  dtstart_*, due_local, due_tzid, due_is_date, due_utc, end_utc, duration,
  completed, completed_utc,             -- COMPLETED (JSON) and its instant
  rrule, rdates, exdates, dtstamp, created, last_modified, organizer, categories,
  sort_order,                           -- X-APPLE-SORT-ORDER, so modelled, not a leftover
  leftovers)

task_relations(id, component_id, position, reltype, related_uid, is_parent, params)  -- RELATED-TO
task_attendees / task_alarms / vtimezones                                          -- as for events
```

- **What each kind claims.** VTODO adds DUE, COMPLETED, PERCENT-COMPLETE, RELATED-TO and X-APPLE-SORT-ORDER to what events model, and leaves DTEND and TRANSP as leftovers. VJOURNAL models the common properties and RELATED-TO; LOCATION, PRIORITY, a second DESCRIPTION and VALARMs (none of which RFC 5545 allows there, or allows once) are leftovers. RELATED-TO stays a leftover on events.
- **The tree.** `parent_id` is gone. A task's parent is its first `RELATED-TO` with `RELTYPE=PARENT` or no RELTYPE (RFC 5545's default), stored by UID with `is_parent` set, because the parent may arrive later in a sync or live in another list. The web UI resolves it within the list; a parent elsewhere leaves the task a root there, and a cycle sent by a client shows its first task as a root rather than hiding both. Deleting a task in the web UI deletes its subtasks in the same list, each with a tombstone; over DAV a subtask is its own resource and stays.
- **Order.** `X-APPLE-SORT-ORDER` orders a list (tasks without one follow, by creation). Dragging in the web UI writes it, and RELATED-TO when the task changes parent, so DAV clients see the same order and tree; each moved task is a new version.
- **Recurrence.** Occurrences count from DTSTART, or from DUE for a task without one (clients that recur tasks by due date). With both, each occurrence lasts DUE − DTSTART; with DUE only, it is an instant.
- **Completing a recurring task.** RFC 5545 leaves it to clients. Cog follows tasks.org: the series moves on to its next occurrence (`Object.Advance`: DTSTART and DUE move, COUNT counts one less, overrides and exceptions before it go) and stays open; completing the last occurrence completes the task. No fixture from Apple Reminders or tasks.org exists yet (testdata/dav/README.md), so this is from their documented behaviour, not captured traffic. Clients that complete an occurrence with a COMPLETED override (Thunderbird) are stored as sent.
- **Web edits** (`plugin/tasks/edit.go`) change only what the form shows, compared with what it was shown: summary, description, priority, status, percent, due, start, parent. Times are shown and read in the list's zone; a due date (DATE) stays a date unless edited. Changed fields drop their dependent leftovers; DTSTAMP and LAST-MODIFIED always move, SEQUENCE when times or STATUS changed (RFC 5545 §3.8.7.4 lists STATUS for tasks). Moving the start of a recurring task moves its exceptions with it.
- The JSON tasks API (task #32, agent tokens) will go through the same store.

### 4.4 Contacts (CardDAV, VCARD)

```
cards(                                 -- one row per CardDAV resource
  id, collection_id, uid, uri, version, size,
  vcard_version,                       -- '2.1', '3.0' or '4.0', as received
  kind,                                -- KIND (4.0) or X-ADDRESSBOOKSERVER-KIND (3.0)
  fn, n_family, n_given, n_additional, n_prefix, n_suffix,   -- N's lists; NULL without N
  nickname, org, org_units, title, role, note,               -- lists as JSON
  bday, anniversary, gender, rev,      -- as written: their syntax differs by version
  categories,
  photo_hash, photo_type, photo_encoding, photo_params,       -- an inline PHOTO, kept as a file
  is_starred,                          -- Cog's own
  leftovers, created_at, updated_at)

card_emails(card_id, position, value, types, pref, group_name, label, params)
card_phones / card_urls / card_impp / card_related                    -- same columns
card_addresses(card_id, position, pobox, ext, street, locality, region, code, country,
               types, pref, group_name, label, params)
card_members(card_id, position, uri, params)   -- groups: MEMBER (4.0), X-ADDRESSBOOKSERVER-MEMBER (3.0)
```

The codec is `internal/dav/vcard` (`Decode`, `Card.Encode`), beside `ical` and sharing `internal/dav/codec` with it. The rows are written and read in the contacts plugin (`plugin/contacts/rows.go`): `icalsql` is a package because two plugins store calendar components, and only one stores cards. A load is eight queries per batch of cards (the card rows, then one per child table), and the text is generated on read through `dav.Table.Render`, with `size` kept for PROPFIND.

- **addressbook-query** (RFC 6352 §8.6) evaluates the whole filter: prop-filters and param-filters with anyof/allof, is-not-defined, text-match with every match type, negation and the collations i;unicode-casemap (case folding and compatibility decomposition, RFC 5051), i;ascii-casemap and i;octet (advertised in `supported-collation-set`; others fail `supported-collation`), and `limit`/`nresults`, cut short with a 507 and `number-of-matches-within-limits`. The filter (`dav.CardFilter`) is matched against each card as Cog writes it, so it reaches leftovers and parameters too. SQLite cannot compare text as i;unicode-casemap does, so the contacts backend (`dav.CardQuerier`) loads the collection's models in the usual fixed number of queries, without reading photo files, matches them in Go, and renders only the hits.

- **Version**: the card keeps the version it was received in and is written back in it; nothing converts. The version decides escaping, how parameters are written, and the names of KIND and MEMBER. **vCard 2.1** (46 cards in the corpus, from Android, Outlook and BlackBerry exports) is stored and written back as 2.1: values escape only `;`, line breaks travel in quoted-printable, a quoted-printable value is decoded to model it and encoded again on output (the `ENCODING` and `CHARSET` parameters stay with it), and types are bare parameters (`TEL;CELL;PREF`). A value in a charset other than UTF-8, or whose bytes are not UTF-8, is not modelled and stays a leftover byte for byte. CardDAV clients send 3.0 or 4.0; 2.1 arrives by import.
- **What the codec claims**: as for events, a property is modelled only when it fits exactly. FN, N, NICKNAME, ORG, TITLE, ROLE, NOTE, BDAY, ANNIVERSARY, GENDER, REV, CATEGORIES and the photo are claimed once and without a group; EMAIL, TEL, ADR, URL, IMPP and RELATED every time, groups included. A second ORG, a grouped FN, an N with too many components, a 2.1 component ending in a backslash (2.1 cannot escape it) stay leftovers.
- **Values**: N and ADR components are lists (RFC 6350; in 2.1 one value each), so `Richter,James` and `Richter\, James` both survive; the columns hold the list comma-separated with commas in a value escaped. Empty components past the last are dropped (Tine 2.0 adds one). EMAIL, TEL, URL, IMPP and RELATED values are kept as written: they are addresses and URIs, and TEXT escaping a `tel:` URI breaks it. Types are kept as written, in order (`type=INTERNET;type=pref` is written back `TYPE=INTERNET,pref`), and `PREF=n` is modelled.
- **Labels**: Apple writes `item1.EMAIL` + `item1.X-ABLabel:Work`. A child property keeps its group; when the group holds just it and one label without parameters, the label is modelled with it and written right after it. Any other group (two labels, `item2.ADR` + `item2.X-ABADR` + a label, `X-ABRELATEDNAMES` and its label) is left as received, group names included, so iOS keeps its custom labels and the country code stays with its address. A label added in the web UI gets the next free `itemN`.
- **Groups**: KIND:group with MEMBER in 4.0, X-ADDRESSBOOKSERVER-KIND and -MEMBER in 3.0, into `kind` and `card_members`; the other version's spelling is a leftover.
- **Photos**: an inline PHOTO in the version's own form (base64 with ENCODING in 2.1 and 3.0, a `data:` URI in 4.0) is decoded and stored as a file named by the SHA-256 of its bytes (`contacts/<user>/photos/`), with its type, ENCODING token and other parameters (Apple's `X-ABCROP-RECTANGLE`) in columns; it is written back inline. A PHOTO given by an http or https URL (Google exports, FullContact) is kept as written (`photo_uri`); the web UI shows it through Cog, fetched with `internal/safehttp` (public addresses only, checked after resolution and pinned, redirects included) and cached for a day under `u-` and the URL's hash, a stale copy served when the remote fails. Other forms (a `data:` URI in a 3.0 card, a `file:` URL) are leftovers. Photos are served from Cog's origin only when their bytes sniff as a raster image, whatever type the card claims, so a client cannot plant HTML or SVG there.
- **Photo files and transactions**: a card write stores its photo file only once its transaction holds SQLite's write lock (after the version bump), and unused files are removed in a transaction that takes the same lock before checking that no card uses them (`Store.collect`). The two never interleave, in one process or across replicas sharing the database.
- **Web edits** (`plugin/contacts/edit.go`) change only what the form shows, compared with what it was shown: first and last name (N's given and family), display name, organisation (its units stay), title, note, and emails and phones row by row. A changed type swaps that type and drops the label; other types, parameters and groups stay; a removed row goes. FN follows N when it did before. Changed fields drop their dependent leftovers and REV always moves. A new card is 3.0 and gets an N from its display name when it has none. Uploading a photo makes it the card's PHOTO, so DAV clients see it.
- `TestFixturesRoundTrip` (`internal/dav/vcard`) checks, for every card in the corpus (104 committed, 134 with the GPL set), that decode(encode(x)) equals x, that encoding is deterministic and a fixed point, that the version is kept and that no property name or group is lost; `TestFixturesThroughRows` (`plugin/contacts`) PUTs every one and checks the card served back decodes to the same model, through the rows, the JSON and the photo files.

## 5. Generation, ETags and PUT semantics

- **Deterministic output.** Encoders write properties in a fixed canonical order, then leftovers in original order; `contentline.Write` folds deterministically. A test asserts that encoding the same rows twice yields identical bytes.
- **ETag = `"<collection_id>-<version>"`** (opaque to clients). There's no hashing on read, and any write bumps it.
- **PUT responses** (RFC 4791 §5.3.4, RFC 6352 §6.3.2.3): the stored resource is regenerated, so it usually differs from the request bytes. When it does, **don't return an ETag** in the PUT response; clients then re-fetch. The core compares the request, re-written with Cog's folding and line endings, against the generated output. Never return an ETag for bytes we won't serve. A client that PUTs back exactly what it fetched gets one.
- **If-Match / If-None-Match** use the version-based ETag.
- **Caching** (optional, only if profiling shows it's needed): cache generated text keyed by `(resource id, version)`. Not a source of truth; safe to drop.

## 6. Sync (RFC 6578) and change tracking

- Each plugin database keeps a counter per collection (`dav_sync.token`). Every write to a resource sets `version = ++token` in the same transaction, so a change and its version can never be seen apart.
- Deletions insert into `dav_tombstones(collection_id, uri, version, deleted_at)` in the same transaction; a later PUT to the same URI removes the tombstone. Deleting a task deletes its subtasks, and each gets a tombstone; moving a resource to another collection is a delete in one and a create in the other.
- **Composite tokens.** A collection's resources may live in several plugin databases, and no transaction spans them, so the collection's sync token is the list of its backends' counters: `http://cog/ns/sync/v2-e42-t17` (backend keys: `e` events, `t` tasks and journals, `c` contacts). RFC 6578 tokens are opaque, so this is legal. `sync-collection` hands each backend its part (absent means 0, a full sync of that backend) and merges the results. `getctag` is the same string plus `props_version`, so CalendarServer clients also notice PROPPATCH changes. JMAP state strings use the same form.
- A token below a backend's `floor`, above its counter, or not in this format gets a `valid-sync-token` error, which makes clients do a full resync. Old single-number tokens fall in that case.
- **Retention**: tombstones older than the admin setting (default 90 days) are pruned for a collection when a client next syncs it (a read first; a write only if something has expired), raising `floor` to the highest pruned version.
- ETags are per resource, `"<collection_id>-<version>"`. Versions from different backends can coincide, which is harmless: an ETag only has to change when its own resource changes.

## 7. Permissions

- Collections are shared as one resource type, `dav:collection`, whose ID is the collection's ID, with the bitmask from `internal/share` (Read=1, Write=2, Reshare=4, FreeBusy=8, Schedule=16). Sharing `personal` from the calendar UI shares its tasks too: the share is on the collection. Access requires a share whose owner is the collection's owner; the resource ID alone is not enough.
- Report it to clients via `DAV:current-user-privilege-set` (a subset of RFC 3744): `read`, `write`, `write-content`, `bind`, `unbind`, `read-free-busy`, `schedule-*`. Without it clients show read-only shares as editable. The owner has `all`.
- Only the owner may PROPPATCH or delete a collection, or create one in their home.
- Free/busy-only shares expose only `read-free-busy` and answer `free-busy-query` REPORTs (RFC 4791 §7.10); events are not readable.
- Shared collections appear under the sharee's calendar home (as today), with `CS:shared-url` / `invite` properties for clients that show sharing info.

## 8. No migration

The only deployment's data came from one Fossify import and a few hand-made contacts, all of which can be exported and imported again (section 14). So the MR **starts from a fresh schema** rather than migrating: no backfills, no legacy tables or columns, no verification period, and the schema may still change freely until the MR merges.

Deploying: export everything (`go tool davfixtures export`, or the CalDAV/CardDAV of the old server), stop the server, move the calendar, tasks and contacts `app_data` directories aside (not delete; they stay as a backup), start the new version, and import through `/settings/data`. Shares of calendars and address books have to be made again; the old ones name resources that no longer exist. An old database left in place fails to open with a schema error rather than being reused.

### 8.1 Import and export in the web UI

An **Import and export** page in each user's own settings (`/settings/data`, beside Profile, Security and Storage; not the admin-only plugin settings pages), built on the DAV core's `Backend` interface, so it covers every registered backend without per-plugin UI.

- **Import**: upload `.ics` or `.vcf` (size-limited); choose an existing collection or create one. The file is split into resources the way a client would PUT them: VEVENT/VTODO/VJOURNAL grouped by UID (overrides with RECURRENCE-ID stay with their master) together with the VTIMEZONEs they reference, and one resource per VCARD. Each resource goes through `Backend.Put`, so UID/`uri` rules, collection kind checks and leftovers are the same as over DAV. A resource whose UID already exists in the collection replaces it, which makes re-importing the same file idempotent. The result (imported, replaced, skipped, failed with reasons) is reported via flash after a redirect (PRG).
- **Export**: one collection as a single `.ics` (one VCALENDAR, VTIMEZONEs deduplicated) or `.vcf`; or everything as a zip with one file per collection. Generated from rows through the same path as GET, so export and DAV always agree.
- Each resource lands in exactly the collection chosen, whatever backend holds it; a file mixing events and tasks imported into `personal` keeps both. Components with no backend in the collection's set are counted as skipped.
- Lands in milestone 2 against the blob-backed backends and picks up the row-backed codecs as milestones 3–5 replace them. ICS feeds and remote subscriptions (section 9) remain separate.

## 9. Protocol scope

Everything below is in scope for the MR.

### DAV protocols and extensions

| Protocol | Why / notes |
|---|---|
| CalDAV RFC 4791 (VEVENT, VTODO, VJOURNAL) | events, tasks, journals |
| CardDAV RFC 6352 | contacts |
| Service discovery RFC 6764 | `.well-known` exists; add `current-user-principal`/home-set checks; document SRV/TXT records |
| Collection sync RFC 6578 | section 6 |
| ACL subset RFC 3744 | section 7 (`current-user-privilege-set`, `owner`) |
| Extended MKCOL RFC 5689 | CardDAV clients create address books with it |
| Scheduling RFC 6638 | schedule inbox/outbox, server-side iTIP; replaces email-only invites (email stays for external attendees); uses SEQUENCE |
| Free/busy query (RFC 4791 §7.10, RFC 6638 outbox POST) | FreeBusy share permission, scheduling |
| Timezones by reference RFC 7809 | `calendar-timezone-id`; clients may omit VTIMEZONE; `CalDAV-Timezones: F` leaves out standard VTIMEZONEs (section 4.1). No `timezone-service-set`: TZDIST is out of scope |
| Managed attachments RFC 8607 | attachments stored as Cog files instead of inline base64 |
| WebDAV-Push (draft-bitfire-webdav-push; DAVx5) | push instead of polling; subscriptions per collection; Web Push (VAPID) transport |
| CalendarServer sharing extensions (`CS:invite`, `CS:shared-url`) | show and accept shares in Apple clients and DAVx5 |

### Feeds and derived collections

| Feature | Notes |
|---|---|
| ICS feeds (export) | secret, revocable `.ics` URL per calendar for webcal subscribers; free/busy-only variant |
| ICS subscriptions (import) | read-only calendars from remote feeds (school, sports); polled with ETag/If-Modified-Since; `CS:source`; fetched through `internal/safehttp` (block private ranges) |
| Birthday calendar | read-only calendar derived from contacts' BDAY/ANNIVERSARY, regenerated on card changes |

### JSON representations and API

| Spec | Notes |
|---|---|
| JSCalendar RFC 8984 | JSON form of events and tasks, generated from the same rows; used by the web UI and the agent API (task #32) |
| JSContact RFC 9553 | JSON form of contacts, same rules |
| JMAP for Calendars and Contacts (RFC 9610 contacts; calendars draft) | `/jmap` session endpoint, `Calendar/get|set|changes`, `CalendarEvent/*`, `AddressBook/*`, `ContactCard/*`; `changes` uses the same version/tombstone tracking as section 6; push via the same subscription store as WebDAV-Push |

### Journals (VJOURNAL)

- Journal collections (`supported-calendar-component-set` = VJOURNAL), owned by the tasks plugin and stored in the same tables as tasks (section 4.3), modelled without due/completion fields. A collection may hold both, as jtx Board's do.
- Not mapped to the notes plugin (section 12). Round-trip storage and sync, so jtx Board journals work.

### Out of scope

WebDAV locking for CalDAV/CardDAV (file WebDAV has it), GroupDAV, TZDIST RFC 7808, VAVAILABILITY RFC 7953.

## 10. Testing

- **Fixture corpus** in `testdata/dav/`, captured from real clients: iOS/macOS Calendar, Reminders and Contacts; DAVx5 with Etar, jtx Board, tasks.org and the Android contacts app; Thunderbird; Outlook (via CalDAV Synchronizer); Google exports; Nextcloud exports (the import path you used). Include recurring events with overrides, custom VTIMEZONEs, grouped vCard labels, photos, and v3 vs v4 cards.
- **Layer tests**:
  - `contentline`: round-trip every fixture.
  - Codecs: decode, encode, decode is a fixed point; encode is deterministic; leftovers survive.
  - Invalidation: editing a field drops exactly the listed leftovers.
- **Protocol tests** (cogtest): PUT/GET/ETag rules, sync tokens with tombstones, privilege sets on shared collections, free/busy-only shares, scheduling inbox delivery, MKCOL, timezone-by-reference negotiation, managed attachment add/update/remove, push subscription and delivery, ICS feed auth and revocation, subscription polling (with SSRF blocks), birthday calendar regeneration.
- **JSON tests**: JSCalendar/JSContact output validated against the RFC examples; JMAP `get/set/changes` round-trips against the same rows as DAV, and a change made over one protocol is visible over the other.
- **Client smoke test** (manual, per release): DAVx5 (with jtx Board and tasks.org) and Thunderbird against a dev server; edit in Cog, verify on the client, and the reverse.
- **Apple and Microsoft clients are best effort.** No devices are available to capture from, so support rests on the public corpus (Outlook 2003–2016, Exchange/O365, iCal 1–4, macOS 10.8–11, iOS 26, older iOS/macOS Contacts), the RFCs, and leftovers preserving whatever isn't modelled. Newer behaviour (Reminders' `X-APPLE-SORT-ORDER`, iOS 17+ contacts) is fixed as reports come in.

## 11. Milestones (within the one MR)

Each milestone is a run of commits that builds and passes tests on its own.

1. **`contentline` + fixture corpus + round-trip tests.** No behaviour change.
2. **DAV core**: user-level collections, version/tombstone sync with composite tokens, privilege sets, PUT ETag rules, Extended MKCOL. Port the existing CalDAV and CardDAV handlers onto it and mount tasks and journals through it (blob-backed). Fresh schema (section 8). Import and export in user settings (section 8.1).
3. **Events**: model, codec, web UI on rows (fixes lossy edits; overrides visible), VTIMEZONE generation, timezones by reference.
4. **Tasks and journals**: VTODO and VJOURNAL models + CalDAV (replaces the stub); tasks web UI on rows.
5. **Contacts**: model, replacing `vcard_data` and the `*_json` columns.
6. **Scheduling + free/busy**: inbox/outbox, iTIP processing, free/busy queries and the FreeBusy share.
7. **Sharing extensions, managed attachments.**
8. **Feeds**: ICS export, ICS subscriptions, birthday calendar.
9. **WebDAV-Push** (subscription store, VAPID, delivery on version bumps).
10. **JSCalendar/JSContact encoders and JMAP** on the same rows; the agent API (task #32) uses these shapes.
11. **Deploy**: export, fresh start, import (section 8). There are no blob columns to drop: milestones 3–5 replace them in the fresh schema.

## 12. Decisions (were open questions)

- **Leftovers**: JSON columns.
- **VTIMEZONE rules for non-IANA zones**: write a small evaluator (yearly BYMONTH/BYDAY rules plus RDATE), sharing the RRULE expander.
- **Tombstone retention**: 90 days by default, admin-configurable. Each collection records the highest pruned version; a sync token below it gets `valid-sync-token` and the client resyncs.
- **VJOURNAL and notes**: notes are markdown files and don't fit VJOURNAL (several DESCRIPTIONs, DTSTART, STATUS, RELATED-TO, ATTACH). Journals are stored separately, in the tasks plugin, sync-only for now; a notes view of them can come later.
- **JMAP**: contacts (RFC 9610) first; calendars track the latest draft behind a flag.
- **Tasks plugin**: stays separate and also owns journals.
- **Collections**: user-level, in the system database, owned by the DAV core; plugins supply resources by component type, and a calendar collection may mix components (section 3.0). Sync tokens are composite (section 6).
- **Migration**: none. Fresh schema; production data is exported and imported again (section 8).
- **Import and export**: a page in the user's settings (`/settings/data`) over the `Backend` interface, in milestone 2 (section 8.1).
- **Row storage**: one shared package, `internal/dav/icalsql`, for every plugin holding calendar components; the component tables have the same columns everywhere.
- **Completing a recurring task** from Cog advances the series to its next occurrence, as tasks.org does (section 4.3).
- **Contact rows** live in the contacts plugin, not a shared package: only one plugin stores cards (section 4.4).
- **vCard 2.1** is stored and written back as 2.1, with 2.1's escaping and quoted-printable; never converted (section 4.4).
- **Contact photos** are files named by their hash, not Files-plugin files or blobs in the database (section 4.4).
- **addressbook-query** is matched in Go on loaded models, not translated to SQL: the default collation is not expressible in SQLite (section 4.4).
- **Recurrence expansion**: `github.com/teambition/rrule-go`, already a dependency, behind `internal/dav/ical` and `internal/dav/tz`; only RRULE arithmetic, not iCalendar parsing. Two limits are worked around or accepted: it stops about 292 years after DTSTART (its offsets are `time.Duration`s), so `FromRules` starts a 1601-based Outlook observance at 1900 in the same phase; and an event series is not expanded beyond that span. When DTSTART does not match the rule, it is counted as an occurrence on top of COUNT rather than as one of them.

## 13. Open questions

- None yet.

## 14. Progress

Branch `design-dav-storage`, one draft MR to master. Update this section at the end of each milestone.

| Milestone | State |
|---|---|
| 1. contentline + fixtures | Done. `internal/dav/contentline`; `testdata/dav` (see its README); `go tool davfixtures` for the local-only GPL corpus and personal exports. |
| 2. DAV core | Done. `internal/dav` (collections in the system database, `Server` for root/principals/homes, PROPFIND/PROPPATCH/REPORT/MKCALENDAR/extended MKCOL, composite sync tokens, privilege sets, import/export); `internal/dav/davsql` (counters and tombstones in each plugin DB); `internal/dav/tz`. Calendar, tasks (VTODO and VJOURNAL, blob-backed) and contacts are backends; their web UIs read collections from the core. `/settings/data` imports and exports; the tombstone retention is an admin setting. Protocol tests in `internal/dav/protocol_test.go`. |
| 3. Events | Done. `internal/dav/ical` (values, leftovers, VEVENT codec, recurrence with overrides); `internal/dav/tz` generates VTIMEZONEs from Go's zone data and evaluates clients' own; the calendar plugin stores rows (section 4.2) and renders text on read through `dav.Table.Render`; the web UI edits rows; RFC 7809. |
| 4. Tasks and journals | Done. `internal/dav/ical` models VTODO and VJOURNAL with the event codec (one `Component` type, properties claimed per kind); `internal/dav/icalsql` stores calendar objects as rows for both the calendar and the tasks plugin; tasks and journals are rows (section 4.3), with RELATED-TO by UID and X-APPLE-SORT-ORDER; recurring tasks complete by advancing; the tasks web UI edits rows. |
| 5. Contacts | Done. `internal/dav/vcard` models vCard 2.1, 3.0 and 4.0 (labels by group, groups and members, inline photos) with leftovers; `internal/dav/codec` holds what it shares with `ical`; the contacts plugin stores cards as rows (section 4.4), photos as hashed files, and renders text on read; the contacts web UI edits rows. |

How milestone 2 differs from the plan it started with:

- Plugins no longer call `RegisterDAVHandler` (two plugins cannot both register `calendars`). They implement `dav.Provider`; the host injects the `dav.Server` before registration and mounts it, checking per request which plugins are enabled. `RegisterDAVHandler` stays in the public API, unused.
- Collections moved to the system database and may mix components (section 3.0); the default `personal` calendar holds events and tasks. Calendars, task lists and address books in the web UIs are views of these collections; new ones get a name slugged from the display name.
- Shares use one type, `dav:collection`, keyed by collection ID, for every plugin; the web share buttons and the DAV core agree on it.
- A request for an owner's default collection name creates it if the home is empty, as the old handlers did on any request.
- Dead properties are stored per collection (`dead_props`) instead of being dropped; `calendar-order` maps to `sort_order`. `calendar-timezone` with a zone Cog cannot resolve is accepted and ignored until the VTIMEZONE evaluator (milestone 3).
- Calendar and contacts backends share `dav.Table`, the read side for a one-table plugin.

Findings so far:
- Production data (cog.duckfam.us, 2026-10-25): 3 calendars, 186 events, all from one Fossify import, no VTODO over DAV, 9 minimal contacts. No DAV client has written to it yet. Everything can be exported and imported again, so the MR starts from a fresh schema (section 8). One stored DESCRIPTION has a broken fold, kept as a malformed property.
- The old server has no CalDAV for tasks, so tasks made in its web UI cannot be exported over DAV. Check the production task count before deploying; if there are any, export them from the old database first.
- Development needs no production credentials: tests use cogtest and the fixture corpus. To test with real data, run `go tool davfixtures export` and import the result into a dev server.

Adjacent bugs fixed in milestone 2 (each covered by a test):

- Calendar sharing mixed user IDs and usernames (`SharedCalendars`, `CheckCalendarAccess`); the web share button used row IDs and CalDAV used UIDs, so the two never matched.
- Share checks ignored the owner: a share of `personal` by one user granted access to every user's `personal`. Access now requires a share made by the collection's owner.
- CalDAV and CardDAV derived the UID from the request path; resources now have a `uri`, and a UID conflict is a `no-uid-conflict` error.
- DELETE (and PROPPATCH) on a shared collection needed only Write; they are owner-only.
- Shared address books returned 405 to writers.
- CardDAV and CalDAV `sync-collection` returned every resource whatever the token.
- Deleting an event in the web UI left no tombstone; deleting a task left none for its subtasks; moving a task or contact to another list left none in the old one. Moving a contact to another book also left a copy behind.
- Web-made UIDs were `<nanoseconds>-<summary>`, leaking the summary and allowing `/`; they are UUIDs.
- Swallowed errors: `MarkNotificationFired`, every plugin's `renderWidget`, invitation emails, contact photo updates, `UpsertCard`/`DeleteCardByID`/`DeleteAddressBook` in the contacts pages, list queries in PROPFIND/REPORT, gallery's contact search, `registry.Add` at startup.
- `SeedPlugins` ignored its username and always seeded `admin`.
- `webtest` closed the shared browser when an engine closed and never relaunched it, so every browser test after the first in a package skipped silently. With it fixed, about thirty tests in `internal/web` and `cmd/cog` run for the first time; their DOM goldens are committed. `TestBrowserVolumeBrowse` had live file times in its golden (fixed); the feed settings tests were stale after the settings split (fixed).

How milestone 3 differs from the plan:

- Overrides are full rows in `vevents` beside the master, not an `event_overrides` table of nullable inherited columns (section 4.2). Attendees and alarms hang off `vevents`, so an override's own attendees and alarms need no `override_id`.
- RDATE, EXDATE, DTSTAMP/CREATED/LAST-MODIFIED, ORGANIZER and CATEGORIES are JSON columns on `vevents`, not child tables: nothing queries them on their own, and loading stays at five queries per batch of events. Attendees and alarms are tables, for scheduling and reminders.
- ATTACH stays a leftover until managed attachments (milestone 7); `event_attachments` is not created yet.
- Clients' VTIMEZONEs are stored per collection (`vtimezones`), keyed by TZID.
- `events.size` keeps the length of the generated text, so PROPFIND without calendar-data need not render.
- Invitations are the stored event with METHOD (and STATUS:CANCELLED for a cancellation), VTIMEZONEs included, instead of a separately built UTC-only copy.

Adjacent bugs fixed in milestone 3:

- A web edit regenerated the whole event from the form: overrides, EXDATEs, unknown properties, attendee parameters and attendees without a mailto address were lost, and SEQUENCE never changed, so clients and invitees ignored edits.
- Overrides (RECURRENCE-ID) were invisible in the web UI; only the first VEVENT was read.
- `UpcomingEvents` and the dashboard widget never showed the next occurrence of a series that started in the past.
- Range queries matched every event whose text contained `RRULE:`.
- Accepting or declining an invitation changed a side table only, so the PARTSTAT never reached the event or DAV clients.
- Reminders came only from the first instance of a series, even when it was over.
- `RRulePreview` ignored the error from writing its response.
- `webtest` snapshots could capture a page that had not finished loading: the element a test waited for can appear long before the rest of the document on a busy machine, which is what made `cmd/cog` `TestBrowserCore/Navigation` differ between runs (a truncated dashboard, no htmx style). `Snapshot` now waits for the load event and fails if it never comes.
- Gallery dereferenced the files plugin without checking it was registered (thumbnails, renames, seeding), so a server without it panicked on every thumbnail. The seed also ignored its count query's error.

Open after milestone 3:

- REPORT `calendar-data` with `expand` or `limit-recurrence-set` (RFC 4791 §9.6.5–6) is left out by decision: the clients Cog targets do not use them. Clients that ask get the stored series.

After milestone 3:

- **Editing one occurrence.** Clicking an instance of a series opens it with "Apply to": *this event* creates or updates its override (a copy of the master without its recurrence, then the form); *this and following* splits the series (`Object.Split`: the old one ends with an UNTIL just before, the new one, with a new UID, carries the rest of a COUNT, the later overrides, exceptions and extra dates, and the form, moved by however much the occurrence was moved); *all events* edits the master and moves it, its exceptions and its overrides' recurrence IDs by as much as the occurrence moved. Deleting offers the same: an EXDATE (and the override goes), an UNTIL, or the whole event. A split is saved in one transaction. Only a whole series may move to another calendar.
- **Redefined zones.** A PUT whose VTIMEZONE for a client's own TZID is new or differs from the stored one recomputes, in the same transaction, the derived times and reminders of the other events in the calendar that use that TZID.
- **Browser tests under load.** At most `WEBTEST_PARALLEL` test binaries (default a quarter of the CPUs) run Chrome at once, by lock files in the temp directory; the action timeout is 30s (`WEBTEST_TIMEOUT`). `Login` and `Count` now wait for the page load: `TestBrowserCore/Dashboard`'s old "6 app cards" was a count of a page that had not finished arriving.

How milestone 4 differs from the plan:

- No separate `task_exdates` / `task_overrides` tables: tasks use the event layout (overrides as full component rows, RDATE/EXDATE as JSON). Rather than copying the calendar's row code, it moved to `internal/dav/icalsql`, and the calendar's tables took the shared column names (`object_id`, `component_id`, a `component` column, and the task columns, empty for events).
- Tasks and journals share one resource table (`tasks.component` says which), so UIDs are unique across both in a collection, relations can link a journal entry to a task (jtx Board does), and `dav.Table` serves both (a `Component` of "" reads the column).
- `ical.Event` became `ical.Component` (with `Kind`), `Object.Events` `Object.Components`, `Object.Comps` `Object.Others`, `DecodeEvents` `Decode(cal, kind)`; the calendar's `stamp` is `Component.Stamp`.
- A PUT may change a URI's component type (a task replaced by a journal entry); before, the tasks backend refused it. Nothing in RFC 4791 forbids it.
- Tasks have no reminders yet (the old plugin had none); their alarms are stored and served.
- Tasks do not narrow `calendar-query` time ranges in SQL (RFC 4791 §9.9's VTODO rules are not implemented); clients get every task and filter.

Adjacent bugs fixed in milestone 4:

- Dragging a task into another task's group in the web UI only reordered it: the parent the client sent was ignored, so the task jumped back on reload. Reordering now sets the parent.
- Reordering and reparenting did not change a task's version, so DAV clients never saw them.
- The edit form read due and start times as UTC whatever the user's zone, and wrote every time as UTC; they are shown and read in the list's zone, and a due date stays a date.
- A web edit regenerated the whole VTODO from the form, losing alarms, recurrence, relations, sort order and everything Cog did not model, and never bumped SEQUENCE.
- Due badges for date-only tasks used the server's zone and could show the previous day; an all-day task was overdue from midnight of its due day.
- Seeding logged and skipped failed subtasks instead of reporting the error.

Open after milestone 4:

- Completing a recurring task in Apple Reminders reportedly also keeps a completed copy as a separate reminder; Cog does not. Capture fixtures from Reminders and tasks.org to confirm both behaviours.
- ~~`DESIGN/database.md` still describes the old `ics_data` blobs for events and tasks.~~ It points to this document since milestone 5.

How milestone 5 differs from the plan:

- The rows live in the contacts plugin, not a sibling of `icalsql` (section 4.4).
- `photo_file_id` became `photo_hash`: photos are files under the plugin's data directory named by their SHA-256, as `photo_path` was, not files in the user's Files. Their type, ENCODING token and other parameters are columns.
- Child rows carry `position` and `group_name` besides the planned columns; ADR components and N are lists (comma-separated in their columns), NICKNAME, ORG's units and CATEGORIES JSON. `card_related` holds RELATED (4.0); Apple's `X-ABRELATEDNAMES` stays a leftover with its label.
- BDAY, ANNIVERSARY and GENDER are stored as written, not parsed: their syntax differs by version (`--0203`, `2012-06-06`, `19950415`). The birthday calendar (milestone 8) will read them.
- vCard 2.1 is modelled rather than left whole: it is stored and written back as 2.1 (section 4.4).
- The edit form gained first and last name fields (N), so a name edited in Cog reaches clients that show N, as iOS does.
- `dav.Table` lost its `Data` mode: no table stores resource text any more.

Adjacent bugs fixed in milestone 5:

- A web edit regenerated the whole card as 3.0 from the form: a 4.0 card became 3.0, and labels, groups, addresses (the form did not show them), photos, every other property and every parameter were lost. REV was the only bookkeeping, and it stayed.
- A photo uploaded in the web UI, or set from a gallery face, was only a file beside the card: DAV clients never saw it, and the card's own PHOTO was ignored by the web UI.
- The old parser took the first TYPE only, read ROLE as the title, split ADR on escaped semicolons, kept ORG's separators in the organisation's name, did not unfold files with bare LF line ends, decoded no 2.1 quoted-printable value, and did not read N or NICKNAME at all.
- The gallery's contact picker linked avatars to `/apps/contacts/api/photos/{id}`, which does not exist; it is `/apps/contacts/photos/{id}`.
- Toggling a contact's star answered 404 for any error, a database failure included.
- `cogtest.Env.Do` ignored `StopRedirect` (only the form helpers honoured it).

Resolved after milestone 5 (listed as open in its first status):

- `addressbook-query` returned every card; its filters are evaluated (section 4.4). `address-data` with a `version` attribute is still answered with the stored version, by decision: Cog does not convert.
- A photo given by URL was a leftover the web UI could not show; it is modelled and fetched through `internal/safehttp` (section 4.4). The safe dialer moved there from `internal/federation`, so ICS subscriptions (milestone 8) can use it.
- Photo garbage collection relied on a mutex in the process; it uses SQLite's write lock (section 4.4), so replicas sharing a database are safe.
- Found on the way: photos from DAV clients were served with the type the card claimed, so a card could put HTML on Cog's origin; only bytes that sniff as an image are served.

Fixed after milestone 2 (pre-existing, found through the `webtest` fix):

- `TestBrowserAdminCreateVolume`'s golden showed the run's temp directory. `webtest` now masks the per-test temp root (`«tmp»`), an environment value like the CSRF token.
- `cmd/cog` `TestBrowserCore/Dashboard` and `/Navigation` were skipped as failing during milestone 2 (6 app cards; DOM differing between runs). Re-run against the fixed `webtest` they pass ten runs out of ten with 8 cards and identical DOM; the failures did not reproduce, so they are unskipped with fresh goldens. `Snapshot` now logs when a page does not settle instead of discarding the error, so a recurrence says why.
- Gallery's `idx_media_fingerprint` was never created on new databases: the guarded migration that adds it is skipped once `schema.sql` has the column. `migration007` creates it unguarded; `TestFreshSchemaHasMigrations` checks every column and index the migrations add reaches a fresh database. `people.contact_uid` is now in `schema.sql` as well.
- Gallery recognition dropped database errors (storing tags and faces, marking media done or failed); a failed write now ends the pass with the error.
- Gallery's clustering tests and the SSH tests ran on shared-cache in-memory databases, which CLAUDE.md rules out; they use files in the test's temp directory.
