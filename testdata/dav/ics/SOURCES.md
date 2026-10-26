# iCalendar fixture corpus — provenance

Every file here is a byte-exact copy of a file from the upstream repository
named in its section (same line endings, encoding and folding; nothing was
re-saved). `.gitattributes` marks the tree `-text` so git never normalises
line endings. Upstreams were cloned with `git clone --depth 1`; the commit is
the one that clone resolved to.

Caveats that apply to the whole corpus:

- Some upstreams store their fixtures with LF endings or long unfolded lines,
  even when the producing client emits CRLF and folds at 75 octets. That was
  the upstream's doing (git normalisation or hand editing); it is kept as-is
  because a parser has to cope with it too. The "eol" a file has is whatever
  it has here.
- Personal data in upstream fixtures was usually redacted by the upstream
  (`xxxx`, `example.com`, `[private]`). Some files were trimmed or hand-edited
  by the upstream to reproduce a bug; where that is known it is noted.
- "Client" is inferred from PRODID and client-specific X- properties. A
  PRODID can lie (a converter can copy one), so treat it as strong evidence,
  not proof.
- `quirks/` holds real-world output that is **not** valid RFC 5545 in some
  syntactic way. A strict parser may reject these; a lenient one should not.
  Each is listed with the reason.

Licence identifiers are SPDX. All are permissive. GPL/LGPL/AGPL material
(Radicale, ical4android/DAVx5, python-recurring-ical-events, Tine 2.0,
Nextcloud) is deliberately **not** here.

---

## biweekly/

- Upstream: https://github.com/mangstadt/biweekly @ `f699308b5a5c35ba5f5492392ec100f16b077437`
- Licence: `BSD-2-Clause` — `LICENSE`
- Path upstream: `src/test/resources/biweekly/{io/text,issues}/`

| File | Client | Exercises |
|---|---|---|
| outlook-2010.ics | Outlook 2010 (14.0) | METHOD:REQUEST, Windows TZID VTIMEZONE, ATTENDEE params, X-ALT-DESC HTML, tab-folded lines |
| issue115-1.ics | Outlook 2016 (16.0) | 40 KB X-ALT-DESC Word HTML, tab folding, LANGUAGE param, Exchange-style long UID, VALARM |
| issue85.ics | Outlook 2016 | `W. Europe Standard Time` VTIMEZONE (DTSTART 1601), quoted TZID param |
| issue126.ics | Exchange 2010 / Outlook.com | METHOD:REQUEST, unquoted TZID with spaces, minimal VEVENTs without UID |
| issue101.ics | KDE libkcal 4.3 (KOrganizer) | VTIMEZONE with multiple STANDARD/DAYLIGHT incl. RDATE + bounded RRULE, X-KDE-* |
| pr63.ics | Google Calendar (UID @google.com) | `TZID=/America/New_York` (leading slash, tzurl style) with no VTIMEZONE, UNTIL in UTC |
| rfc5545-example6.ics | RFC 5545 example (synthetic) | VFREEBUSY with FREEBUSY periods |

## python-icalendar/

- Upstream: https://github.com/collective/icalendar @ `0a16c354def97afe95ff80639ae4e2b0fbfce8f3`
- Licence: `BSD-2-Clause` — `LICENSE.rst`
- Path upstream: `src/icalendar/tests/{calendars,events}/`

