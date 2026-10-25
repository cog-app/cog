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

1. **`contentline`**: a small generic package for the shared RFC 5545 / RFC 6350 line format.
2. **Per-type models and codecs**: events, tasks and contacts map between the generic tree and SQL rows plus leftovers.
3. **DAV core**: collections, sync, permissions and discovery, shared by CalDAV and CardDAV.

### 3.1 `contentline` (no library)

Deliberately thin, and with no domain types. It only knows syntax.

```go
type Component struct {
	Name       string       // VCALENDAR, VEVENT, VTODO, VALARM, VTIMEZONE, VCARD, ...
	Props      []Property   // in input order
	Components []*Component // nested, in input order
}

type Property struct {
	Name   string  // upper-cased
	Params []Param // in input order; values unescaped and unquoted
	Value  string  // raw value text (escaping is type-specific; codecs decode it)
}

type Param struct {
	Name   string
	Values []string // multi-valued, e.g. MEMBER="a","b"
}
```

- `Parse(r) ([]*Component, error)` handles unfolding, CRLF/LF, quoting, multi-valued params, vCard 3/4 quirks (`TYPE=a,b`, unparameterised `ENCODING=b`), and preserves order.
- `Write(w, []*Component)` handles folding at 75 octets (not runes), CRLF, and quoting params when needed. It is deterministic: the same tree always produces the same bytes.
- Text value escaping (`\,` `\;` `\n`) is applied by codecs per property type, not here, because it depends on the value type (TEXT vs URI vs structured N/ADR).
- **Round-trip property:** `Write(Parse(x))` equals `x` up to line endings and folding, for every fixture. This is tested before anything else is built on it.

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
| LOCATION | `X-APPLE-STRUCTURED-LOCATION`, `X-TITLE` param |
| DTSTART/DTEND/DURATION/RRULE | `X-MICROSOFT-CDO-*` time copies, `X-MOZ-GENERATION` |
| an alarm's TRIGGER | that alarm's `X-MOZ-LASTACK`, `X-MOZ-SNOOZE-TIME`, `X-APPLE-DEFAULT-ALARM`, `ACKNOWLEDGED` |
| STATUS (task) | `X-APPLE-COMPLETED`-style shadows (check fixtures) |
| FN/N (contact) | `X-PHONETIC-*` |

The table is code, grown from fixtures. A web edit that changes a field applies its rows. A DAV PUT replaces the component wholesale, so it doesn't need them.

#### Edit bookkeeping

Any change made by Cog (web, agent, API), not a DAV PUT, must bump `SEQUENCE` when scheduling-relevant fields change (times, RRULE, location, attendees), and always update `DTSTAMP` and `LAST-MODIFIED`. Otherwise clients and invite recipients ignore the change.

## 4. Data model

Common to every resource table:

- `uid` and `uri` (resource name in the collection; usually `<uid>.ics` or `.vcf`, but clients choose it and it must be kept).
- `version INTEGER`, a per-collection monotonic counter set from the collection's `sync_token` on every write (section 6).
- `created_at`, `updated_at`.
- `leftovers TEXT` (JSON).

Times are stored as **local wall time + zone**, never only as a UTC instant, because recurrence and DST depend on local time:

- `*_local TEXT` holds `20261025T090000`, or `20261025` for DATE values.
- `*_tzid TEXT` is the resolved IANA name, `''` for floating, or `'UTC'`.
- `*_utc INTEGER` is a derived instant, indexed for range queries, and recomputed when the zone or the calendar's floating zone changes.
- `*_is_date INTEGER` marks DATE values (all-day).

### 4.1 Timezones

- A TZID that resolves to IANA (directly, via a Windows map, X-LIC-LOCATION, or a path suffix; see `resolveTZID` in `plugin/calendar/tz.go`) is stored as the IANA name.
- A TZID that doesn't resolve keeps its **original VTIMEZONE text** in a `vtimezones` table (`collection_id`, `tzid`, `vtimezone`), so its meaning survives and it can be written back out. Its `*_utc` values are computed from that VTIMEZONE's STANDARD/DAYLIGHT rules (needs a small rule evaluator) or, until that exists, from the collection's zone, with a warning.
- Output includes a VTIMEZONE for every non-UTC TZID used, unless the client asked for timezones by reference (RFC 7809). For IANA zones, generate VTIMEZONE from Go's zone data: probe transitions per year and emit STANDARD/DAYLIGHT with RRULEs where the pattern is regular. This replaces MR !22's TZID-without-VTIMEZONE output.

