# vCard fixture provenance

Every `.vcf` here is copied byte-for-byte from an upstream test suite: line
endings, charset, BOMs and folding are untouched. Each source directory carries
the upstream licence file. Files were only renamed where noted.

`quirks/` holds files that break RFC syntax in a way a real client does (or did).
A strict round-trip test should expect to handle them specially.

Licences are all permissive. GPL/AGPL/LGPL sources (Tine 2.0, Roundcube,
vcard4android, Radicale) are **not** in this tree; see "Local-only" at the end.

---

## ez-vcard/ (18 files)

- Upstream: https://github.com/mangstadt/ez-vcard
- Commit: `3d082a110624e3cdda666e6ccc653d25d115e489`
- Licence: BSD-2-Clause (`LICENSE`, upstream `LICENSE`)
- Paths: `src/test/resources/ezvcard/io/text/*.vcf`, `src/test/resources/ezvcard/issue/issue114.vcf`
- The `John_Doe_*` files came to ez-vcard from the Cardme project
  (https://sourceforge.net/projects/cardme/, BSD-licensed); see `vcard-samples-license.txt`.

| File | Client | Exercises |
|---|---|---|
| John_Doe_ANDROID.vcf | Android stock contacts (c. 2010) | 2.1, 6 cards, QP + `CHARSET=UTF-8`, bare `JPEG` param on PHOTO, unfolded base64 |
| John_Doe_BLACK_BERRY.vcf | BlackBerry | 2.1, base64 PHOTO without TYPE |
| John_Doe_EVOLUTION.vcf | GNOME Evolution | 3.0, `X-EVOLUTION-*`, comma TYPE lists |
| John_Doe_GMAIL.vcf | Gmail export (c. 2010) | 3.0, `itemN.` groups + `X-ABLabel`, `X-PHONETIC-*` |
| John_Doe_IPHONE.vcf | iOS 5.0.1 (`PRODID`) | 3.0, groups, `_$!<..>!$_` labels, large folded base64 PHOTO |
| John_Doe_LOTUS_NOTES.vcf | Labelled Lotus Notes, but `PRODID` says Apple Address Book 6.1 (probably passed through it) | 3.0, groups, PHOTO |
| John_Doe_MAC_ADDRESS_BOOK.vcf | Mac OS X Address Book | 3.0, `PHOTO;BASE64:` with Apple's indented folding, X-ABLabel free-text labels |
| John_Doe_MS_OUTLOOK.vcf | Microsoft Outlook | 2.1, QP, CHARSET, X-MS-*, base64 PHOTO |
| outlook-2003.vcf | Outlook 2003 | 2.1, QP soft line breaks in LABEL/NOTE |
| outlook-2007.vcf | Outlook 2007 | 2.1, QP, CHARSET, PHOTO, `X-MS-OL-DESIGN` XML blob |
| gmail-single.vcf | Gmail export | 3.0, groups, custom X-ABLabel values (GRAND_CENTRAL, PROFILE) |
| gmail-single2.vcf | Gmail export | 3.0, 25 item groups, every relation label, X-ABRELATEDNAMES, X-ABDATE |
| gmail-list.vcf | Gmail export | 3.0, 3 cards in one stream |
| thunderbird-MoreFunctionsForAddressBook-extension.vcf | Thunderbird + MoreFunctionsForAddressBook add-on | 3.0, CHARSET on 3.0, PHOTO, TYPE lists |
| issue114.vcf | Reporter's export (client unknown) | 4.0, RFC 6868 `^n`/`^'` caret encoding in a `LABEL=` param, folded inside a param value |
| fullcontact.vcf | ez-vcard 0.9.14 output for FullContact | 4.0, PHOTO as https URL, TYPE lists (library output) |
| rfc2426-example.vcf | RFC 2426 examples (synthetic) | 3.0, lower-case `begin:vcard`, group-prefixed properties |
| rfc6350-example.vcf | RFC 6350 example (synthetic) | 4.0 |

## aosp-vcard/ (26 files)

- Upstream: https://android.googlesource.com/platform/frameworks/opt/vcard
- Commit: `f049d8cd3a6579d140d22fcdcbdbdfd5f7c020e3`
- Licence: Apache-2.0. The repo has no LICENSE file; `Android.bp`/`tests/Android.bp` declare
  `Android-Apache-2.0` and every source file carries the Apache-2.0 header.
- Path: `tests/res/raw/`
- These are the Android vCard importer's tests. Many come from devices the Android team saw in the field.

| File | Client | Exercises |
|---|---|---|
| v21_complicated.vcf, v30_complicated.vcf | Synthetic, built on the RFC 2045 QP example | Many props, QP soft breaks, ORG with escaped `;`, base64 PHOTO, `X-CLASS` |
| v21_japanese_1/2/3.vcf, v30_japanese.vcf | Japanese feature phones | `CHARSET=SHIFT_JIS` raw bytes and QP, `SOUND;X-IRMC-N` |
| v21_multiple_entry.vcf | Japanese feature phone | 3 cards, Shift_JIS |
| v21_blackberry_photo.vcf | BlackBerry | 2.1 base64 PHOTO |
| v21_winmo_65.vcf | Windows Mobile 6.5 | `ANNIVERSARY;VALUE=DATE` in 2.1, a stray `AGENT:` text line, empty `X-` values |
| v21_x_android_custom_after_photo.vcf | Android export | `X-ANDROID-CUSTOM` right after a base64 PHOTO block |
| v21_im.vcf | Android export | `X-ANDROID-CUSTOM`, `X-GOOGLE-TALK` |
| v21_pref_handling.vcf, v21_org_before_title.vcf, v21_title_before_org.vcf | Synthetic | Repeated ORG/TITLE, bare PREF |
| v21_backslash.vcf | Synthetic | Backslash escaping in 2.1 compound values |
| v21_x_param.vcf | Synthetic | Mixed-case and non-`X-` unknown bare params |
| v21_sip.vcf, v30_sip.vcf, v40_sip.vcf | Android export | `X-SIP` / `IMPP:sip:` |
| v30_adr_types.vcf | Synthetic | ADR TYPE variants |
| v30_comma_separated.vcf | Synthetic | Quoted param value containing `,` and `:`, TYPE on N |
| v30_multibyte_param.vcf | Synthetic | Quoted UTF-8 multibyte TYPE value |
| v30_pager.vcf, v30_pause_wait.vcf, v30_simple.vcf | Android export | TYPE lists, `p`/`w` dial chars |
| v40_sort_as.vcf | Synthetic (Japanese) | 4.0 `SORT-AS` with quoted, `;`-containing UTF-8 values on N and ORG |

## ccs-caldavtester/ (15 files)

- Upstream: https://github.com/apple/ccs-caldavtester
- Commit: `bed21e5924275552c1561febc8203a9f194cf737`
- Licence: Apache-2.0 (`LICENSE`, upstream `LICENSE.txt`)
- Path: `Resource/CardDAV/`. Renamed `vcardput/N.vcf` -> `vcardput-N.vcf` and
  `sharing/groups/read-write/3.vcf` -> `sharing-groups-3.vcf` to avoid collisions.
- Apple's CardDAV conformance suite. The cards were exported from Mac OS X Address Book
  (10.6–10.8 era), some trimmed by hand. Per upstream `vcardput/ReadMe.txt`, `2` was made in Mulberry.

| File | Exercises |
|---|---|
| vcardput-2.vcf | Mulberry: `item1.ADR` + `X-ABADR`, `_$!<HomePage>!$_`, repeated `type=` params, UID with `:ABPerson` |
| vcardput-3.vcf | Company card, `X-ABShowAs:COMPANY` |
| vcardput-4.vcf | NOTE with a leading space |
| vcardput-5.vcf | `X-YAHOO`, `X-ABRELATEDNAMES` with `_$!<Friend>!$_` / `_$!<Assistant>!$_` |
| vcardput-8.vcf | Latin-extended UTF-8 |
| vcardput-9.vcf | Japanese UTF-8 |
| vcardput-10.vcf | Every custom field: X-AIM, X-ABDATE, X-ABRELATEDNAMES, `BDAY;value=date` |
| vcardput-11.vcf, vcardput-12.vcf | Folding inside an ADR component, escaped `,` and `\n` |
| vcardput-13.vcf | `X-APPLE-STRUCTURED-LOCATION;VALUE=URI:geo:` |
| vcardput-14.vcf | RFC 6868 caret escapes in a param (`^'`) |
| vcardput-16.vcf | Group card: `X-ADDRESSBOOKSERVER-KIND:group` + many `X-ADDRESSBOOKSERVER-MEMBER` (no FN/N) |
| vcardput-17.vcf, vcardput-19.vcf | Address Book 6.1 group cards; 19 has a non-URN member (`user03@example.com`) |
| sharing-groups-3.vcf | Address Book 6.1 group, LF-only line endings |

## folkerkinzel-vcards/ (10 files)

- Upstream: https://github.com/FolkerKinzel/VCards
- Commit: `345111f8a8cbfac509896c438900098ee0376538`
- Licence: MIT (`LICENSE`, upstream `LICENSE`)
- Path: `src/FolkerKinzel.VCards.Tests/TestFiles/`

| File | Client | Exercises |
|---|---|---|
| Outlook v2.vcf | Microsoft Outlook (German) | 2.1, `N;LANGUAGE=de`, QP LABEL with soft breaks, Windows-1252 8-bit, `X-MS-OL-*` |
| Photo Outlook v2.vcf | Microsoft Outlook | 2.1 base64 PHOTO ended by a blank line, long single-line `X-MS-OL-DESIGN;CHARSET=utf-8` XML |
| Photo v3.vcf | Thunderbird CardBook 54.4 | 3.0, `PHOTO;ENCODING=B;TYPE=JPEG`, UTF-8 |
| v4.vcf | Thunderbird CardBook 36.3 | 4.0, `PREF=1`, 2 cards |
| v3.vcf | Library test input (Google/Apple-style) | 3.0, `itemN.` groups with empty `X-ABLabel:`, `SOURCE;CONTEXT=VCARD` |
| v2_1.vcf | Library test input | 2.1, long UTF-8 QP NOTE with soft breaks, PHOTO |
| WhatsAppIssue.vcf | WhatsApp contact share (user report) | 3.0, `TYPE=WhatsApp`, `CHARSET=UTF-8` on 3.0, no trailing newline |
| NextCloudPhotoIssue2.vcf | Nextcloud (sabre/vobject 4.5.4), user report | UTF-8 BOM, `ADR` with 7 components, folded base64 PNG. **Renamed from `.txt`** |
| NextCloudPhotoIssue3.vcf | Nextcloud Contacts 6.0.0, user report | 4.0 `PHOTO:data:image/png;base64\,…` (escaped comma in a URI), 7-component ADR. **Renamed from `.txt`** |
| Utf-8-Greek.vcf | Library test input | 2.1 UTF-8 QP. Renamed from `Utf-8/Greek.vcf` |

## ical.js/ (4 files)

- Upstream: https://github.com/kewisch/ical.js
- Commit: `33ba266282fac7c721d792e04124513df73fe140`
- Licence: MPL-2.0 (`LICENSE`, upstream `LICENSE`)
- Path: `test/parser/`

| File | Exercises |
|---|---|
| escape_semicolon.vcf | Nextcloud / sabre/vobject 4.1.6 output: quoted `TYPE="HOME,VOICE"`, `\;\;\,\;` in TEL, upper-case `ITEM1.` group |
| vcard.vcf | Synthetic 4.0 with every RFC 6350 property, incl. `KIND:group` and `MEMBER` (only v4 group sample in this corpus) |
| vcard3.vcf | RFC 2426 examples: `PHOTO;VALUE=uri`, inline base64, AGENT, repeated N |
| vcard_grouped.vcf | Synthetic 4.0 upper-case `ITEM1.` group |

## vobject-py/ (1 file)

- Upstream: https://github.com/py-vobject/vobject
- Commit: `680ed60eed72d42fe2cbed62ca767320e433a8b4`
- Licence: Apache-2.0 (`LICENSE`, upstream `LICENSE`)
- `test_files/radicale-1587.vcf`: minimal 3.0 card with GEO, LF line endings (from a Radicale bug report).

## calcard/ (1 file)

- Upstream: https://github.com/stalwartlabs/calcard
- Commit: `5e645150e83e9b43b9bf29f52c157e1b7081ec09`
- Licence: Apache-2.0 OR MIT (`Apache-2.0.txt`, `MIT.txt`, upstream `LICENSES/`)
- `resources/vcard/077.vcf`: macOS 15.5 Contacts (`PRODID:-//Apple Inc.//macOS 15.5//EN`), anonymised
  (`{NAME}`), with an `item1.X-ABLabel:E-Mail` free-text label, `X-ABShowAs:COMPANY`, and
  `PHOTO;ENCODING=b;TYPE=JPEG` that actually holds PNG bytes. It is not attributed to another project in
  calcard's `resources/SOURCES.txt`, so it falls under calcard's own licence. The other calcard fixtures were
  not taken: most are copied from other projects, some GPL/AGPL, and their licensing is unclear.

---

## quirks/

### quirks/aosp-vcard/ (Apache-2.0, same commit as above)

| File | Quirk |
|---|---|
| v30_ios_613_multiline.vcf | iOS 6.1.3 export with **CR CR LF** line endings throughout, plus groups, X-ABLabel and base64 PHOTO. Android added a workaround for this. |
| v21_base64_no_2_crlf.vcf | 2.1 base64 PHOTO not ended by the blank line 2.1 requires; the next property follows straight on |
| v21_adr_multiple_line.vcf | 2.1 ADR continued across physical lines with a trailing `\` instead of folding (German "Packstation" address from a real export) |
| v21_invalid_multiple_line.vcf | EMAIL value continued on a line starting with two spaces: a fold that leaves a space in the value |

### quirks/ccs-caldavtester/ (Apache-2.0, same commit as above)

| File | Quirk |
|---|---|
| vcardput-6.vcf (76 KB) | Mac OS X Address Book `PHOTO;BASE64:`: continuation lines indented with **two** spaces and **spaces inside the base64** (`…AAx\n  sl Rx…`). Upstream path `Resource/CardDAV/vcardput/6.vcf` |

### quirks/folkerkinzel-vcards/ (MIT, same commit as above)

| File | Quirk |
|---|---|
| AnsiIssue.vcf | Real Outlook 2.1 export: raw Windows-1252 bytes in ADR with no CHARSET, `CHARSET=Windows-1252` only on the QP LABEL, empty trailing param (`ADR;HOME;PREF;:`), `X-MS-CARDPICTURE` |
| LabelIssue.vcf | Outlook-derived: several QP LABELs with soft breaks, some without CHARSET, raw 8-bit ADR |
| German.vcf, Greek.vcf, Ukrainian.vcf, Hebrew.vcf | Outlook-style 2.1 in legacy ANSI code pages (1252/1253/1251/1255): raw 8-bit FN/N/ADR without CHARSET, bare `QUOTED-PRINTABLE` param. Upstream path `MultiAnsiFilterTests/` |
| v3Utf16Bom.vcf | **UTF-16LE with BOM**, 3.0, 2 cards, CRLF. Upstream path `MultiAnsiFilterTests/` |

### quirks/sabre-vobject/

- Upstream: https://github.com/sabre-io/vobject
- Commit: `5e2f338811916c03927398bb24e527989de63194`
- Licence: BSD-3-Clause (`LICENSE`, upstream `LICENSE`)
- Path: `tests/VObject/`

| File | Quirk |
|---|---|
| issue153.vcf | 3.0 `PHOTO;BASE64:` (bare BASE64 param) with base64 continuation lines indented by **two** spaces (sabre issue #153) |
| issue64.vcf | 2.1 `PHOTO;ENCODING=BASE64;JPEG:` with the same two-space indented base64, LF endings (sabre issue #64) |

---

## Local-only (not committed): `tmp/dav-fixtures-gpl/vcf/`

Copyleft sources with real-client output, kept for local experiments only:

- `tine20/` (AGPL-3.0, https://github.com/tine20/tine20 @ `f05c103b4b2bd217775bbef4e681346e02adbdb4`,
  `tests/tine20/Addressbook/Import/files/`): eM Client 5, iOS 5.0.1, Mac OS X AddressBook 6.0,
  macOS 10.14.6, SOGo Connector, "WebDAV Collaborator" Outlook import, German 2.1 phone books.
- `roundcube/` (GPL-3.0, https://github.com/roundcube/roundcubemail @ `e46005c613dd67420594c40c3ae98ee9cf872283`,
  `tests/src/`): Apple (X-ABUID), The Bat!, a UTF-16 sample, and a photo card.
- `vcard4android/` (GPL-3.0, https://github.com/bitfireAT/vcard4android @ `7dbab269865e99eb4f46a25313d6397b51cd6ba8`,
  `lib/src/test/resources/`): DAVdroid 1.3.4 `strange-rev.vcf`, all-fields 3.0, unknown properties with labels.
- `radicale/` (GPL-3.0, https://github.com/Kozea/Radicale @ `4d3887e0fe5f4d3cf815d65e2a4d8bf9b0ca5b75`,
  `radicale/tests/static/`): DAVx5 3.3.5 and 4.2.6, CardBook 105.3, SOGo Connector, and data: URI photos (3.0 and 4.0).

## cog-production

Anonymised samples of the address book on cog.duckfam.us (2026-10-25), via `go tool davfixtures export` and `anonymise`. Production contacts are minimal (VERSION 3.0, UID, FN, REV). Licence: same as this repository.

| File | Exercises |
|---|---|
| contact-01.vcf, contact-02.vcf | minimal v3 cards as Cog stores them today |