| File | Client | Exercises |
|---|---|---|
| alarm_thunderbird_future.ics | Thunderbird (Mozilla Calendar V1.1) | 14 KB VTIMEZONE with ~85 sub-components (RDATE history), X-MOZ-* props, two VALARMs |
| alarm_thunderbird_snoozed_until_1457.ics | Thunderbird | X-MOZ-SNOOZE-TIME / X-MOZ-LASTACK on a snoozed alarm |
| alarm_etar_future.ics | Etar (Android, via "Offline Calendar / iCal Import/Export 2.8.1") | Android export: big VTIMEZONE, 3 VALARMs, X-LIC-LOCATION |
| alarm_google_future.ics | Google Calendar | 4 VALARMs incl. EMAIL, ATTENDEE, X-WR-* |
| x_location.ics | Google Calendar export of an Apple-created event | X-APPLE-STRUCTURED-LOCATION with long quoted params, non-ASCII |
| issue_350.ics | Podio API | DESCRIPTION;ALTREP=data: URI, X-ALT-DESC HTML, tab folding, escaped quotes, umlauts |
| issue_348_exception_parsing_value.ics | Sixt car rental ("-//Sixt//RAC") | VFREEBUSY + VEVENT + VALARM in one calendar, odd values |
| issue_27_multiple_periods_in_freebusy_multiple_freebusies.ics | DavMail (Exchange gateway) | iTIP METHOD:REPLY VFREEBUSY, multiple FREEBUSY lines with FBTYPE |
| issue_27_multiple_periods_in_freebusy_one_freebusy.ics | DavMail | FREEBUSY with comma-separated period list |
| issue_836_do_not_quote_tzid.ics | Exchange 2010 / O365 | Windows TZID, METHOD:PUBLISH |
| timezone_same_start.ics | Exchange 2010 / O365 | METHOD:REQUEST, STANDARD and DAYLIGHT with identical DTSTART 1601 |
| issue_156_RDATE_with_PERIOD_TZID_khal_2.ics | khal (Lotus Notes origin: X-LOTUS-*) | RDATE;VALUE=PERIOD;TZID, RECURRENCE-ID, ATTENDEEs |
| issue_237_fail_to_parse_timezone_with_non_ascii_tzid.ics | Outlook-style (`(UTC-03:00) Brasília`) | Non-ASCII UTF-8 TZID and TZNAME, quoted TZID param |
| issue_53_description_parsed_properly.ics | Meetup.com feed | Bare VEVENT (no VCALENDAR wrapper), folded DESCRIPTION, GEO, escaped commas |
| property_params.ics | BlackBerry (RIM BIS 3.0) | METHOD:REQUEST, quoted CN, X-RIM-*, X-MICROSOFT-CDO-ALLDAYEVENT |
| rfc_7529.ics | BlackBerry (RIM BIS 3.0) | RRULE variety from a real device |
| issue_1549_binary_attachment.ics | synthetic | ATTACH;ENCODING=BASE64;VALUE=BINARY inline PNG, single very long unfolded line |
| rfc_6868.ics | RFC 6868 example (synthetic) | ^n / ^' / ^^ parameter-value caret escaping |
| rfc_7256_multi_value_parameters.ics | synthetic | Multi-valued parameters (MEMBER, DELEGATED-TO) |
| rfc_7953_3.ics | RFC 7953 example (synthetic) | VAVAILABILITY / AVAILABLE components |
| rfc_7986_conferences.ics | RFC 7986 example (synthetic) | CONFERENCE with FEATURE/LABEL params |
| rfc_7986_image.ics | RFC 7986 example (synthetic) | IMAGE (URI and inline base64) on VEVENT/VTODO/VJOURNAL |
| rfc_9074_example_4.ics | RFC 9074 example (synthetic) | VALARM UID, RELATED-TO;RELTYPE=SNOOZE, ACKNOWLEDGED |
| rfc_9253_related_to.ics | RFC 9253 example (synthetic) | RELATED-TO with new RELTYPE values, LINK |

## libical/

- Upstream: https://github.com/libical/libical @ `3d0f0cc430a0002232817d3f108444f4031d6e3b`
- Licence: `MPL-2.0` (libical is `MPL-2.0 OR LGPL-2.1-only`; we take MPL-2.0) — `LICENSE.txt`, `LICENSES/MPL-2.0.txt`
- Path upstream: `test-data/`

| File | Client | Exercises |
|---|---|---|
| 2446.ics | RFC 2446 (iTIP) examples, synthetic ("ACME") | Every iTIP METHOD (PUBLISH/REQUEST/REPLY/ADD/CANCEL/REFRESH/COUNTER/DECLINECOUNTER) as concatenated VCALENDARs; VTODO, VJOURNAL, VFREEBUSY, RECURRENCE-ID;RANGE, RELATED-TO |
| incoming.ics | synthetic iTIP mailbox ("ACME") | 15 concatenated VCALENDARs: REQUEST/REPLY/CANCEL/COUNTER/PUBLISH, VFREEBUSY reply |

## icaljs/

- Upstream: https://github.com/kewisch/ical.js @ `33ba266282fac7c721d792e04124513df73fe140`
- Licence: `MPL-2.0` — `LICENSE`
- Path upstream: `samples/`, `test/parser/`

| File | Client | Exercises |
|---|---|---|
| google_birthday.ics | Google Calendar | Birthday calendar: X-GOOGLE-*, RECURRENCE-ID overrides, RDATE |
| recur_instances.ics | Zimbra | RRULE + EXDATE + RDATE + RECURRENCE-ID overrides, two VTIMEZONEs, unknown X- component |
| multiple_rrules.ics | Zimbra | VEVENT with more than one RRULE |
| property_params.ics | synthetic parser test | Quoted params containing `,` `;` `:`, multi-value DELEGATED-TO, caret/backslash oddities, non-ASCII in CN |

