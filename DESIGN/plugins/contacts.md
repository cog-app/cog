# Contacts Plugin Design

The Contacts plugin (`id: contacts`) provides address book management and full CardDAV compatibility for NextCloud clients, mobile devices, and desktop contact apps.

## 1. Role and Loading Model

- **Plugin Type:** 1st-party plugin.
- **Recommended Deployment:** Privileged internal plugin for global CardDAV route compatibility.
- **Storage:** `app_data/contacts/plugin.db` per user.
- **Primary Protocol:** CardDAV (RFC 6352) over WebDAV.

## 2. Data Model

- **Address Books:** DAV collections owned by the user, kept by the DAV core in the system database (`dav_collections`); shared as `dav:collection`.
- **Cards:** rows in the plugin database (`cards` and its child tables), with the vCard text generated on read. Anything Cog does not model is kept and written back. See [dav-storage.md](../dav-storage.md) section 4.4.

## 3. CardDAV Compatibility

### Endpoints
- Discovery redirects from `/.well-known/carddav`.
- Principal: `remote.php/dav/principals/users/{username}/`
- Addressbook home: `remote.php/dav/addressbooks/{username}/`
- Book collection: `remote.php/dav/addressbooks/{username}/{book_id}/`

### DAV Operations
- `PROPFIND`: expose principal/addressbook metadata and capabilities.
- `REPORT`:
  - `addressbook-query`
  - `sync-collection`
  - `addressbook-multiget`
- `GET`: fetch `.vcf` card payloads.
- `PUT`: create/update cards with ETag preconditions.
- `DELETE`: remove cards and advance collection sync token.

### Sync Semantics
- Each address book maintains a monotonic `sync_token`.
- Any card create/update/delete increments the parent `sync_token`.
- Clients use `sync-collection` to fetch deltas by token.

## 4. API and UI

- **Plugin UI Pages:** `/apps/contacts/*`
- **Actions:** create/edit/delete cards and books, merge duplicates, import/export vCard.
- **Optional API:** `/api/contacts/*` for app integrations outside CardDAV.

## 5. Sharing and Resources

- Shareable resources:
  - `contacts:address_book` (recommended share boundary)
  - `contacts:card` (optional, advanced)
- Default permission model:
  - `READ`: view cards
  - `WRITE`: create/update/delete cards
  - `SHARE`: manage sharing
- Sharing resolution uses global `shares` and owner-DB scoped access rules.

## 6. Search Indexing

- Register a `SearchProvider` for cards.
- Suggested mapping:
  - `name`: `fn`
  - `subtext`: primary email or phone
  - Full-text fields: `fn`, first email, first phone, `org`, `note`
- Search indexing remains eventual-consistent via Collector pipeline.

## 7. Performance and Maintenance

- Use indexed columns for `uid`, `etag`, `uri`, and `address_book_id`.
- Keep DAV `REPORT` queries bounded and paginated where possible.
- Maintain a duplicate-detection job (for example same normalized email + name).
- Track metrics: CardDAV request latency, sync token lag, write conflicts, import failures.

## 8. Security

- Enforce strict ownership checks on principal and addressbook paths.
- Validate vCard payload size and allowed content types.
- Sanitize rendered contact fields in HTML views.
- Require conditional write checks (`If-Match`/`If-None-Match`) to prevent lost updates.