### 4.2 Events (CalDAV, VEVENT)

```
calendars(id, uid, name, color, description, timezone, sort_order, sync_token, ctag, ...)

events(
  id, calendar_id, uid, uri, version,
  summary, description, location, url, status, class, transp, priority,
  dtstart_local, dtstart_tzid, dtstart_utc, dtstart_is_date,
  dtend_local,   dtend_tzid,   dtend_utc,          -- or duration
  duration,                                        -- ISO 8601, when DTEND absent
  rrule,                                           -- canonical RRULE text
  sequence, dtstamp, created, last_modified,
  organizer_email, organizer_params,
  color, categories_json,
  leftovers,
  range_end_utc   -- last occurrence end, or NULL for infinite; for range queries
)

event_rdates(event_id, value_local, tzid, is_date, is_period, period_end_local)
event_exdates(event_id, value_local, tzid, is_date)

event_overrides(                                   -- RECURRENCE-ID components
  id, event_id, recurrence_id_local, recurrence_id_tzid, range_this_and_future,
  <same field set as events, all nullable = inherit>,
  sequence, leftovers
)

event_attendees(id, event_id, override_id NULL, email, cn, partstat, role, rsvp, cutype, params)
event_alarms(id, event_id, override_id NULL, action, trigger, trigger_related, trigger_abs_utc,
             repeat, duration, description, summary, attach, leftovers)
event_attachments(id, event_id, override_id NULL, uri, fmttype, filename, managed_id, params)
```

One CalDAV resource is one `events` row plus its overrides. Rules to settle when implementing:

- **Orphaned overrides** (a RECURRENCE-ID that no longer matches the RRULE after an edit): RFC 5545 allows them; keep them, and display them as standalone instances.
- **Override plus EXDATE on the same instance**: EXDATE wins for display; keep both for round-trip.
- **RANGE=THISANDFUTURE**: store the flag. Web "edit this and future" should instead split the series (end the old RRULE with UNTIL and create a new event); that is what most clients do.
- Range queries use `dtstart_utc < end AND (range_end_utc IS NULL OR range_end_utc > start)`, then expand in Go. This replaces the current `ics_data LIKE '%RRULE:%'`.

### 4.3 Tasks (CalDAV, VTODO)

Same collection model as calendars (task lists are calendar collections with `supported-calendar-component-set` = VTODO).

```
task_lists(id, uid, name, color, timezone, sort_order, sync_token, ctag, ...)

tasks(
  id, list_id, uid, uri, version,
  summary, description, location, url, status, priority, percent_complete, class,
  dtstart_local, dtstart_tzid, dtstart_utc, dtstart_is_date,
  due_local,     due_tzid,     due_utc,     due_is_date,
  completed_utc,
  rrule, sequence, dtstamp, created, last_modified,
  categories_json,
  sort_order,          -- maps to X-APPLE-SORT-ORDER, so modelled, not a leftover
  leftovers
)

task_relations(task_id, reltype, related_uid, params)  -- RELATED-TO; PARENT drives the tree
task_exdates / task_overrides / task_alarms            -- same shapes as events
```