## py-vobject/

- Upstream: https://github.com/py-vobject/vobject @ `680ed60eed72d42fe2cbed62ca767320e433a8b4`
- Licence: `Apache-2.0` — `LICENSE`
- Path upstream: `test_files/`

| File | Client | Exercises |
|---|---|---|
| ms_tzid.ics | Outlook 2007 (12.0) | TZID containing commas (`Canberra, Melbourne, Sydney`), three STANDARD rules with UNTIL |
| tzid_8bit.ics | Outlook 2007 | Cyrillic 8-bit (UTF-8) TZID, used unquoted in a param |
| radicale-0827.ics | Thunderbird | Weekly RRULE with 9 separate EXDATE lines and RECURRENCE-ID overrides |

## sabre-vobject/

- Upstream: https://github.com/sabre-io/vobject @ `5e2f338811916c03927398bb24e527989de63194`
- Licence: `BSD-3-Clause` — `LICENSE`
- Path upstream: `tests/VObject/RecurrenceIterator/`

| File | Client | Exercises |
|---|---|---|
| UntilRespectsTimezoneTest.ics | Church Community Builder (feed) | METHOD:PUBLISH, RRULE UNTIL vs TZID, X-WR-* |

## ical4j/

- Upstream: https://github.com/ical4j/ical4j @ `9ef69b0712050b415ae36b978b6b76e9a5472397`
- Licence: `BSD-3-Clause` — `LICENSE.txt`
- Path upstream: `src/test/resources/samples/{valid,invalid}/`, `src/test/resources/net/fortuna/ical4j/{data,transform}/`.
  ical4j's "invalid" means "fails ical4j's semantic validation", not a syntax error; the syntax-broken ones are under `quirks/`.
- **Renamed:** `apple.ics` and `outlook1.ics` are `transform/apple.txt` and `transform/outlook1.txt` upstream (extension changed so the corpus globs on `*.ics`; bytes unchanged).

| File | Client | Exercises |
|---|---|---|
| apple-iphone-os-26-structured-location.ics | iOS 26 Calendar | X-APPLE-STRUCTURED-LOCATION with base64 X-APPLE-MAPKIT-HANDLE param folded across lines, escaped `;` in param, non-ASCII |
| apple.ics (transform/apple.txt) | macOS 10.8.5 Calendar | X-APPLE-DEFAULT-ALARM, `ACTION:NONE` alarm with TRIGGER 19760401T005545Z, ATTACH;VALUE=URI:Basso, ACKNOWLEDGED, X-WR-ALARMUID, X-APPLE-NEEDS-REPLY; LF, unfolded |
| calconnect10.ics | Apple iCal 3.0 | RRULE, X-WR-* |
| EXDATE.ics | Apple iCal 2.0 | EXDATE + large VTIMEZONE history |
| Australian_TV_Melbourne.ics | Apple iCal 1.5 | 41 VEVENTs with 39 VALARMs, ATTACH sound |
| maritz.ics | Nextcloud Calendar 5.3.2 | RELATED-TO;RELTYPE=SIBLING, four VALARMs incl. EMAIL and `-P1W6DT15H` |
| bitfire1.ics | DAVx5 / ical4j (X-TZINFO) | Minimal VTIMEZONE with RDATE;VALUE=PERIOD, seconds-precision TZOFFSETFROM (+005328), no PRODID |
| calconnect.ics, calconnect4.ics, calconnect6.ics, calconnect9.ics | Lotus Notes 6.0 (CalConnect interop) | METHOD:REQUEST, X-LOTUS-*, RDATE lists, RECURRENCE-ID, many ATTENDEE params |
| Standup.ics | Outlook 2003 (11.0) | RRULE, VALARM, X-MICROSOFT-CDO-* |
| 2207678.ics, Session6.ics | Outlook 2007 (12.0) | X-ALT-DESC, tab folding, non-ASCII |
| outlook1.ics (transform/outlook1.txt) | Windows Live Calendar (Outlook.com) | METHOD:REQUEST, `Pacific Standard Time` TZID, VALARM without DESCRIPTION |
| blankTzid.ics | Exchange 2010 | ATTACH, ATTENDEE, empty-ish TZID |
| stacksize.ics | eM Client (MailClient.VObject 9.2) | X-ALT-DESC HTML entities, VTIMEZONE with TZID param on its own DTSTART |
| evolution.ics, evolution2.ics | Evolution (Ximian) | X-EVOLUTION-*, VALARM |
| schedule-unstable.ics | Evolution | 37 VEVENTs, 12 VALARMs |
| korganizer_sample.ics | KOrganizer 3.1 | RRULE, X-KDE-* |
| sunbird_sample.ics | Mozilla Sunbird (Calendar V1.0) | 88 KB: 182 VEVENTs + 4 VTODOs, X-MOZILLA-*, non-ASCII |
| twinkle.ics | Mozilla Calendar V1.0 | VTODOs with VALARMs, RRULE |
| google_aus_holidays.ics | Google Calendar | 96 VEVENTs, RECURRENCE-ID, two VTIMEZONEs |
| groupwise.ics | Novell GroupWise 7.0.2 | X-GW* props, `$$$` in values, ATTENDEE CUTYPE=RESOURCE with space in mailto |
| oracle-personal-notes-test.ics | Oracle Calendar (CSDK) | ATTENDEE with many X-ORACLE-* params incl. base64 values, folded mid-param |
| zidestoreical4jbomb.ics | OpenGroupware ZideStore 1.3 | METHOD:REQUEST, ATTENDEE |
| bhav23-1.ics | Trumba Calendar Services | Hosted-feed VEVENT |
| 1106817412.ics | bloc.jp | Japanese (UTF-8) text, METHOD:PUBLISH |
| lastfm.ics | Last.fm event feed | 33 VEVENTs, non-ASCII, X- props |
| test-relaxed-parsing.ics | Scalix (X-SCALIX-LABEL), Outlook-style TZ | `TZID:(GMT-03:00)` quoted in params, JavaMail-style UID |

