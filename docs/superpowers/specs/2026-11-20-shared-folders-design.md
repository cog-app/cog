# Shared folders and user-storage volumes

## Why

Two people want one photo library on the HDD. Both back up phones into it with
the NextCloud app, and the recipient browses it with a NextCloud gallery app.
Once it is set up for her, she should never have to think about where it
lives.

Today that cannot be done:

- A folder share is recorded (`files:directory` in `shares`) but the
  recipient has nowhere to open it. There is no "Shared with me" page, nothing
  in her WebDAV tree, nothing in SSH. Only `GET /api/shares/with-me` (JSON)
  and a single-file download exist.
- A volume share lets the recipient mount the *whole* volume (or any subpath)
  from Settings → Storage. There is no way to give users their own private
  space on a volume, the way the default volume gives each user
  `<user>/files`.
- Nothing tells a recipient a volume was shared with them.

## Goals

1. **Volume kind.** A volume is either *raw access* (today's behaviour) or
   *user storage*. Admins set it in Admin → Volumes.
2. **Storage areas.** Sharing a user-storage volume with a user, group or
   everyone (`SubjectInstance`) lets them create storage areas on it: zero,
   one or many. An area lives at `<volume>/<username>/<name>` and is mounted
   at a folder of the user's choosing. They never see the volume root or
   anyone else's area.
3. **Shared with me.** A place beside the user's files (web sidebar, and
   `/shared` in SSH) listing everything shared with them, grouped by owner:
   folders, files, and volumes. Shared folders can be browsed (and written,
   if the share allows) there without mounting.
4. **Share mounts.** A recipient can mount a shared folder into their own
   tree. It then behaves as their own folder for every front end: web Files,
   WebDAV / NextCloud clients (sync, auto-upload, gallery apps), SSH, and
   in-process plugins that read `UserVFS` (Gallery). Both users see the same
   bytes.
5. **CLI.** `/shared/<owner>/<name>` in the SSH namespace, beside `/files`.
   `ln -s /shared/<owner>/<name> ~/Dest` creates a share mount; removing the
   link removes the mount.
6. **Quota is charged to the share owner** for the volume the bytes land on.

## Non-goals

- Federation / cross-server shares.
- Pending/accept flow. A share is available in Shared with me immediately;
  mounting is the recipient's explicit choice.
- Arbitrary symlinks. `ln` only creates mounts.
- Moving data on disk during conversion.

## Anti-goals (would be a failure even if it "works")

- A storage-volume user can reach the volume root, another user's area, or
  the server's own files by any route (web, WebDAV, OCS, SSH, SFTP, rsync,
  volume browser, path tricks like `..`).
- A share mount keeps working after the share is revoked, or stays writable
  after it is narrowed to read-only.
- Removing a mount deletes or trashes the files behind it.
- Bytes are counted twice (owner and recipient) or charged to the recipient.
- Per-request N+1 work: resolving a user's tree must stay one build per
  cache period, as `mount.Resolver` does today.

## Decided

### Volume kind

- New column on `volumes`: `kind TEXT NOT NULL DEFAULT 'raw'`, values `raw`
  and `storage`. The migration leaves every existing volume `raw`; nothing
  changes until an admin switches one.
- The default volume stays as it is (root mounts are not areas).
- Admins see any volume whole (Admin → Volumes). The owner of a user-created
  volume sees it whole. Everyone else on a `storage` volume sees only their
  areas.
- **Raw:** unchanged. A share allows mounting the volume or a subpath; a
  read-only share mounts read-only.
- **Storage:** any share (any permission bits) means "may create areas".
  Areas are always read-write for their owner.

### Areas

- An area is a mount row with a new marker (e.g. `kind = 'area'`) whose
  `volume_subpath` is `<username>/<name>`. `name` comes from the last
  element of the mount point the user picked (`/Backup/Photos` → `Photos`),
  suffixed (`Photos-2`, …) if that name is taken on that volume for that
  user. Renaming or moving the mount point later does not move it on disk.
- An area outlives nothing: while the user's share of the volume is gone,
  the area mount is skipped (like volume mounts are today); the files stay on
  disk.
- **Switching a volume raw → storage** converts each non-admin, non-owner
  mount of that volume into an area in place: same `volume_subpath`, no data
  moved. The `<username>/<name>` rule only applies to areas created after the
  switch. (Switching storage → raw: areas become ordinary raw mounts of the
  same subpath.)

### Shared with me

- Web: a "Shared with me" item in the Files sidebar (`/apps/files/shared`).
  The existing "Volumes" item keeps volumes the user owns or administers;
  volumes shared *to* the user move to Shared with me.
- Listing, grouped by owner display name: shared folders, shared files,
  shared volumes. Each entry has a stable name within its owner
  (`<owner username>/<name>`, suffixed on collision), used by both web URLs
  and `/shared` in SSH.
