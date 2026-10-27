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

Each calendar home also holds the user's **schedule inbox and outbox** (RFC 6638) at `inbox/` and `outbox/`; the names are reserved, so no calendar can take them. They are not rows in `dav_collections` (no UI lists them as calendars): the inbox is collection ID 0 (`dav.InboxID`, which `AUTOINCREMENT` never hands out), and its messages live in the calendar plugin's database (section 4.2). The outbox has no resources; it answers free/busy requests.

Some collections are **filled by Cog** rather than written by clients (`Collection.Kind`): a subscription to a remote feed, and the user's birthday calendar (section 9.1). Their resources are rows in the backend that stores events like any calendar's, so the web UI, sync, feeds and sharing need nothing special; they are **read-only to every client, the owner included** (`Collection.ReadOnly`): the privilege set has no write, PUT, DELETE and attachment POSTs are refused, import skips them, and invitations never land in them. The owner may still rename, recolour, share and delete them.

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
  created_at, updated_at,
  kind                               -- '' (written by clients), 'subscription' or 'birthdays': filled by Cog, read-only
)

dav_subscriptions(                   -- a subscription's feed (section 9.1)
  collection_id PRIMARY KEY,         -- ON DELETE CASCADE
  url,                               -- as the user gave it, webcal:// included; served as CS:source
  etag, last_modified,               -- validators of the last response, for conditional fetches
  refresh,                           -- seconds the feed asks for (REFRESH-INTERVAL, X-PUBLISHED-TTL); 0 = default
  next_poll_at, last_poll_at, last_success_at,
  failures, last_error,              -- consecutive failures and why the last one failed, shown in the UI
  skipped)                           -- components of the last fetch a subscription does not hold

dav_birthday_cursors(collection_id, book_id, version)   -- how far the birthday calendar has read each address book

push_subscriptions(                  -- a client's Web Push endpoint and what it watches (sections 9.2, 9.3)
  id PRIMARY KEY,                    -- random; the registration URL ends with it
  user_id,                           -- who registered it; ON DELETE CASCADE
  protocol,                          -- 'webdav' or 'jmap'
  resource,                          -- what it watches: 'collection/<id>', 'inbox/<user id>', 'home/<user id>/<home>'; JMAP: 'user/<user id>'
  endpoint, p256dh, auth,            -- the push resource and its RFC 8291 keys; UNIQUE(protocol, resource, endpoint)
  options,                           -- the protocol's own, JSON: WebDAV's triggers; JMAP's device id, types and verification
  expires_at,
  pending, seq,                      -- bits owed (content, property) and a counter of marks, so a push clears only what it carried
  next_attempt_at, failures, last_error, last_success_at)
```

Sync state, in every plugin database that holds resources (`internal/dav/davsql` runs the SQL in the caller's transaction):

```
dav_sync(collection_id PRIMARY KEY, token, floor, modified)   -- floor: highest pruned tombstone version; modified: last write, for a feed's Last-Modified
dav_tombstones(collection_id, uri, version, deleted_at, PRIMARY KEY(collection_id, uri))
```

Common to every resource table:

- `collection_id`, the `dav_collections` ID (no foreign key; it is in another database).
- `uid` and `uri` (resource name in the collection; usually `<uid>.ics` or `.vcf`, but clients choose it and it must be kept). Both unique per collection.
- `version INTEGER`, set from the plugin's `dav_sync.token` for the collection on every write (section 6).
- `created_version INTEGER`, the version of the write that created the row, which a replacing write keeps: JMAP's `/changes` tells records created since a state from those only updated (section 9.3).
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
- Output includes a VTIMEZONE for every non-UTC TZID used, unless the client asked for timezones by reference (RFC 7809: `CalDAV-Timezones: F`; the server advertises `calendar-no-timezone`), in which case only clients' own definitions are sent. For IANA zones `tz.VTimezone` builds the component from Go's zone data, starting at the earliest year the resource uses: transitions are probed up to 2087 (past the last irregular change the tz database predicts, so the output is the same whether the zone data lists those years or stops at 2037), runs of the same yearly rule become RRULEs (the last open-ended, so it covers later years), and irregular changes become RDATEs. The output depends only on the zone data, never on the current date. A test generates every common kind of zone (northern and southern DST, half-hour offsets, abolished DST, Morocco's Ramadan changes) and reads it back through `FromRules` hour by hour.

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
event_attachments(id, vevent_id, position, value, inline, managed_id, fmttype, size, filename, params)
managed_attachments(managed_id, hash, size, fmttype, filename, created_at)   -- this user's RFC 8607 files
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
- **Scheduling** (RFC 6638, `internal/dav/itip` and `internal/dav/schedule.go`): a write to an event with an ORGANIZER and attendees, over DAV or from the web UI, is compared with the copy it replaces. When the calendar's owner is the organizer, each attendee the server schedules for (SCHEDULE-AGENT absent or SERVER) gets a REQUEST when they are new or what they are invited to changed in a way that matters (times, recurrence, text, status, who attends, SEQUENCE), or SCHEDULE-FORCE-SEND asks; a CANCEL when they were removed, everything they are invited to was cancelled, or the event was deleted. Each attendee gets their own view: the master and the overrides listing them, with an EXDATE for an override that leaves them out (the series ends before a RANGE=THISANDFUTURE one), without the organizer's alarms or the scheduling parameters. When the owner is an attendee, a PARTSTAT change on any occurrence is a REPLY to the organizer, and deleting the event declines it (unless `Schedule-Reply: F`).
- **Delivery**: a recipient who is a Cog user (by email, ignoring case, or by principal URL) has the message applied to their calendar at once and kept in their inbox: a REQUEST replaces their copy (unless its SEQUENCE is older) or lands in their default calendar, keeping their own alarms and TRANSP; a CANCEL of the whole event marks every component cancelled, so they see it; of occurrences, excludes them (truncating the series for THISANDFUTURE); a REPLY records the sender's PARTSTAT on the occurrences answered, creating an override for one that had none. Anyone else gets the message by email (iMIP) when SMTP is configured. The outcome is recorded on the stored copy as SCHEDULE-STATUS before it is stored (1.2 delivered, 1.1 emailed, 3.7 no address Cog can use, 3.8 no permission, 5.1 no email configured); a client's own SCHEDULE-STATUS gives way to the server's, and SCHEDULE-FORCE-SEND is never stored. Messages are sent after the write commits; a delivery that fails is logged, since the change is already stored.
- **Inbox**: `schedule_inbox` in the calendar database holds each message as the text Cog wrote, versioned with `davsql` under collection 0, so the inbox syncs like a calendar. Clients delete what they have seen; messages older than the tombstone retention are dropped on the next delivery, since a web-only user never deletes any.
- **Web**: inviting someone makes the user the ORGANIZER (as `mailto:` their email, or their principal URL without one); the event modal shows Yes/Maybe/No to an attendee of someone else's event, which sets their PARTSTAT on every occurrence and replies.
- **Free/busy**: busy time comes from instances (`Object.Instances`): TRANSP:TRANSPARENT, STATUS:CANCELLED and instances the owner declined are free; STATUS:TENTATIVE and instances the owner has not answered or accepted tentatively are BUSY-TENTATIVE; an all-day instance is busy for its days in the calendar's zone. Periods of one type that overlap or touch are merged and clipped to the range.
- **Reminders**: `notifications` are derived from the alarms of the first instance that has not ended, on every write.
- **Attachments** (`event_attachments`): every ATTACH on an event is modelled, in one of three forms. A URI (Apple's old dropbox links, Google Drive links) and inline data (`VALUE=BINARY;ENCODING=BASE64`, kept as the base64 text sent) are written back as received; a **managed attachment** (RFC 8607) is a URI Cog serves, with its MANAGED-ID, FMTTYPE, SIZE and FILENAME. An ATTACH that does not fit (no value, a SIZE that is not a number, inline data in another encoding or with a MANAGED-ID) stays a leftover. Tasks and journals keep ATTACH among their leftovers.
- **Managed attachments** (RFC 8607, `internal/dav/attach.go`): a POST to an event with `?action=attachment-add`, `-update` or `-remove` (and `rid=M,<RECURRENCE-ID>…` to name instances, an occurrence without an override getting one) changes its ATTACH, bumps SEQUENCE and is scheduled like any other change, so attendees get it. Only the event's organizer (or its owner, for an event without one) may; a sharee needs Write. The response carries `Cal-Managed-ID`, the new ETag, and the event with `Prefer: return=representation`. An update gets a new MANAGED-ID and URL. Preconditions are RFC 8607's (`valid-action`, `valid-rid`, `valid-managed-id`, `max-attachment-size` 25 MB, `max-attachments-per-resource` 20, `DAV:quota-not-exceeded`).
- **Attachment files**: the bytes are files under the calendar plugin's data directory (`calendar/<user>/attachments/`), named by their SHA-256, like contact photos; `managed_attachments` maps each MANAGED-ID to its file. The file and row are written in the transaction that stores the event, once it holds the write lock; a row no ATTACH names any more is removed after the write, and its file when no row uses it, under the same lock (`collectAttachments`). They count toward the user's quota with their Files (`plugin.StorageUser`; contact photos count too).
- **Serving**: `/remote.php/dav/attachments/<owner>/<managed-id>`, absolute from the configured base URL; the home's `managed-attachments-server-URL` is empty, so clients behind another host name substitute their own. The owner, anyone the event's calendar is shared with for reading, and the attendees of a component carrying it may fetch it; it is sent as a download with `Content-Security-Policy: sandbox`, never rendered on Cog's origin. An attachment that is gone answers 410. It cannot be PUT or DELETEd.
- **Attachments and scheduling**: a Cog attendee's copy names the organizer's attachment by its URL and MANAGED-ID, and may be written back with it; their access follows the invitation. An external attendee's email carries the URL with a token signed for their address, valid while they are an attendee of a component carrying it, and no MANAGED-ID.
- **A PUT naming a managed attachment** (RFC 8607 section 3.7) is accepted for the owner's own, with the URL and SIZE corrected from what is stored, and for one the stored copy already had (an invitation's); any other is `valid-managed-id-parameter`. Import and export drop MANAGED-ID (section 3.12.7), leaving the URL.
- GEO, COMMENT, CONTACT, RESOURCES, RELATED-TO, COLOR and CONFERENCE are leftovers.

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
- Agents (task #32) and the planned mobile API reach tasks through `dav.Session`, the same store (section 9.3).

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
- **Scheduling on PUT and DELETE**: an event write reads the copy it replaces to work out what it sends (section 4.2). When the client sent no If-Match or If-None-Match, the write is made conditional on that copy and retried if another write came between, so the messages always describe the change that was made. Recording SCHEDULE-STATUS changes the stored copy, so such a PUT returns no ETag and the client fetches it again, as RFC 6638 expects.
- **Caching** (optional, only if profiling shows it's needed): cache generated text keyed by `(resource id, version)`. Not a source of truth; safe to drop.

## 6. Sync (RFC 6578) and change tracking

- Each plugin database keeps a counter per collection (`dav_sync.token`). Every write to a resource sets `version = ++token` in the same transaction, so a change and its version can never be seen apart.
- Deletions insert into `dav_tombstones(collection_id, uri, version, deleted_at)` in the same transaction; a later PUT to the same URI removes the tombstone. Deleting a task deletes its subtasks, and each gets a tombstone; moving a resource to another collection is a delete in one and a create in the other.
- **Composite tokens.** A collection's resources may live in several plugin databases, and no transaction spans them, so the collection's sync token is the list of its backends' counters: `http://cog/ns/sync/v2-e42-t17` (backend keys: `e` events, `t` tasks and journals, `c` contacts). RFC 6578 tokens are opaque, so this is legal. `sync-collection` hands each backend its part (absent means 0, a full sync of that backend) and merges the results. `getctag` is the same string plus `props_version`, so CalendarServer clients also notice PROPPATCH changes. JMAP state strings use the same form, per collection of an account and for one backend: `v1:12-c42,15-c3` (section 9.3).
- A token below a backend's `floor`, above its counter, or not in this format gets a `valid-sync-token` error, which makes clients do a full resync. Old single-number tokens fall in that case.
- **Retention**: tombstones older than the admin setting (default 90 days) are pruned for a collection when a client next syncs it (a read first; a write only if something has expired), raising `floor` to the highest pruned version.
- ETags are per resource, `"<collection_id>-<version>"`. Versions from different backends can coincide, which is harmless: an ETag only has to change when its own resource changes.