## icalnet/

- Upstream: https://github.com/rianjs/ical.net (now ical-org/ical.net) @ `ab7d9e2daa1e9d350cb91e9cd91dc3904eda8a54`
- Licence: `MIT` — `license.md`
- Path upstream: `Ical.Net.Tests/Calendars/{Serialization,Recurrence,Alarm,Todo,Journal}/`

| File | Client | Exercises |
|---|---|---|
| Outlook2007LineFolds.ics | Outlook 2007 (12.0) | Folding with TAB continuation (valid, but trips naive parsers), X-ALT-DESC, X-MS-OLK-*, CUTYPE=RESOURCE |
| XProperty2.ics | Outlook 2007 | X-MICROSOFT-*/X-MS-OLK-* properties |
| Bug2966236.ics | Outlook 2003 (11.0) | Recurring event with Outlook VTIMEZONE |
| Bug2912657.ics | Windows Calendar (Vista) | METHOD:PUBLISH, RRULE |
| Trigger1.ics | Sun Java Calendar Server | X-NSCP-* / X-S1CS-* params, ORGANIZER folded before `;`, RECURRENCE-ID, 20 VALARMs |
| Attachment3.ics | Mozilla Calendar V1.1 | ATTACH;VALUE=BINARY;ENCODING=BASE64 folded inline |
| Categories1.ics | Mozilla Calendar | `CATEGORIES;ENCODING=BASE64` (base64 on a TEXT property), EXDATE |
| Bug2148092.ics | Mozilla Calendar V1.0 | VTODO + 6 VEVENTs, non-ASCII |
| ALARM2.ics | Mozilla Calendar V1.1 | VALARM variant |
| Todo5.ics | Mozilla Calendar V1.1 | Recurring VTODO |
| Google1.ics | Google Calendar | RRULE, non-ASCII |
| Parse1.ics | Drupal iCal API | Web-generated feed |
| JOURNAL2.ics | RFC example (synthetic, "ABC Corporation") | VJOURNAL with multi-line DESCRIPTION |

## golang-ical/

- Upstream: https://github.com/arran4/golang-ical @ `f1ba710c2e179905ebf0635c6d994d725fd5078e`
- Licence: `Apache-2.0` — `LICENSE`
- Path upstream: `testdata/`
- **Renamed:** `thunderbird.ics` is `testdata/issue97/thunderbird.ics_disabled` upstream.

| File | Client | Exercises |
|---|---|---|
| issue52_notworking.ics | Google Calendar | Long unfolded ATTENDEE lines, X-RESPONSE-COMMENT quoted param, X-GOOGLE-CONFERENCE, umlauts (redacted) |
| google.ics | Google Calendar | Minimal Google export |
| thunderbird.ics | Thunderbird (DAVx5-style X-TZINFO zone) | VTIMEZONE with seconds offset, RDATE |