- A shared folder is browsable at `/apps/files/shared/<owner>/<name>/...`
  with the same listing/download/upload UI as Files, limited by the share's
  permissions. A shared file downloads/previews.
- A storage volume entry offers "Add storage" → pick a folder in your files →
  creates an area mount there. A raw volume entry offers mounting as today.
- A shared folder entry offers "Add to my files" → pick a destination →
  creates a share mount. Entries already mounted say where.
- Settings → Storage lists all the user's mounts (volume, area, share) with
  remove; removal only unmounts.

### Share mounts

- A mount row may point at a share instead of a volume: e.g. `share_id`
  nullable, `volume_id` nullable, exactly one set (new `kind = 'share'`).
- Resolution, at `UserFS` build time: share → owner → current path of the
  shared directory (by file ID in the owner's files DB; paths are mutable)
  → the owner's tree at that path. Permission comes from
  `share.CheckAccessGrantedBy` with the owner as grantor, re-checked on each
  build exactly as volume mounts are: no read → skipped, no write →
  read-only.
- If the shared directory no longer exists, the mount is skipped (and shown
  as broken in Settings → Storage), not an error for the whole tree.
- The owner's tree may itself contain share mounts (reshares). Resolution
  must not recurse unboundedly: a cycle (A mounts B's folder that contains a
  mount of A's) must terminate with the inner mount skipped.
- Cache invalidation: share create/delete/update and owner renames/moves of
  shared folders must invalidate affected recipients' cached VFS (or all,
  as `InvalidateAll` does); `cacheTTL` remains the backstop.
- Allowed mount points: same rules as today (not `/`, not the trash), plus
  not inside another share mount.

### SSH / CLI

- `SessionFS` mounts a read-through view at `/shared` beside `/files`:
  `/shared/<owner>/<name>` per entry in Shared with me. Shared folders are
  browsable/writable per share; shared files readable; volumes appear as
  empty directories (nothing to browse).
- New `ln` command: `ln -s <target> <link>` where target is under
  `/shared/<owner>/<name>` (folder share or storage volume) and link is under
  `/files`. Folder → share mount; storage volume → new area at that path.
  Anything else errors with a message saying only shared folders/volumes can
  be linked. `ls -l` shows mounts as links (`-> /shared/...`).
- `rm`/`unlink` on a mount point removes the mount, never the contents.
- SFTP/rsync see the same `/shared` (same `SessionFS`).

### Quota

- Bytes count against the **owner of the area / shared folder**, on the
  **volume they land on**, against `Quotas.EffectiveQuota(owner, volumeID,
  groups)`.
- A recipient's writes through a share mount are checked against the
  owner's quota for that volume.
- A user's usage must not include bytes under share mounts, and usage on the
  default volume must not include bytes in areas on other volumes.
- OCS `quota` reports the default-volume figure as today.

### NextCloud compatibility

- `shared_with_me=true` OCS listings set `file_target` / `path` to the mount
  point when the recipient has mounted the share, so clients see it in place.
- WebDAV PROPFIND under a share mount reports the owner (`oc:owner-id`,
  `oc:owner-display-name`) and permissions from the share (no `R` unless
  reshare is granted; no write letters when read-only).

## Left to the builder

- Exact schema shape (columns vs. a separate table) for mount kinds and
  share targets, provided one indexed query loads a user's mounts.
- How usage is computed per volume (e.g. grouping files-DB rows by mount, or
  tracking per-area totals) — must stay correct when a recipient writes into
  an owner's folder and the owner has not listed it since.
- How the owner's path for a shared directory is resolved from
  `internal/mount` without importing the files plugin (an injected lookup
  func wired in `services` is the expected shape).
- Web page layout, within the existing Files layout and `ui` components.
- Naming/suffix scheme for Shared-with-me entries and areas.
- Trash for deletes under share mounts and areas: follow what volume mounts
  do today, provided trashed bytes are never charged to a recipient.

## Builder-check notes (settled as my calls)

- The volume browser (`/apps/files/volumes/...`) must apply the same rule:
  on a `storage` volume, non-admin non-owners get nothing there; their areas
  are reached through their own tree.
- A storage volume's Shared-with-me entry lists the user's existing areas on
  it, mounted or not; adding storage can reattach an unmounted area instead
  of creating a new one.
- Resharing something under a share mount requires the Reshare bit on the
  share; without it the share dialog refuses.

## My calls (not discussed, check these)

- Existing volumes migrate as `raw`; switching kind converts mounts in place.
- Storage-volume shares are always read-write for one's own areas.
- "Volumes" sidebar keeps owned/administered volumes; shared ones move to
  Shared with me.
- Missing shared directory → mount skipped and shown broken, not deleted.
- SFTP/rsync get `/shared` too.
- `ln` on a storage volume creates an area (the CLI way to "Add storage").
- No accept step for shares.