## 7. Permissions

- Collections are shared as one resource type, `dav:collection`, whose ID is the collection's ID, with the bitmask from `internal/share` (Read=1, Write=2, Reshare=4, FreeBusy=8, Schedule=16). Sharing `personal` from the calendar UI shares its tasks too: the share is on the collection. Access requires a share whose owner is the collection's owner; the resource ID alone is not enough.
- Report it to clients via `DAV:current-user-privilege-set` (a subset of RFC 3744): `read`, `write`, `write-content`, `bind`, `unbind`, `read-free-busy`, `schedule-*`. Without it clients show read-only shares as editable. The owner has `all`.
- Only the owner may PROPPATCH or delete a collection, or create one in their home.
- Free/busy-only shares expose only `read-free-busy` and answer `free-busy-query` REPORTs (RFC 4791 §7.10); events are not readable.
- **Push subscriptions** (section 9.2) need read access to what they watch: the owner, or a share with Read. A free/busy-only share has no push properties and cannot subscribe. A sharee's subscription is dropped at the first change after their access ends.
- **Read-only collections** (a subscription, the birthday calendar; section 3.0): the owner has `read`, `write-properties`, `read-acl`, `read-current-user-privilege-set` and `read-free-busy`, not `all`; a sharee's Write grants nothing there, and Schedule nothing with it. They are transparent for free/busy unless the owner marks them opaque.
- **Feeds** are public link shares (`PUBLIC` with a token) of a `dav:collection`, made by its owner: Read serves the calendar, FreeBusy only its busy times (section 9.1). The share panel lists them as "Feed" and revoking one there revokes the feed.
- **Free/busy through the outbox** (RFC 6638 §5): a user may see the busy time of all their own calendars, and of someone else's calendars those that person shared with them with FreeBusy or Read; for anyone else the answer is `3.8;No authority`, and for an address that is not a Cog user `3.7`. A calendar whose owner set `schedule-calendar-transp` to transparent never makes them busy there.
- **Schedule** (bit 16) lets a sharee act as the owner in scheduling: a write they make in the owner's calendar sends invitations and replies in the owner's name, as the owner's own write would, and the collection reports `schedule-send`, `schedule-send-invite` and `schedule-send-reply`. Without it, a sharee's write is stored but sends nothing, and the attendees (or the organizer) it would have reached get SCHEDULE-STATUS 3.8, so the client can see why. Anyone may invite any Cog user; delivery needs no share. The share panel's presets are Read-only, Read/Write, Free/Busy only and Delegate (Read, Write, FreeBusy and Schedule).
- The owner's inbox reports `read`, `unbind` and `schedule-deliver` (with `-invite`, `-reply` and `schedule-query-freebusy`); the outbox `read` and `schedule-send` (with `-invite`, `-reply` and `-freebusy`). Nobody else may open either.
- Shared collections appear under the sharee's calendar home (as today), with `CS:shared-url` / `invite` properties for clients that show sharing info.
- **CalendarServer sharing** (`internal/dav/sharing.go`, `calendarserver-sharing` in DAV): the owner's collection lists its sharees in `CS:invite` (by mailto address or principal, with `CS:read` or `CS:read-write`) and is `CS:shared-owner` once shared; `CS:allowed-sharing-modes` says it `can-be-shared`. A sharee sees `CS:shared`, `CS:shared-url` (the owner's collection URL, where shared collections already live) and `CS:invite` with the owner as `CS:organizer` and only their own entry. A `CS:share` POST by the owner sets or removes user shares, resolving `mailto:` addresses and principal URLs to Cog users; it changes the same `dav:collection` rows the web share panel makes (`read` is Read, `read-write` Read and Write; FreeBusy is kept, and Schedule while the share can write), so either side sees what the other made, and is applied whole or not at all.
- **Shares take effect at once**, as they always have from the web UI: there are no pending invitations or notification collection, and `CS:invite` reports every sharee as `invite-accepted`. A `CS:invite-reply` POSTed to the sharee's home is accepted for clients that send one: accepting answers `CS:shared-as`; declining removes the sharee's own share. Group, link and everyone shares, and free/busy-only shares, are not listed in `CS:invite`, which has no way to say them; they work as before.

## 8. No migration

The only deployment's data came from one Fossify import and a few hand-made contacts, all of which can be exported and imported again (section 14). So the MR **starts from a fresh schema** rather than migrating: no backfills, no legacy tables or columns, no verification period, and the schema may still change freely until the MR merges.