Moved to `invalid/golang-ical/thunderbird.ics`: upstream appended prose ("Disabled due to wordwrapping differences") after `END:VCALENDAR`, so it is not a valid resource. Kept to test that trailing junk is rejected.
| timeparsing.ics | DAVx5 4.0 / ical4j 3.1 | Five VEVENTs, DAVx5 PRODID without `-//` |

## khal/

- Upstream: https://github.com/pimutils/khal @ `3ba0fcef06d5244a4a7330869a8d5f18a0342ce9`
- Licence: `MIT` — `COPYING`
- Path upstream: `tests/ics/`

| File | Client | Exercises |
|---|---|---|
| non_dst_error.ics | NS Internationaal (Dutch rail booking) | Long VTIMEZONE history, VALARMs |
| event_dt_mixed_awareness.ics | ownCloud Calendar 0.7.3 | Floating vs. zoned DTSTART/DTEND in one event |
| event_rrule_recuid_cancelled.ics | khal | RRULE with a STATUS:CANCELLED RECURRENCE-ID override |
| tz_windows_format.ics | khal (Windows-style TZID) | `TZID:New Zealand Standard Time` (unquoted spaces in param), redundant `VALUE=DATE-TIME` on DTSTART |
| cal_lots_of_timezones.ics | khal | 7 VTIMEZONEs, RDATE, RECURRENCE-ID |

## node-ical/

- Upstream: https://github.com/jens-maus/node-ical @ `1930673781915be9aacadc50a504aac18282cde5`
- Licence: `Apache-2.0` — `LICENSE`
- Path upstream: `test/fixtures/`

| File | Client | Exercises |
|---|---|---|
| tentative-apple-calendar.ics | macOS 11.6.7 Calendar | SCHEDULE-AGENT=CLIENT, EMAIL param, PARTSTAT incl. lowercase `cancelled`, X-APPLE-TRAVEL-ADVISORY-BEHAVIOR |
| vtodo-vfreebusy.ics | Apple iCal 1.0 (+ RFC snippets) | VEVENT, VTODO with AUDIO VALARM + ATTACH URI, VJOURNAL and VFREEBUSY in one calendar |
| Office-2012-owa.ics | Exchange 2010 OWA | Display-name TZIDs (`(UTC-05:00) Eastern Time (US & Canada)`) with no VTIMEZONE, leading space in SUMMARY |
| ms_timezones.ics | Exchange 2010 | 8 Windows VTIMEZONEs, EXDATE |
| exchange_whole_day_moved_recurrence.ics | Exchange 2010 | All-day recurrence with a moved RECURRENCE-ID instance |
| outlook-custom-tz-exdate.ics | Exchange 2010 | "Customized Time Zone" + EXDATE |
| bad_ms_tz.ics | Exchange 2010 | `TZID=Customized Time Zone` unquoted, X-MICROSOFT-CDO-* |
| vfreebusy-zimbra.ics | Zimbra | Published VFREEBUSY |
| vtimezone-year-0001-dtstart.ics | eM Client | VTIMEZONE DTSTART in year 0001, non-ASCII |
| sabredav-school-holidays.ics | SabreDAV / Nextcloud server | X-APPLE-* on server-stored object |
| test_date_time_duration.ics | Synology Calendar | DURATION instead of DTEND |
| meetup-timed-tz.ics | Meetup.com | Feed with VTIMEZONE |
| google-recurrence-order.ics | Google Calendar | RECURRENCE-ID instance before its master |
| utf8-french-calendar.ics | ownCloud Calendar 0.6.3 | VTODO, French UTF-8 text |
| tripit-location-escaping.ics | TripIt (Bennu) | 47 VEVENTs, heavy escaping in LOCATION/DESCRIPTION |
| test_with_multiple_tzids_in_vtimezone.ics | The Events Calendar (WordPress plugin) | Two STANDARD blocks, non-ASCII |

## icalendar-rb/

- Upstream: https://github.com/icalendar/icalendar @ `41d388974757e871da735d8d36a1b4ab0c481b9d`
- Licence: `BSD-2-Clause` (gem offers `BSD-2-Clause OR GPL-3.0-only OR Ruby`-style terms; we take BSD-2-Clause) — `LICENSE`, `icalendar.gemspec`
- Path upstream: `spec/fixtures/`