- Today's `parent_id` becomes a view over `RELATED-TO;RELTYPE=PARENT`. Store the UID, not the row id, because the parent may arrive later in a sync, or may live in another list.
- Completing a recurring task follows RFC 5545 (the client usually creates a COMPLETED override or advances DTSTART). Mirror what Apple Reminders and tasks.org send; capture fixtures.
- The existing tasks web UI and the JSON tasks API (task #32, agent tokens) both go through this store.

### 4.4 Contacts (CardDAV, VCARD)

```
address_books(id, uid, name, description, sync_token, ctag, ...)

cards(
  id, address_book_id, uid, uri, version, vcard_version,  -- '3.0' or '4.0'
  fn, n_family, n_given, n_additional, n_prefix, n_suffix, nickname,
  org, org_units, title, role, note, bday, anniversary, gender,
  kind,                 -- individual/group/org/location (v4) or X-ADDRESSBOOKSERVER-KIND (v3)
  photo_file_id,        -- photo stored as a Cog file; inline data or URI regenerated
  rev, categories_json, is_starred,
  leftovers
)

card_emails(card_id, value, types, pref, label, params)
card_phones(card_id, value, types, pref, label, params)
card_addresses(card_id, pobox, ext, street, locality, region, code, country, types, pref, label, params)
card_urls / card_impp / card_related                  -- same shape
card_members(card_id, member_uri)                     -- groups (v4 MEMBER, v3 X-ADDRESSBOOKSERVER-MEMBER)
```

- **Labels**: Apple writes `item1.EMAIL` + `item1.X-ABLabel:Work`. Property groups (`item1.`) must be modelled: `contentline` keeps the group prefix, and the codec pairs `X-ABLabel` with its sibling into `label`. Losing the group breaks custom labels in iOS.
- **Version**: store the version received and write the same one back. Clients that sent 3.0 expect 3.0. Never convert silently.
- **Photos**: decoded into the files plugin (as `photo_path` does today); output inlines them for v3 and uses `data:` URIs for v4.
- Migrate today's `*_json` columns into child tables.

## 5. Generation, ETags and PUT semantics

- **Deterministic output.** Encoders write properties in a fixed canonical order, then leftovers in original order; `contentline.Write` folds deterministically. A test asserts that encoding the same rows twice yields identical bytes.
- **ETag = `"<collection_id>-<version>"`** (opaque to clients). There's no hashing on read, and any write bumps it.
- **PUT responses** (RFC 4791 §5.3.4, RFC 6352 §6.3.2.3): the stored resource is regenerated, so it usually differs from the request bytes. When it does, **don't return an ETag** in the PUT response; clients then re-fetch. Compare a byte-normalised request (unfolded, CRLF) against the generated output to decide. Never return an ETag for bytes we won't serve.
- **If-Match / If-None-Match** use the version-based ETag.
- **Caching** (optional, only if profiling shows it's needed): cache generated text keyed by `(resource id, version)`. Not a source of truth; safe to drop.

## 6. Sync (RFC 6578) and change tracking

- Every collection has `sync_token` (monotonic). Every write to a resource sets `version = ++sync_token` in the same transaction.
- Deletions insert into `tombstones(collection_id, uri, version)`.
- `sync-collection` with token *t* returns resources with `version > t`, plus tombstones with `version > t`. Tokens older than the tombstone retention window get `valid-sync-token` errors, which make clients do a full resync.
- `getctag` (CalendarServer) = `sync_token`.
- This replaces `EventsChangedSince`, `deleted_events`, `deleted_tasks` and `deleted_cards` with one shared mechanism in the DAV core.

## 7. Permissions

- Collections carry the share bitmask from `internal/share` (Read=1, Write=2, Reshare=4, FreeBusy=8, Schedule=16).
- Report it to clients via `DAV:current-user-privilege-set` (a subset of RFC 3744): `read`, `write`, `write-content`, `bind`, `unbind`, `read-free-busy`, `schedule-*`. Without it clients show read-only shares as editable.
- Free/busy-only shares expose only `read-free-busy` and answer `free-busy-query` REPORTs (RFC 4791 §7.10); events are not readable.
- Shared collections appear under the sharee's calendar home (as today), with `CS:shared-url` / `invite` properties for clients that show sharing info.

## 8. Migration

Per plugin, behind the same "repair once per database" mechanism MR !22 introduced (`meta` table + `repairOnce`), since plugin migrations are SQL-only:

1. SQL migration adds the new tables and columns; the old blob columns stay.
2. On first open, decode each existing blob into rows + leftovers. Record failures (bad ICS) in a `migration_errors` table and keep the blob for those rows.
3. **Verification period**: writes go to rows; the blob column is regenerated from rows on every write. A background check (or test fixture run) compares `encode(rows)` with the original blob, normalised, and logs differences.
4. When the comparison is clean on real data, a later migration drops the blob columns.

Order: calendar events first (most data, most bugs), then tasks (needs CalDAV anyway), then contacts (already works; lowest urgency).

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
| Timezones by reference RFC 7809 | `calendar-timezone-id`; clients may omit VTIMEZONE; pairs with section 4.1 |
| Managed attachments RFC 8607 | attachments stored as Cog files instead of inline base64 |
| WebDAV-Push (draft-bitfire-webdav-push; DAVx5) | push instead of polling; subscriptions per collection; Web Push (VAPID) transport |
| CalendarServer sharing extensions (`CS:invite`, `CS:shared-url`) | show and accept shares in Apple clients and DAVx5 |

### Feeds and derived collections

| Feature | Notes |
|---|---|
| ICS feeds (export) | secret, revocable `.ics` URL per calendar for webcal subscribers; free/busy-only variant |
| ICS subscriptions (import) | read-only calendars from remote feeds (school, sports); polled with ETag/If-Modified-Since; `CS:source`; SSRF rules as for remote volumes (block private ranges) |
| Birthday calendar | read-only calendar derived from contacts' BDAY/ANNIVERSARY, regenerated on card changes |

### JSON representations and API

| Spec | Notes |
|---|---|
| JSCalendar RFC 8984 | JSON form of events and tasks, generated from the same rows; used by the web UI and the agent API (task #32) |
| JSContact RFC 9553 | JSON form of contacts, same rules |
| JMAP for Calendars and Contacts (RFC 9610 contacts; calendars draft) | `/jmap` session endpoint, `Calendar/get|set|changes`, `CalendarEvent/*`, `AddressBook/*`, `ContactCard/*`; `changes` uses the same version/tombstone tracking as section 6; push via the same subscription store as WebDAV-Push |

### Journals (VJOURNAL)

- Journal collections (`supported-calendar-component-set` = VJOURNAL), modelled like tasks without due/status-completion fields.
- Mapping to the notes plugin: open question (section 12). The minimum is round-trip storage and sync, so jtx Board journals work.

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
- **Client smoke test** (manual, per release): DAVx5 and iOS against a dev server; edit in Cog, verify on the device, and the reverse.

## 11. Milestones (within the one MR)

Each milestone is a run of commits that builds and passes tests on its own.

1. **`contentline` + fixture corpus + round-trip tests.** No behaviour change.
2. **DAV core**: shared collections, version/tombstone sync, privilege sets, PUT ETag rules, Extended MKCOL. Port the existing CalDAV and CardDAV handlers onto it.
3. **Events**: model, codec, migration with verification, web UI on rows (fixes lossy edits; overrides visible), VTIMEZONE generation, timezones by reference.
4. **Tasks and journals**: VTODO and VJOURNAL models + CalDAV (replaces the stub); tasks web UI on rows.
5. **Contacts**: model + migration from `vcard_data` and `*_json`.
6. **Scheduling + free/busy**: inbox/outbox, iTIP processing, free/busy queries and the FreeBusy share.
7. **Sharing extensions, managed attachments.**
8. **Feeds**: ICS export, ICS subscriptions, birthday calendar.
9. **WebDAV-Push** (subscription store, VAPID, delivery on version bumps).
10. **JSCalendar/JSContact encoders and JMAP** on the same rows; the agent API (task #32) uses these shapes.
11. **Drop blob columns** once verification (section 8) is clean on real data. This can trail the MR as a follow-up migration if production verification needs time.

## 12. Open questions

- Leftovers as JSON columns vs a generic `props(owner_kind, owner_id, ord, name, params, value)` table. JSON is simpler and keeps order; a table is queryable. Start with JSON.
- VTIMEZONE rule evaluation for non-IANA zones: write one, or accept collection-zone fallback with a warning?
- Tombstone retention window (30 days? 1000 versions?).
- VJOURNAL ↔ notes plugin: store journals separately and only sync, or back notes with VJOURNAL?
- JMAP calendars is still a draft: track its latest version, or ship contacts (RFC 9610) first and calendars behind a flag?
- Does the tasks plugin merge into the calendar plugin (one CalDAV home, VTODO collections alongside VEVENT ones), or stay a separate plugin sharing the DAV core? Clients see one calendar home per user either way.