Deploying: export everything (`go tool davfixtures export`, or the CalDAV/CardDAV of the old server), stop the server, move the calendar, tasks and contacts `app_data` directories aside (not delete; they stay as a backup), start the new version, and import through `/settings/data`. Shares of calendars and address books have to be made again; the old ones name resources that no longer exist. An old database left in place fails to open with a schema error rather than being reused: `migrate` runs `schema.sql` in one transaction, so the failed attempt leaves the old file byte for byte as it was, and reports `migrate.ErrIncompatible` ("the database was made by an incompatible version of Cog; move it aside…"). The server itself starts, since the system database migrates as usual; requests touching the old database answer 500 with that error in the log. Section 8.2 is the step-by-step version.

### 8.2 Cut-over runbook (cog.duckfam.us)

Rehearsed in milestone 11 against a server built from `master` (section 14). Not yet run against production.

**Merging is the cut-over.** CI's `trigger_deploy` deploys every commit on `master` (to `jonathan/home.duckfam.us`), so the new version starts on the old data the moment the MR merges. Steps 1–3 happen before merging; steps 4–6 right after the deploy starts. Until step 5, calendar, tasks and contacts answer 500 (the old databases are refused, unchanged); everything else works.

Paths below are inside the deployment: `$COG_DB` is the system database, `$COG_DATA_DIR` the data directory, `<user>` a username.

1. **Pre-flight (old server running, read only).** For each user with DAV data, against `$COG_DATA_DIR/<user>/app_data/*/plugin.db` (`sqlite3 -readonly`):
   - tasks: `SELECT count(*) FROM tasks;` The old server has no CalDAV for tasks; if this is not 0, step 3 exports them.
   - contacts: `SELECT count(*) FROM cards WHERE photo_path != '';` and `SELECT fn FROM cards WHERE is_starred = 1;` Photos uploaded in the old web UI and stars lived beside the card, not in it, so no export carries them. Note them; re-add by hand after step 5.
   - calendar: `SELECT name, color FROM calendars;` and `SELECT count(*) FROM events WHERE color != '';` Imported collections get the name typed in step 5 and the default colour; note names and colours to set again. Per-event colours set in the old web UI are not in the exported text.
   - shares: `sqlite3 -readonly "$COG_DB" "SELECT resource_type, resource_id, subject_type, subject_id, permissions FROM shares WHERE resource_type IN ('calendar', 'address_book');"` These are the shares to make again in step 6.