| File | Client | Exercises |
|---|---|---|
| two_date_time_events.ics | macOS 10.9.4 Calendar | VALARMs, X-APPLE-*, RRULE |
| custom_component.ics | Atlassian Confluence Calendar | Custom `X-EVENT-SERIES` component, RDATE |
| tz_store_param_bug.ics | Exchange 2010 | METHOD:COUNTER |
| reversed_timezone.ics | unknown (no PRODID) | VTIMEZONE whose sub-components are in reverse chronological order |

## cyrus/

- Upstream: https://github.com/cyrusimap/cyrus-imapd @ `804283d9b4d02446ac6a241893f75895b6b37a2c`
- Licence: `BSD-Attribution-HPND-disclaimer` (the CMU licence) — `COPYING`
- Path upstream: `cassandane/data/icalendar/`
- These started as macOS Calendar 10.9.5 output and were hand-edited by the Cyrus developers to exercise JMAP Calendar mapping: many carry `X-JMAP-*` parameters, and some long lines are left unfolded. Good for parameter-heavy round-trips; not pristine Apple output.

| File | Client | Exercises |
|---|---|---|
| alerts.ics | macOS 10.9.5 (edited) | 6 VALARM variants: EMAIL with ATTENDEE, VALARM UID / X-WR-ALARMUID, ACKNOWLEDGED, snoozed alarm via RELATED-TO;RELTYPE=PARENT, absolute TRIGGER |
| attendeedir.ics | macOS 10.9.5 (edited) | ATTENDEE DIR param |
| participants.ics | macOS 10.9.5 (edited) | ATTENDEE params DELEGATED-TO/-FROM, MEMBER, ROLE, PARTSTAT, quoted X-params, long unfolded lines |
| links.ics | macOS 10.9.5 (edited) | ATTACH by URI with FMTTYPE, SIZE and quoted X- params |
| locations-apple.ics | macOS 10.9.5 (edited) | X-APPLE-STRUCTURED-LOCATION with X-TITLE, X-APPLE-RADIUS |
| location-newline.ics | macOS 10.15.6 | Escaped newline in LOCATION, CRLF |
| recurrenceoverrides.ics | macOS 10.9.5 (edited) | RRULE + RDATE + EXDATE + RECURRENCE-ID overrides with VALARMs |
| relatedto.ics | macOS 10.9.5 (edited) | RELATED-TO with FIRST/NEXT, unknown X- RELTYPEs, and the RELTYPE param repeated on one property |
| rscale.ics | macOS 10.9.5 (edited) | RRULE RSCALE (RFC 7529) |
| rsvpsequence.ics | macOS 10.9.5 (edited) | ATTENDEE RSVP, SEQUENCE across master and RECURRENCE-ID override |
| translations.ics | macOS 10.9.5 (edited) | LANGUAGE param on SUMMARY, X- params on LOCATION |

## calendarserver/

- Upstream: https://github.com/apple/ccs-calendarserver @ `13c706b985fb728b9aab42dc0fef85aae21921c3`
- Licence: `Apache-2.0` — `LICENSE.txt`
- Path upstream: `twistedcaldav/test/data/`

| File | Client | Exercises |
|---|---|---|
| Holidays.ics | Apple iCal 2.0 | 47-event US holiday calendar, yearly RRULEs |
| OneEvent.ics | Apple iCal 4.0.4 | Single event with VTIMEZONE |
| TruncatedApr01.ics | Apple iCal 3.0 | METHOD:PUBLISH, X-WR-* |
| dropbox-attachment.ics | Apple iCal 4.0.1 | Apple's legacy "dropbox" attachments: X-APPLE-DROPBOX and a relative `ATTACH;VALUE=URI`. Path upstream: `txdav/caldav/datastore/test/calendar_store/ho/me/home_attachments/calendar_1/1.ics`; the upstream's `%(now)s` year placeholder is replaced by `2009`, nothing else changed |
| managed-attachment.ics | CalendarServer | A managed attachment (RFC 8607) as CalendarServer writes it for Apple clients: `ATTACH;FILENAME;FMTTYPE;MANAGED-ID;SIZE`. Extracted from the `data_attach_1` template in `txdav/caldav/datastore/test/test_sql.py` with its placeholders filled in (times, a MANAGED-ID and a URL); LF endings as upstream |

## ical-expander/

- Upstream: https://github.com/mifi/ical-expander @ `94135b34b6c352a3f8d1b08012d1b2cd1facd0c0`
- Licence: `MIT` — `LICENSE`
- Path upstream: `test/`