2. **Export over DAV** (from a workstation, old server running): `COG_USER=<user> COG_PASS=… go tool davfixtures export -out tmp/dav-export -import tmp/dav-import`. It prints a count per collection. `tmp/dav-export` has one file per resource, byte for byte; `tmp/dav-import/<home>/<user>/<collection>.{ics,vcf}` has each collection as one file, ready to upload.
3. **Tasks** (only if step 1 counted any): copy the tasks database off the server (`sqlite3 … ".backup tasks.db"` while it runs, or the file after step 4), then `go tool davfixtures tasks -db tasks.db -import tmp/dav-import`: one `.ics` per list under `tmp/dav-import/tasks/`, built from the columns (the old web UI's source of truth: `ics_data` had neither the parent nor the order), with RELATED-TO for subtasks and X-APPLE-SORT-ORDER for the order.
4. **Merge; when the deploy starts, stop Cog and move aside** — never delete — for every user: `$COG_DATA_DIR/<user>/app_data/{calendar,tasks,contacts}` to a backup location outside `$COG_DATA_DIR` (with their `-wal` and `-shm` files, which live in the same directories). Start Cog, open the calendar, tasks and contacts pages once as each user, and check the log has no `incompatible version` error (the databases are opened on first use, not at start); if it has, a directory was missed.
5. **Import** in the browser as each user, at `/settings/data`: each calendar file into `Personal` (the old `personal`) or *New calendar…* with the old name; task lists into `Personal` or a new calendar; `contacts.vcf` into `Contacts`. Each upload reports *n new, n replaced, n skipped*; for a first import, *replaced* means the old server held two resources with one UID in a collection (it allowed that; one collection now holds a UID once) and *skipped* a component no collection here can hold: a journal into `Personal`, which holds events and tasks, or a stand-alone VFREEBUSY. Both should be 0 for production's data. Uploading the same file again only replaces.
6. **Check and re-share.** Export again and compare: `go tool davfixtures export -base https://cog.duckfam.us -out tmp/dav-after -import tmp/dav-after-import`, then `go tool davfixtures compare -got tmp/dav-after/calendars tmp/dav-export/calendars tmp/dav-import/tasks` and the same for `addressbooks`. It must report 0 missing (anything missing is named with the file it came from). *differ* lists resources whose properties changed meaning; spellings that mean the same (parameter order, a default VALUE, folded lists, TEXT escaping, a Windows or prefixed TZID written as IANA) are not counted, `-exact` counts them too. MANAGED-ID is removed on import by design (the attachment is not managed here). Then make the shares from step 1 again in each calendar's and address book's share panel (or with a client that speaks CalendarServer sharing), and set names, colours, stars and photos noted in step 1.
7. **Smoke test with clients.** In DAVx5 (with jtx Board or tasks.org) and Thunderbird: remove and re-add the account (collection URLs are the same for `personal` and `contacts`, but every ETag and sync token is new, so a client keeping its cache would merge stale copies), then check the counts per calendar and address book, edit an event in Cog and see it on the phone, edit one on the phone and see it in Cog, and the same for a contact and a task. A sharee checks they see the shared collections.

Rolling back: stop Cog, deploy the previous `master`, move the new `app_data/{calendar,tasks,contacts}` directories aside and the old ones back. The system database needs nothing: this MR's system migrations only add tables and columns the old version ignores.

### 8.1 Import and export in the web UI

An **Import and export** page in each user's own settings (`/settings/data`, beside Profile, Security and Storage; not the admin-only plugin settings pages), built on the DAV core's `Backend` interface, so it covers every registered backend without per-plugin UI.

- **Import**: upload `.ics` or `.vcf` (size-limited); choose an existing collection or create one. The file is split into resources the way a client would PUT them: VEVENT/VTODO/VJOURNAL grouped by UID (overrides with RECURRENCE-ID stay with their master) together with the VTIMEZONEs they reference, and one resource per VCARD. Components of other types (`X-EVENT-SERIES`, VFREEBUSY) go with the resource of their UID, or the file's first resource when they have none, and are kept among its other components; one whose UID no resource has is skipped and counted. A VEVENT, VTODO or VJOURNAL with no VCALENDAR around it, which the old server stored as sent, is read as if it had one. Each resource goes through `Backend.Put`, so UID/`uri` rules, collection kind checks and leftovers are the same as over DAV. A resource whose UID already exists in the collection replaces it, which makes re-importing the same file idempotent. The result (imported, replaced, skipped, failed with reasons) is reported via flash after a redirect (PRG).
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
| Scheduling RFC 6638 | schedule inbox/outbox, server-side iTIP; replaces email-only invites (email stays for external attendees); uses SEQUENCE. Events only: no client Cog targets assigns tasks by iTIP, so VTODO is not scheduled |
| Free/busy query (RFC 4791 §7.10, RFC 6638 outbox POST) | FreeBusy share permission, scheduling |
| Timezones by reference RFC 7809 | `calendar-timezone-id`; clients may omit VTIMEZONE; `CalDAV-Timezones: F` leaves out standard VTIMEZONEs (section 4.1). No `timezone-service-set`: TZDIST is out of scope |
| Managed attachments RFC 8607 | attachments stored as Cog files instead of inline base64; per-instance (`rid`) supported (section 4.2) |
| WebDAV-Push (draft-bitfire-webdav-push; DAVx5) | push instead of polling; subscriptions per collection, inbox and home; Web Push transport with VAPID and RFC 8291 encryption (section 9.2) |
| CalendarServer sharing extensions (`CS:invite`, `CS:shared-url`, `CS:share`, `CS:invite-reply`) | show, make and answer shares in Apple clients and DAVx5 (section 7) |

### Feeds and derived collections

| Feature | Notes |
|---|---|
| ICS feeds (export) | secret, revocable `.ics` URL per calendar for webcal subscribers; free/busy-only variant |
| ICS subscriptions (import) | read-only calendars from remote feeds (school, sports); polled with ETag/If-Modified-Since; `CS:source`; fetched through `internal/safehttp` (block private ranges) |
| Birthday calendar | read-only calendar derived from contacts' BDAY/ANNIVERSARY, regenerated on card changes |

### 9.1 Feeds, subscriptions and birthdays

**ICS feeds** (`internal/dav/feed.go`): `/remote.php/dav/feeds/<token>.ics` serves a calendar as one iCalendar file, without an account.

- The secret is a **public link share** of the collection (`dav.Links`, over the share table): revocable tokens are what that table already keeps, the share panel lists and revokes them with the calendar's other shares, and the permission bits say which feed it is (Read: the calendar; FreeBusy only: busy times). Every request looks the token up, so revoking or renewing takes effect at once. A link counts only when the collection's owner made it, as for any share; one with a password is not a feed, since subscribing apps cannot give one; an expired one is gone.
- The text is `Server.Export`'s, from rows through the same path as GET; a busy-only feed is redacted by the backend as a free/busy-only share sees it (`BusyRedactor`), and a backend that cannot redact (tasks) is left out.
- **Polling is cheap**: the ETag is the collection's ctag (composite sync token and props version), and Last-Modified the latest of the collection's `updated_at` and its backends' `dav_sync.modified`. Both come from one query per backend, so `If-None-Match` or `If-Modified-Since` gets a 304 without anything rendered.
- **Managed attachments in a feed** carry a token (`?feed=`), an HMAC of the owner, the attachment and the feed's secret, accepted while that feed exists and an event of its calendar carries the attachment. Subscribers have no account, so plain URLs would not open, and a token bound to the feed is revoked with it, as an external attendee's is with their invitation. MANAGED-ID is dropped, as in an export. A busy-only feed has no attachments.
- The calendar settings page lists each calendar's two feeds, copyable and as a `webcal://` link, and makes, renews and revokes them (PRG).
- **An address book's public link** serves its cards the same way, as one `.vcf` file (`Server.Export`), with the same validators; a busy-only link to one serves nothing. The share panel shows each link's feed path.

**ICS subscriptions** (`internal/dav/subscribe.go`): a calendar collection of kind `subscription`, made from the settings page with a `webcal://`, `webcals://` (both read as https) or `http(s)://` URL.

- **Fetching**: through `internal/safehttp` (public addresses only, checked after resolution and pinned, every redirect hop checked, at most five), within 30 seconds and 10 MB, conditionally on the last response's ETag and Last-Modified, which are kept only once the events are stored. The first fetch happens on subscribing; a URL that fails is refused and nothing is kept.
- **Storing**: the feed is split into resources as an import is (VEVENTs grouped by UID with their overrides and VTIMEZONEs, METHOD dropped, MANAGED-ID stripped), each named after its UID, or its hash when the UID is unsafe in a path, so the same event has the same name on every fetch; an event without a UID gets one from a hash of its text. They are stored as rows through the events backend in one transaction (`BatchWriter`), decoded by `internal/dav/ical` with leftovers kept. An event whose text is unchanged is not rewritten (its ETag stays); one gone from the feed is deleted with a tombstone, so DAV clients sync the feed's changes. Components other than VEVENT are skipped and counted; a feed with nothing readable is an error rather than a reason to delete every event. The feed's X-WR-TIMEZONE becomes the calendar's zone for floating times when the user has not set one. VALARMs are removed, as Apple Calendar does by default: the feed's author does not decide when its subscribers are reminded, and Cog's reminders would otherwise fire for every fixture of a season.
- **Polling**: every hour, or as often as the feed asks (REFRESH-INTERVAL, X-PUBLISHED-TTL, within 15 minutes and a week). The calendar plugin runs `Server.PollDue` every minute; each due subscription is claimed in the system database first, so replicas never fetch the same feed at once. A failure is recorded on the subscription (count and reason, shown on the settings page with the next try), logged, and retried with backoff (doubling, at most a day); the URL is left out of messages, since private feed URLs are secrets. "Refresh now" fetches at once.
- **To clients** it is a calendar (resource type `calendar`) with `CS:source`, read-only (section 7). It is not `CS:subscribed`: a client that saw that would fetch the feed itself instead of syncing Cog's copy.

**Birthday calendar** (`internal/dav/birthday.go`): one per user, of kind `birthdays`, at `contact_birthdays`, turned on and off from the settings page.

- **Per user, not per address book**: one calendar to show or subscribe to, as Nextcloud and Apple do; per book would add a calendar per address book. It reads the user's own address books and those shared with them for reading (directly, through a group or with everyone).
- **A real collection, not a virtual one**: its events are rows, so the web UI, sync, ETags, feeds and sharing work unchanged. A virtual collection would need its own sync tokens over several address books, its own range queries and its own path in the web UI.
- **Events**: one yearly all-day event per BDAY and per ANNIVERSARY (or, in a 3.0 card without one, the vendor property clients use: X-ANNIVERSARY, X-EVOLUTION-ANNIVERSARY, X-MS-ANNIVERSARY, X-WAB-WEDDING_ANNIVERSARY, or Apple's and Google's `itemN.X-ABDATE` labelled `_$!<Anniversary>!$_`; other labelled dates are neither), `🎂 Name (1990)` or `💍 Name (2010)`, transparent. Dates are read as written in every version (`vcard.ParseDate`): `19950415`, `1995-04-15`, `--0415`, `--04-15`, date-times with or without zones, and Apple's yearless `1604-04-15` with X-APPLE-OMIT-YEAR; forms without a day, free text and impossible dates make no event. A series starts in the birth year (1900 at the earliest), or 1970 without one; February 29 recurs on the last day of February. The URI and UID are the address book and a hash of the card's URI, never its content, so an edit replaces the event in place; DTSTAMP is fixed, so regenerating an unchanged card writes nothing.
- **Following the cards**: the contacts store calls `Server.CardsChanged` after every write it commits (DAV, the web UI, an import, a deleted book). The calendar keeps, per address book, the version it has read (`dav_birthday_cursors`), and catches up from the cards changed and deleted since (`Backend.Changes`, then `DateSource.CardDates`, which reads the date columns and leftovers without rendering a card), the events those cards may have, and one batch write: a fixed number of queries however large the book. An import holds the sync until its last card. A cursor below the book's floor, a new book or a deleted one is read whole. A sync that fails (or is skipped while a plugin is disabled) is made good by the next.
- **Shared books**: each cursor records the book's owner, so a write to a book also syncs the calendars of the other users that read it. A share made, changed or deleted (`share.Store.OnChange`) syncs the calendar of the user it names, or every birthday calendar for a group or everyone's share; group membership changes without either, so the calendar plugin sweeps every birthday calendar hourly (`SweepBirthdays`). A book no longer readable, deleted or unshared, takes its events with it.

### 9.2 WebDAV-Push

**Subscriptions** (`internal/push`, `internal/dav/push.go`) live in the system database (`push_subscriptions`, section 4), beside `dav_collections`: every replica sees them and any replica can deliver, a sharee's subscription is to the owner's collection whatever database holds its resources, and nothing per user has to be opened to find who to tell. The store knows nothing of DAV: a subscription names a protocol and a resource in that protocol's terms, so JMAP push (milestone 10) adds a protocol, not a table.

- **Discovery**: `DAV: webdav-push` in OPTIONS; `transports` (Web Push with the VAPID public key), `topic` and `supported-triggers` on collections (content updates at depth 1, property updates at depth 0), the schedule inbox and the calendar and address book homes (content updates only), when asked for by name. The topic is an HMAC of the resource with the server secret: stable, unique, and opaque to the push service, which sees it in the `Topic` header.
- **Registration**: a `push-register` POST to anything the user can read (a free/busy-only share cannot subscribe: `push-not-available`). The push resource must be https; the subscription's P-256 key and auth secret are required, since every message is encrypted; triggers are downgraded rather than refused, and none left is `no-supported-trigger`. The same endpoint again updates that registration, as the draft asks; another user's endpoint is `invalid-subscription`. The answer is 204 with the registration URL (`/remote.php/dav/push/<id>`) in `Location` and the expiry in `Expires`: as asked, within an hour and a week, a week by default. Its owner DELETEs it to unsubscribe; anyone else gets 404.
- **Noticing changes**: every resource write and delete bumps its collection's row in `dav_sync` (section 6). The host gives every connection to a user's plugin database an observer (`db.TxObserver`, through `plugin.Registry.SetTxObserverFactory`): SQLite's update hook records the collections whose row changed, and the commit hook reports them, the rollback hook drops them. So DAV, the web UIs, imports, subscription polls, birthday syncs and scheduling deliveries are all covered without any of them knowing about push, and a write path added later is too. The inbox's row (collection 0) reports the owner's inbox. The `Store` reports collections created and deleted (their home changes; a deleted collection's subscribers are told) and properties changed (PROPPATCH, web renames); `ShareChanged` reports shares, which change a collection's privileges and the sharee's home.
- **Coalescing**: reports gather in memory (`pushQueue`) and wake the deliverer, which waits a second (`push.Debounce`) and then marks: the subscriptions to what changed whose triggers match, and, for a sharee, who can still read the collection (one who cannot is removed). A mark sets a bit on the subscription, so however many writes come before a delivery — an import committed resource by resource, a feed's batch — each subscriber gets one push.
- **Delivery** (`push.Deliverer`): due subscriptions are claimed with a two-minute lease, so replicas do not both send; the message is content-free, `<push-message>` with the topic and an empty `content-update` and/or `property-update`, no sync token; it is encrypted (RFC 8291, aes128gcm) and signed (VAPID, RFC 8292, ES256 with the key generated once and kept in `system_settings` like the server secret; the contact claim is the server's URL, which push services that require one get from any https deployment), sent with a day's TTL and the topic, through `internal/safehttp`. A change marked while its push was in flight stays owed (`seq`). 404 or 410 removes the subscription; any other failure is logged and retried after 30 seconds, doubling to at most six hours (or the service's Retry-After). Expired subscriptions get nothing and are pruned hourly with the subscriptions of deleted collections.
- **Push-Dont-Notify** is honoured for writes to resources, PROPPATCH and inbox deletions: the registrations it names (the current user's only; others and junk are ignored), or `*`, are not told of the request's own change. It is counted per change, so another client's write in the same second still reaches them; a request that changed nothing silences nothing.

### JSON representations and API

| Spec | Notes |
|---|---|
| JSCalendar RFC 8984 | JSON form of events, tasks and journals, from the same models as iCalendar (`internal/dav/jscal`, section 9.3) |
| JSContact RFC 9553 | JSON form of contacts, same rules (`internal/dav/jscontact`) |
| JMAP core RFC 8620 | `/.well-known/jmap`, the session resource, the API with batching and back-references, `Core/echo`, PushSubscription and EventSource (`internal/jmap`) |
| JMAP for Contacts RFC 9610 | `AddressBook/get|changes|set`, `ContactCard/get|changes|query|set` |
| JMAP for Calendars (draft-ietf-jmap-calendars) | behind `COG_JMAP_CALENDARS`: `Calendar/get|changes|set`, `CalendarEvent/get|changes|query|set`; tasks are not offered (JMAP for Tasks is another draft) |

### 9.3 JSCalendar, JSContact and JMAP

**Codecs** (`internal/dav/jscal`, `internal/dav/jscontact`, sharing `internal/dav/jsobj`) map the models (`ical.Object`, `vcard.Card`) to JSON and back, never the text. Decode(Encode(x)) equals x for every resource in the corpus (`TestFixturesRoundTrip` in each), and the RFCs' examples read and write back as sent (`TestRFCExamples`). Nothing is lost either way:

- **Model to JSON.** Modelled fields map to standard properties, grouped (an event's start, end, due and duration are one group; its organizer and attendees another). What JSCalendar or JSContact has no property for travels in vendor properties prefixed `cog:`: a component's or card's leftovers (`cog:leftovers`, the same JSON as the leftovers column), a participant's or email's unmodelled parameters (`cog:params`), Apple's property group (`cog:group`), types that are not contexts or features (`cog:types`), the vCard version, a VALARM's description and repeat, a value outside an enumeration (`cog:partstat` for `IN-PROCESS`), a journal entry (`cog:component: VJOURNAL` on an Event). Where the standard form says what a value means but not how it was written (a TZID's spelling, DTEND against DURATION, a date's syntax, the order of attendees), the group's exact fields are kept in `cog:exact`, and used on the way back only while the group's properties are still what they encode to: a client that changes the group is read for what it sent, with the vendor data beside it kept.
- **JSON to model.** A property a client sends that the model has no field for, at any depth Cog reads (a location's coordinates, a note's author, `localizations`), becomes a `JSPROP` leftover whose `JSPTR` parameter is its JSON pointer (draft-ietf-calext-jscalendar-icalendar; RFC 9555 for vCard), written back at that pointer. Ids a client chose are kept in a `JSID` parameter on the property they became. A name's or address's components in an order (or with kinds) N or ADR cannot say are kept on it in `X-COG-JSCOMPS` while they still say the same. So a JSON client's data survives a DAV client's edit of something else, and the reverse.
- **Overrides** are `recurrenceOverrides` patches against the master (whole properties); RDATEs empty patches; EXDATEs excluded ones; what a patch may not set (RFC 8984 section 4.3.5: `privacy`, `relatedTo`, `replyTo`) differs in `cog:override`; a second override of one occurrence or a second master is kept whole in `cog:more`.

**JMAP** (`internal/jmap`) answers `/jmap/api` through the auth chain (Basic, app passwords, sessions; requests must be `application/json`, which a page on another site cannot send). Every user is an **account**; a user who shares collections with another is an account of theirs too (`isPersonal: false`, read-only unless something in it is writable), holding just those collections, with each collection's `myRights` from the share (section 7).

- **Ids**: collections `c<id>`; records `r<collection>-<base64url(URI)>` (a URI too long for an id is named by its hash), so a DAV tombstone names the record it deleted.
- **State**: a type's state in an account is its collections' counters of the one backend that stores it (`v1:12-c42,15-c3`); collections' own state their IDs, properties versions and the user's rights. `/changes` reads `Backend.Changes` per collection; records created since the state are told apart by `created_version`; `maxChanges` stops at a version, never inside one; a collection gone or a counter below its floor is `cannotCalculateChanges`. `/queryChanges` always is.
- **Writes** go through `dav.Session` (`internal/dav/api.go`), the same `Backend.Put` and `Delete`, access checks, UID checks, managed-attachment checks and counters as a DAV request, so a JMAP change is a DAV change and the reverse (`TestCrossProtocol`, `TestCalendarEvents`). An update is a JMAP PatchObject applied to the record's JSON, read back, and stored on condition of the version it was read at. Cog's edit bookkeeping applies (section 3.2): REV moves on a card, DTSTAMP and LAST-MODIFIED on an event, SEQUENCE when times, location or attendees changed and the client did not set it, and changed fields drop their dependent leftovers.
- **Scheduling** happens only when `CalendarEvent/set` has `sendSchedulingMessages: true`, as the draft asks; otherwise the event is stored as sent.
- **Push**: `PushSubscription` keeps a JMAP subscription in the WebDAV-Push store (section 9.2), resource `user/<id>`: a `PushVerification` is pushed first, and `StateChange` only once the client has set the code it carried. At each push round the DAV core tells JMAP whose collections changed (`dav.UserListener`: the owners and the users they are shared with), which marks those users' subscriptions and wakes their EventSource connections (`/jmap/eventsource`). A StateChange carries the current states of every account the user has, for the types asked for.

**Agents and the mobile app** (task #32, not in this MR): no separate REST API. Two interfaces are planned, both over `dav.Session` (`internal/dav/api.go`), so an agent's change goes through the same access checks, UID rules, counters, push and edit bookkeeping as a DAV or JMAP one:

- **SSH CLI.** An agent gets its own SSH key (section 2 of `DESIGN/ssh.md`) and runs plugin commands (`ssh user@host 'calendar …'`, namespaced `<plugin> <subcommand>`, through the same interpreter as the shell). The calendar, tasks and contacts plugins register no commands yet; theirs would read and write the JSCalendar and JSContact shapes above, and include tasks, which JMAP lacks.
- **gRPC (or similar) API**, planned for a mobile app and Cog-specific features JMAP has no place for. A CLI client tool over it offers the same commands as the SSH CLI without an SSH connection per command.

What a key or token may reach (one collection, read only) is decided with those interfaces, not here.

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
10. **JSCalendar/JSContact encoders and JMAP** on the same rows.
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
- **Schedule inbox**: in the calendar plugin's database, beside the events the messages are about, versioned with `davsql` like a calendar (collection 0), not in the system database, which has no sync state and would hold every user's messages. Messages are stored as the text Cog wrote: they are never edited, so rows would add nothing (section 4.2).
- **Implicit scheduling decides what is news by comparing views**, not by SEQUENCE alone: a client that forgets to bump SEQUENCE still gets its attendees told, and a bumped SEQUENCE is itself a change. A PARTSTAT change of one attendee is not news to the others.
- **A REPLY updates the organizer's copy only.** The other attendees are not sent the event again for it, as SabreDAV does not: it would send every attendee a message per answer. Their copies show the answers as of the last invitation they got.
- **A cancellation marks the recipient's copy cancelled** rather than deleting it, so they see that it was cancelled, as SabreDAV does.
- **Sharees schedule as the owner only with the Schedule permission** (section 7).
- **Free/busy of other users** is visible through a share (section 7), and beyond shares by setting, not by default: an admin can let every user see every other user's busy time, and a group can let its members see each other's (so an organisation enables it for its own group, not the whole server). Busy time only, never event details; which calendars count and who may change a group's setting are decided when it is built. Not in this MR (issue #36).
- **Managed attachment files** are hashed files under the calendar plugin's data directory, as contact photos are, not files in the user's Files. An attachment lives and dies with the ATTACH that names it, its access follows the event's calendar and invitations rather than a file share, the calendar keeps working with the Files plugin disabled, and nothing a user does in Files (moving, renaming, deleting, editing) can break an event or change an attachment behind its MANAGED-ID. What Files gives for free, counting toward the quota, is kept by plugins reporting their storage (`plugin.StorageUser`). The ATTACH value is an absolute URL under the DAV root, not a plugin route, so clients fetch it with the credentials they already use for CalDAV.
- **CalendarServer shares need no acceptance**: a share made over DAV takes effect at once, like one made in the web panel, and `CS:invite-reply` is accepted but only declining changes anything (section 7). Pending invitations would need a notification collection and a state Cog's shares do not have, for clients that mostly show them as accepted anyway.
- **External attendees fetch attachments with a token** signed for their address (HMAC with the server secret) and checked against the event's current attendees, so removing them from the event revokes it.
- **Recurrence expansion**: `github.com/teambition/rrule-go`, already a dependency, behind `internal/dav/ical` and `internal/dav/tz`; only RRULE arithmetic, not iCalendar parsing. Two limits are worked around or accepted: it stops about 292 years after DTSTART (its offsets are `time.Duration`s), so `FromRules` starts a 1601-based Outlook observance at 1900 in the same phase; and an event series is not expanded beyond that span. When DTSTART does not match the rule, it is counted as an occurrence on top of COUNT rather than as one of them.

- **A feed's secret is a public link share** of the collection, not a column on `dav_collections`: the share table already keeps revocable tokens with expiry, the share panel already lists and revokes links, the permission bits tell a full feed from a busy-only one, and a collection can have both (section 9.1).
- **Feed subscribers fetch managed attachments with a token** bound to the feed's secret, not plain URLs, which would not open without an account; revoking the feed revokes them (section 9.1).
- **Subscriptions are stored, not proxied**: the server fetches the feed and keeps its events as rows, so every client syncs them (and the web UI shows them) with Cog's own ETags and tombstones; the collection is a `calendar` with `CS:source`, not CalendarServer's `CS:subscribed`, which makes Apple clients fetch the feed themselves. They hold events only.
- **The birthday calendar is per user and a real collection**, derived incrementally from the address books' sync state (section 9.1).
- **Collections Cog fills are read-only to their owner too** (section 3.0): a client's edit would be overwritten by the next fetch or card change.
- **Push subscriptions live in the system database**, generic over protocols (`internal/push`), not per user beside the resources: any replica can find and deliver them, sharees subscribe to the owner's collection, and JMAP push reuses the store (section 9.2).
- **Changes are noticed by a connection observer on `dav_sync`**, not by calls at each write path: every write already bumps that row in its transaction, so watching it covers every path, present and future, and reports only committed changes. It is SQLite-specific, like the search drainer's update hooks, and like them only a hint: a lost push costs a client a poll, never data. A backend without connection hooks would report from `davsql.Bump`'s callers instead.
- **Pushes are content-free** (topic and kind of update, no sync token) and **owed, not queued**: a bit per subscription, so a burst of writes is one push and a failing endpoint holds one owed push, not a backlog.
- **JSON representations come from the models**, with nothing lost either way: vendor `cog:` properties for what the model holds and JSON cannot say, `JSPROP`/`JSID` leftovers for what JSON holds and the model cannot, and exact forms used only while unchanged (section 9.3). Converting through iCalendar or vCard text would lose leftovers' bytes and re-parse on every read.
- **A shared collection is in its owner's JMAP account**, which the sharee sees as a second account, as JMAP's sharing model has it, rather than mixed into the sharee's own.
- **JMAP record ids name the resource** (collection and URI), so a tombstone names the record it deleted and no id table is needed; the price is that a record cannot move between collections in place, and clients destroy and create instead.
- **Write transactions begin IMMEDIATE** (`_txlock=immediate` on every writable handle, plugin and system databases; `db.TxLockImmediate`): a deferred transaction that reads and then writes cannot wait for the write lock and fails with `database is locked` when another connection commits in between. Taking the lock at BEGIN makes it wait under `_busy_timeout` instead. Read-only handles leave it out. Transactions that only read take the write lock too; there are few, and they are short.
- **A JMAP record does not move between collections**: changing `addressBookIds` or `calendarIds` stays `invalidProperties`. Clients destroy and create; ids keep naming the resource, and no id table is needed (section 9.3).
- **Schema scripts run in a transaction**: `migrate` applies `schema.sql` and each migration in one transaction, so a database from an incompatible version is refused unchanged with `migrate.ErrIncompatible` instead of being half migrated (section 8).
- **No REST API for agents** (task #32): agents use the SSH CLI with a key of their own, and later a CLI client over the gRPC API planned for the mobile app, both over `dav.Session` (section 9.3). The REST routes proposed in milestone 10 are dropped. Neither interface is part of this MR.
- **JMAP push needs encryption keys**: every push is RFC 8291-encrypted, as WebDAV-Push's are; a subscription without keys is refused rather than pushed in the clear.

## 13. Open questions

None open.

## 14. Progress

Branch `design-dav-storage`, one draft MR to master. Update this section at the end of each milestone.

| Milestone | State |
|---|---|
| 1. contentline + fixtures | Done. `internal/dav/contentline`; `testdata/dav` (see its README); `go tool davfixtures` for the local-only GPL corpus and personal exports. |
| 2. DAV core | Done. `internal/dav` (collections in the system database, `Server` for root/principals/homes, PROPFIND/PROPPATCH/REPORT/MKCALENDAR/extended MKCOL, composite sync tokens, privilege sets, import/export); `internal/dav/davsql` (counters and tombstones in each plugin DB); `internal/dav/tz`. Calendar, tasks (VTODO and VJOURNAL, blob-backed) and contacts are backends; their web UIs read collections from the core. `/settings/data` imports and exports; the tombstone retention is an admin setting. Protocol tests in `internal/dav/protocol_test.go`. |
| 3. Events | Done. `internal/dav/ical` (values, leftovers, VEVENT codec, recurrence with overrides); `internal/dav/tz` generates VTIMEZONEs from Go's zone data and evaluates clients' own; the calendar plugin stores rows (section 4.2) and renders text on read through `dav.Table.Render`; the web UI edits rows; RFC 7809. |
| 4. Tasks and journals | Done. `internal/dav/ical` models VTODO and VJOURNAL with the event codec (one `Component` type, properties claimed per kind); `internal/dav/icalsql` stores calendar objects as rows for both the calendar and the tasks plugin; tasks and journals are rows (section 4.3), with RELATED-TO by UID and X-APPLE-SORT-ORDER; recurring tasks complete by advancing; the tasks web UI edits rows. |
| 5. Contacts | Done. `internal/dav/vcard` models vCard 2.1, 3.0 and 4.0 (labels by group, groups and members, inline photos) with leftovers; `internal/dav/codec` holds what it shares with `ical`; the contacts plugin stores cards as rows (section 4.4), photos as hashed files, and renders text on read; the contacts web UI edits rows. |
| 6. Scheduling and free/busy | Done. `internal/dav/itip` works out iTIP messages (RFC 5546) from a change and applies received ones; `internal/dav/schedule.go` has the inbox and outbox, implicit scheduling on DAV PUT/DELETE and web edits, delivery between Cog users and iMIP email to others, SCHEDULE-AGENT/-STATUS/-FORCE-SEND, the free-busy-query REPORT and outbox free/busy, and the Schedule permission (sections 4.2, 5 and 7). Inboxes live in the calendar plugin's database (`schedule_inbox`). Fixtures: RFC 5546's examples in `testdata/dav/ics/rfc5546/` and its broken ones in `quirks/rfc5546/`. |
| 7. Sharing extensions, managed attachments | Done. `internal/dav/sharing.go` serves CalendarServer sharing (`CS:invite`, `CS:shared-url`, `CS:share`, `CS:invite-reply`) on the `dav:collection` shares (section 7); `internal/dav/attach.go` serves RFC 8607 managed attachments, with ATTACH modelled in `event_attachments` and the files kept by the calendar plugin (section 4.2); the event modal lists, downloads, uploads and removes attachments. Fixtures: RFC 8607's examples in `testdata/dav/ics/rfc8607/`, CalendarServer's managed and dropbox forms in `calendarserver/`. |
| 8. Feeds | Done. `internal/dav/feed.go` serves ICS feeds from public link shares, with a free/busy-only variant, conditional GETs and feed-bound attachment tokens; `internal/dav/subscribe.go` keeps subscription calendars filled from remote feeds through `internal/safehttp`, polled by the calendar plugin; `internal/dav/birthday.go` derives the birthday calendar from the contacts' dates (`vcard.ParseDate`); collections have a kind, and those Cog fills are read-only (sections 3.0, 7, 9.1). The calendar settings page manages all three. |
| 9. WebDAV-Push | Done. `internal/webpush` encrypts (RFC 8291) and signs (VAPID, RFC 8292) Web Push messages with the standard library only; `internal/push` keeps subscriptions in the system database and delivers them; `internal/dav/push.go` serves discovery, registration and unregistration on collections, inboxes and homes, notices changes through a connection observer on `dav_sync`, and honours Push-Dont-Notify (section 9.2). |
| 10. JSCalendar, JSContact and JMAP | Done. `internal/dav/jscal` and `internal/dav/jscontact` (with `internal/dav/jsobj`) map the models to RFC 8984 and RFC 9553 and back, losslessly both ways (section 9.3); `internal/dav/api.go` is the DAV core outside DAV requests (`dav.Session`: collections with rights, models, changes, scheduled writes); `internal/jmap` serves JMAP core, JMAP for Contacts, JMAP for Calendars behind `COG_JMAP_CALENDARS`, PushSubscription over the push store and EventSource. |
| 11. Deploy | Prepared and rehearsed; not run against production. The runbook is section 8.2. `go tool davfixtures` gained `tasks` (the old server's task lists from its database), `compare` (an export after an import against the one before) and a whole-collection file per collection from `export`. Rehearsed end to end: a server built from `master`, filled over CalDAV/CardDAV with 150 corpus files in three calendars and an address book, tasks made in its web UI and calendar and address book shares, then exported, stopped, its `app_data` moved aside, started as this branch on the same system database, imported through `/settings/data` (and again into a `go tool dev -reset` server), compared over DAV and JMAP, and re-shared. Old databases left in place are refused unchanged. Write transactions begin IMMEDIATE, JMAP records still do not move, and agents get no REST API (section 12). |

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
- ATTACH stayed a leftover until managed attachments; `event_attachments` arrived in milestone 7.
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
- BDAY, ANNIVERSARY and GENDER are stored as written, not parsed: their syntax differs by version (`--0203`, `2012-06-06`, `19950415`). The birthday calendar reads them (`vcard.ParseDate`, section 9.1).
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

How milestone 6 differs from the plan:

- Events only: VTODO is not scheduled (section 9).
- Email invitations are gone from the calendar plugin; the DAV core sends iMIP, for external attendees and for replies to external organizers, which the old code never sent. Milestone 3's invitation (the stored event with METHOD) became the per-attendee view of section 4.2.
- The inbox and outbox are virtual collections of the core, not rows in `dav_collections`; their names are reserved in every calendar home (section 3.0).
- `dav.Users` gained `ByEmails` (one query for every recipient), and `ical.ZonesFor` moved from `icalsql` so the core can use it without an import cycle; `ical.Object.Clone` is new.
- The share preset "Appointment Scheduling" (Read, FreeBusy, Schedule, no Write) granted nothing Schedule could use, since scheduling for the owner needs a write; it is now "Delegate" (Read, Write, FreeBusy, Schedule).

Adjacent bugs fixed in milestone 6:

- Every web save of an event with attendees emailed every attendee a new invitation, even when nothing changed; Cog users were emailed instead of finding the event in their calendar.
- Web-made events with attendees had no ORGANIZER, which RFC 5545 requires of a meeting, so no client could treat them as one.
- `RespondToInvite` changed the PARTSTAT of whichever attendee the form's `email` named, did not move DTSTAMP or LAST-MODIFIED, told nobody, and no page used it; it answers for the user's own addresses, replies to the organizer, and the event modal has Yes/Maybe/No.
- The event modal ignored its render error.

Open after milestone 6:

- Free/busy across the server: decided in milestone 11 as an admin and group setting (section 12), not built.
- Not built, and not part of this MR: a web view of the inbox (a notification list of invitations and replies).

How milestone 7 differs from the plan:

- `event_attachments` models every ATTACH, not only managed ones: URIs and inline data are rows too, written back as received, so the web UI can list and download them. Their columns are on `icalsql.Tables` like relations; tasks and journals keep ATTACH as a leftover.
- The bytes live with the calendar plugin, not the Files plugin (section 12). The DAV core owns the protocol (POST, serving, access, scheduling) through `dav.AttachmentStore`; `PutRequest.Attachments` carries new files into the write that names them.
- `dav.Server.Shares` is a `dav.Shares` (listing by owner and changing permissions as well), and the server has `BaseURL` and `Secret` for attachment URLs and tokens.
- Quota counts what plugins store outside Files (`plugin.StorageUser`): calendar attachments and contact photos.
- Shares need no acceptance (section 12); there is no notification collection.
- `itip` treats a changed attachment as news to attendees, and an attachment POST bumps SEQUENCE.

Adjacent bugs fixed in milestone 7:

- The quota check allowed any upload when the usage could not be read; it fails with the error.
- The share panel stopped listing shares at the first error, and showed a share with a user it could not look up without a name; user search and the picker did the same with their lists, and two renders ignored their errors. Errors are logged and answered with 500; a share with a deleted user says so.

How milestone 8 differs from the plan:

- The poller lives in the DAV core and runs from the calendar plugin's lifecycle, not a service of its own: with the calendar disabled there is nothing to store events in.
- Collections gained `kind`; subscriptions their own table (`dav_subscriptions`), joined into every collection read for `Source`; `dav_sync` gained `modified`, which `davsql.Bump` sets, for Last-Modified.
- `dav.BatchWriter` stores many events of one collection in one transaction (a feed, a birthday sync); without it the core writes one at a time.
- `dav.DateSource` lets the birthday calendar read cards' dates from columns and leftovers, not rendered text.
- A subscription holds VEVENT only; a feed's tasks are skipped and counted. Its alarms are removed (section 9.1).
- `share.Store` gained `OnChange`, run after a share is created, changed or deleted; the birthday calendars follow shared address books through it.

Adjacent bugs fixed in milestone 8:

- The share panel showed a calendar's public link as `/s/<token>`, a page that serves no calendar: the link did nothing. It is the calendar's feed now, and the panel shows the feed's path.
- The calendar settings page answered errors with their text (`http.Error(w, err.Error())`) and ignored its render error; it logs them and answers 500.
- An address book's public link served nothing; it serves the book's cards as a vCard file.
- The reminders endpoint (`/apps/calendar/notifications`) loaded each due reminder's event with its own query; it loads them in one.
- `TestBrowserVolumeBrowse` browses the data directory, so its golden showed the size of `system.db-wal`, which followed the system schema and every write before the page. The test empties the log first.

How milestone 9 differs from the plan:

- Subscriptions are to collections, the schedule inbox and the two homes (the draft's SHOULD list), not collections only; principals are not offered push, since nothing a client shows of a principal changes in Cog.
- Changes are noticed by observing `dav_sync` on every plugin connection (section 12), not by notifying from each write path. `db.ObserveHook` shares SQLite's one update hook per connection between search and the observer.
- Messages carry no sync token, by decision (content-free); the draft's SHOULD is for clients that skip a push they already know, which a sync with the token they hold does cheaply anyway.
- Registration always answers 204: the draft's 201 is for registrations that return extra information, which Cog has none of.
- PROPPATCH and share changes are property updates; a collection deleted is a content update to its subscribers and to its owner's home.
- `internal/safehttp` gained `ClientWith` and `Options.Allow`, a private-address allowance for one client, which the push tests use for their TLS push service; the process-wide `SetAllowPrivate` stays for the older tests.
- `services.NewFromDB` returns an error (the server secret and the VAPID key can fail to load); `Providers` has `Push` and `Pusher`, and `StartPush` runs delivery beside the plugins.

Adjacent bugs fixed in milestone 9:

- The server secret swallowed its errors: a failed random read or a failed write of a new secret went unnoticed, and a secret that could not be stored was regenerated on every start, invalidating sessions' CSRF tokens and every signed attachment URL. Loading it (and the VAPID key) now fails startup with the error, and two processes starting at once agree on the stored key.
- `safehttp`'s private-address override was a plain variable read by dialing goroutines while tests toggled it (a data race); it is atomic.

Accepted in milestone 9:

- Pruning tombstones raises a collection's `floor` in `dav_sync`, which the observer cannot tell from a write, so it sends one push. It happens once per expired batch (default 90 days), after a client's sync.
- Push-Dont-Notify is matched to a request's change after the request ends; a delivery round that starts between the commit and the end of the request (a second after the change at the earliest) sends the push anyway.
- External (gRPC) plugins open their databases in their own process, outside the observer; none holds DAV resources.

How milestone 10 differs from the plan:

- Leftovers travel as `cog:leftovers` (Cog's own JSON of them), not RFC 9555's `vCardProps` or jCal arrays, which decode values and could not give back the bytes a client sent. JSON a client sends that Cog does not model is kept the standard way, as `JSPROP` (and ids as `JSID`).
- `cog:exact` was not planned: without it, a value JSON can only normalise (DTEND written against DURATION, a TZID's spelling, ROLE left out) would change on every JSON round trip.
- Backends hand out models (`dav.ModelReader`, through `dav.Table.Load`); every resource table has `created_version`, and `Backend.Changes` the version each tombstone was made at (`Changes.DeletedAt`), for JMAP's `/changes` and its `maxChanges`.
- `putScheduled` and `deleteScheduled` moved from the DAV request to the server, with a switch to store without scheduling, so `dav.Session` writes as a PUT does.
- The DAV core reports whose collections changed at each push round (`dav.UserListener`); JMAP marks its subscriptions from that rather than having resources of its own in the push store.
- Not offered: blobs (photos are `data:` URIs, so no method needs upload or download; the URLs answer 404), `expandRecurrences` and `utcStart`/`utcEnd` in CalendarEvent queries, moving a record between collections (section 12), `shareWith` (no Principals capability), keyless push subscriptions, JMAP for Tasks. `isSubscribed` and the calendars' display preferences are accepted and not kept.
- The agent API's routes were proposed (section 9.3), not built; milestone 11 dropped them for the SSH CLI and a gRPC client (section 12).

Adjacent bugs fixed in milestone 10:

- `assert.Nil`/`NotNil` (and `require`'s) panicked on a value that cannot be nil (a string, a number) instead of reporting it.

Found in milestone 10, fixed in milestone 11 (section 12): deferred write transactions that read first fail with `database is locked` when another connection commits in between.

How milestone 11 differs from the plan:

- The cut-over is not a step after merging but the merge itself: CI deploys every commit on `master`, so the runbook (section 8.2) does the export before merging and moves the old databases aside as the deploy starts.
- The export was rehearsed against a stand-in, not production, which this milestone does not touch: the production export and the pre-flight counts (tasks, web-only photos and stars) are for the operator to run (section 8.2, steps 1–3).
- Section 13's agent API question was answered by dropping the REST facade: agents will use the SSH CLI, and later a CLI client over the gRPC API planned for the mobile app (sections 9.3 and 12). Neither is built here.
- What the old server held that no export carries, found by reading its schema: contact photos uploaded in its web UI and stars (columns beside the card), per-event colours, and calendar colours and names (the import names a new collection after what is typed). The runbook lists them in its pre-flight queries to set again by hand; with nine contacts and three calendars in production, a tool was not worth it.

Rehearsal results (old server built from `master` at `b39e940`; 127 calendar resources, 23 cards, 6 tasks in two lists, two shares):

- Every resource arrived, with the same properties, apart from what the import is meant to change (MANAGED-ID, section 8.1) and a TZID on a UTC time or an empty TZID, which the codec drops. Equivalent spellings (default VALUE, a date without VALUE=DATE, lists over several lines, TEXT escaping, `TYPE=A;TYPE=B`, Windows and prefixed TZIDs) differ in bytes, as section 4.1 and the codecs intend; `compare` reads past them.
- Not imported, and counted as skipped: a VJOURNAL in `personal` (which holds events and tasks) and stand-alone VFREEBUSY objects. Production's calendars come from one Fossify import and are unlikely to hold either; step 5's counts will say.
- Duplicate UIDs within one old collection (the old server allowed them) became one resource each; the import reports them as *replaced*.
- DAV and JMAP agree: the same 618 events per calendar and 22 cards, with the same UIDs and titles. Master-less overrides appear in JMAP as one event with the rest kept in `cog:more` (section 9.3); tasks and journals are not in JMAP (section 9).
- Bob, re-shared `personal` and `Contacts` through the share panel, sees both over DAV and not the unshared `work`; the old `calendar` and `address_book` share rows stay in the system database, unused.
- Not done: DAVx5 and Thunderbird against the rehearsal server (no devices here); the runbook's step 7 is for the operator.

Adjacent bugs fixed in milestone 11:

- The import dropped components of types it does not store without counting them: `X-EVENT-SERIES` or any other `X-` component, and VFREEBUSY, even inside a resource the old server held whole. They are kept with their resource (section 8.1) or counted as skipped.
- The import skipped a VEVENT stored without its VCALENDAR (the old server accepted a PUT of one); it wraps it.
- `migrate` ran `schema.sql` outside a transaction, so a database it could not migrate was left half changed: the new schema's tables were created in the old calendar database before the first index failed with `no such column: component_id`, which also gave no hint of the cause. It runs every script in a transaction and reports `ErrIncompatible`.
- `go tool dev` ignored the errors of writing its SMTP settings and of closing the database it wrote them with.
- Saving a calendar's name, colour or zone answered any lookup error with 404, rendered a database error's text to the browser, and answered an unknown zone with a bare 400 page from a POST. Errors are logged and every outcome redirects with a message (PRG).

CI had failed on every push since milestone 3, while every milestone's tests passed locally; the branch was marked ready before that was noticed. Four causes, all reproduced locally (a worktree without `tmp/`, Go 1.26, Go's own `zoneinfo.zip`) and fixed:

- `TestFixturesModelled` (vcard) counted the local-only GPL fixtures, and its minimums had been set with them present; CI has none. It counts the committed corpus only.
- `TestVTimezoneRoundTrip` failed for Africa/Casablanca after 2037 with Go's bundled zone data, which lists Morocco's Ramadan changes to 2087, where this machine's system data stops at 2037. This was a real error in what Cog sends with that data, not just in the test: `VTimezone` scanned to 2037 and wrote the zone as constant after it. It scans to 2087 (section 4.1).
- `TestFixturesRoundTrip` (jscal) compared models as JSON text, and Go 1.26 writes text that is not UTF-8 (a Latin-1 fixture) as `\ufffd` but a real U+FFFD as itself, so both sides differed in spelling only. The comparison makes them one; JSON carrying U+FFFD for such bytes is expected.
- `internal/web` came near or past `go test`'s default 10 minutes from `39de905` (after milestone 3) on, when browser tests began queueing for Chrome slots (`WEBTEST_PARALLEL`) and the queueing counted against the package's time. CI and `go tool verify` run with `-timeout 30m`.

Fixed after milestone 2 (pre-existing, found through the `webtest` fix):

- `TestBrowserAdminCreateVolume`'s golden showed the run's temp directory. `webtest` now masks the per-test temp root (`«tmp»`), an environment value like the CSRF token.
- `cmd/cog` `TestBrowserCore/Dashboard` and `/Navigation` were skipped as failing during milestone 2 (6 app cards; DOM differing between runs). Re-run against the fixed `webtest` they pass ten runs out of ten with 8 cards and identical DOM; the failures did not reproduce, so they are unskipped with fresh goldens. `Snapshot` now logs when a page does not settle instead of discarding the error, so a recurrence says why.
- Gallery's `idx_media_fingerprint` was never created on new databases: the guarded migration that adds it is skipped once `schema.sql` has the column. `migration007` creates it unguarded; `TestFreshSchemaHasMigrations` checks every column and index the migrations add reaches a fresh database. `people.contact_uid` is now in `schema.sql` as well.
- Gallery recognition dropped database errors (storing tags and faces, marking media done or failed); a failed write now ends the pass with the error.
- Gallery's clustering tests and the SSH tests ran on shared-cache in-memory databases, which CLAUDE.md rules out; they use files in the test's temp directory.