| File | Client | Exercises |
|---|---|---|
| icaljs-issue-285.ics | macOS 10.12.2 Calendar | RECURRENCE-ID override, X-APPLE-* |
| all-day.ics | macOS 10.13.3 Calendar | All-day recurring events |

## rfc5546/

- Upstream: RFC 5546 (iTIP), section 4, https://www.rfc-editor.org/rfc/rfc5546.txt
- Licence: `BSD-2-Clause` — code components of an IETF RFC (IETF Trust Legal Provisions, section 4.e)
- Extracted byte for byte from the text: the six-space indentation and page breaks removed, lines joined with CRLF. Synthetic, like the other RFC examples here, but they are what the scheduling code (`internal/dav/itip`) is written against.

| File | Section | Exercises |
|---|---|---|
| 4.2.2-reply.ics | 4.2.2 | REPLY accepting a group event, REQUEST-STATUS |
| 4.2.3-update.ics | 4.2.3 | REQUEST moving an event (SEQUENCE 1), folded ATTENDEE parameters |
| 4.4.1-recurring-request.ics | 4.4.1 | Recurring REQUEST with a client's own VTIMEZONE, RDATE and EXDATE; attendee addresses without a scheme |
| 4.4.2-request.ics | 4.4.2 | Monthly series REQUEST with UNTIL |
| 4.4.2-modify-instance.ics | 4.4.2 | REQUEST of one override (RECURRENCE-ID) of that series |
| 4.4.3-cancel-instance.ics | 4.4.3 | CANCEL of one instance |
| 4.4.4-cancel-series.ics | 4.4.4 | CANCEL of the whole series |
| 4.4.5-this-and-future-range.ics | 4.4.5 | REQUEST changing all future instances: the RFC's example with `RANGE=` added to `RECURRENCE-ID;THISANDFUTURE`, as RFC 5545 §3.8.4.4 spells it (the original is under `quirks/rfc5546/`) |

## rfc8607/

- Upstream: RFC 8607 (CalDAV Managed Attachments), https://www.rfc-editor.org/rfc/rfc8607.txt
- Licence: `BSD-2-Clause` — code components of an IETF RFC (IETF Trust Legal Provisions, section 4.e)
- Extracted as `rfc5546/` was: indentation and page breaks removed, lines joined with CRLF. The managed attachment code (`internal/dav/attach.go`) is written against them.

| File | Section | Exercises |
|---|---|---|
| 3.4-add.ics | 3.4 | Event after `attachment-add`: ATTACH with MANAGED-ID, FMTTYPE, SIZE and FILENAME, folded |
| 3.5-update.ics | 3.5 | The same after `attachment-update`: a new MANAGED-ID and SIZE |
| A-precondition-failed.ics | Appendix A | Weekly series with a client's own VTIMEZONE, before any attachment |
| A-add-recurring.ics | Appendix A | The series with an attachment on the master |
| A-add-instance.ics | Appendix A | An override created by `rid=20120220T100000`, carrying its own attachment |

---

## quirks/

Real-world output that is not valid RFC 5545. Same upstreams and licences as
above (see the matching section for repo, commit and licence file).

### quirks/ical4j/ (BSD-3-Clause, ical4j @ `9ef69b07`)

| File | Client | Quirk |
|---|---|---|
| boeing.ics | Outlook 2003 (11.0) | VFREEBUSY with 7-digit dates (`2006717T080000Z` — month not zero-padded); no CRLF after final END |
| tmeher.ics | Exchange CDO | `CLASS:` with empty value; `\N` (uppercase) newline escapes; TZID with `\,` in VTIMEZONE but quoted with plain `,` in params; ATTENDEE param folded mid-value |
| sogo-geo-escaped-semicolon.ics | SOGo 3.2.10 | `GEO:12.34567\;12.34567` — backslash-escaped structured separator |
| korganizer.ics | KOrganizer 3.3.2 | Blank lines inside VCALENDAR and after END:VCALENDAR |
| yahoo1.ics (transform/yahoo1.txt, renamed) | Yahoo Calendar | `DTSTART;TZID=Europe/London:20140130T140000Z` (TZID plus UTC `Z`), `ROLE=REQ_PARTICIPANT` (underscore), PRODID without leading `-` |
| yahoo2.ics (transform/yahoo2.txt, renamed) | Yahoo Calendar | Same TZID+`Z` quirk across several events, VALARMs |
| TZ-no-description.ics (transform/outlook/TZ-no-description.txt, renamed) | Outlook 2013 (15.0) | `VTIMEZONE` named `(no TZ description)` whose only STANDARD has `TZOFFSETFROM/TO:-0000` (negative zero, forbidden by RFC 5545); RECURRENCE-ID overrides, VALARMs, non-ASCII |

### quirks/icalnet/ (MIT, ical.net @ `ab7d9e2d`)

| File | Client | Quirk |
|---|---|---|
| Bug2959692.ics | Outlook 2003 (11.0) | `PRODID:...//EN VERSION:2.0` — VERSION run into the PRODID line, no VERSION property; no CRLF after final END |
| EmptyLines1.ics | Mozilla Calendar V1.0 | Empty lines between components and after END:VCALENDAR |

### quirks/python-icalendar/ (BSD-2-Clause, python icalendar @ `0a16c354`)

| File | Client | Quirk |
|---|---|---|
| issue_165_missing_event.ics | Exchange CDO | `BYDAY=MO, TU, WE, TH, FR` (spaces in RRULE list), RRULE folded |
| issue_168_input.ics | Apple (X-APPLE-RADIUS) | `X-APPLE-RADIUS=49.91…` — property line with `=` and no `:` |
| multiple_calendar_components.ics | Mozilla Calendar V1.0 | Every property split as `NAME` / blank line / ` :value` (empty line inside a fold) |
| bom_calendar.ics | unknown | UTF-8 BOM before BEGIN:VCALENDAR (common from Windows exporters) |

### quirks/py-vobject/ (Apache-2.0, py-vobject @ `680ed60e`)

| File | Client | Quirk |
|---|---|---|
| ruby_rrule.ics | LinkeSOFT DIMEX | `RRULE:FREQ=DAILY\;COUNT=10` — escaped semicolon in a RECUR value; DTSTAMP without `Z` |
| vobject_0050.ics | Salesforce (Force.com Labs) | Leading blank line before BEGIN:VCALENDAR; `METHOD: REQUEST` and `DTEND: 2024…` with a space after the colon; both DTEND and DURATION |

### quirks/icalendar-rb/ (BSD-2-Clause, icalendar-rb @ `41d38897`)

| File | Client | Quirk |
|---|---|---|
| bad_wrapping.ics | hand-made reproduction (PRODID `manual`) | Line folded in the middle of a UTF-8 multi-byte sequence (octet-counting folders such as Outlook do this) |

### quirks/ical-expander/ (MIT, ical-expander @ `94135b34`)

| File | Client | Quirk |
|---|---|---|
| icaljs-issue-257.ics | InfCloud 0.12.1 (from Thunderbird data, X-MOZ-GENERATION) | Blank lines between components; PRODID after the components |
| invalid_dates.ics | InfCloud 0.12.1 | Blank lines between components; `DTSTART;TZID=America/Chicago:20170325` (DATE value with TZID, no VALUE=DATE) |

### quirks/rfc5546/ (BSD-2-Clause, RFC 5546 section 4)

| File | Section | Quirk |
|---|---|---|
| 4.2.1-request.ics | 4.2.1 | `DTEND:19970701T2100000Z` has seven time digits; a room attendee without a scheme |
| 4.2.9-cancel.ics | 4.2.9 | `ATTENDEE;CUTYPE=INDIVIDUAL;mailto:a@example.com`: the value's colon is missing, so the address reads as a parameter |
| 4.4.5-this-and-future.ics | 4.4.5 | `RECURRENCE-ID;THISANDFUTURE`: a bare parameter where RFC 5545 has `RANGE=THISANDFUTURE` |

## cog-production

Anonymised samples of the calendars on cog.duckfam.us (2026-10-25), produced with `go tool davfixtures export` and `anonymise`. Text, names and contact details are placeholders; structure, times, recurrence, VTIMEZONEs and alarms are as stored. Licence: same as this repository.

All 186 production events came from one import (`PRODID:-//duckfam//fossify-import//EN`), not from a DAV client, so they share one shape. One stored DESCRIPTION has a continuation line without a leading space (a broken fold); `contentline` keeps it as a malformed property, and it is kept here (as `\nMalformed continuation N`) as a real example.

| File | Exercises |
|---|---|
| event-01.ics … event-05.ics | RRULE with BYDAY/UNTIL/COUNT, long EXDATE lists, VALARM, all-day VALUE=DATE, America/Denver VTIMEZONE, LOCATION/DESCRIPTION, the broken fold |
