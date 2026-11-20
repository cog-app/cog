package files

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"
)

// Shared is what has been shared with a user, beside their own files: the
// folders, files and volumes others shared with them, by owner. A folder can
// be browsed there, or mounted in their files; a storage volume gives them
// room for areas of their own, each mounted in their files.
type Shared struct {
	Files   *Plugin
	Shares  *share.Store
	Users   *user.Store
	Volumes *volume.Store
	Access  *volume.Access
	Mounts  *mount.Store
}

// SharedKind is what a shared entry is.
type SharedKind string

const (
	SharedFolder SharedKind = "folder"
	SharedFile   SharedKind = "file"
	SharedVolume SharedKind = "volume"
)

// SharedEntry is one thing shared with a user.
type SharedEntry struct {
	Kind  SharedKind
	Owner *user.User
	// Name is unique among what Owner shared with the user: the entry's
	// place is /shared/<owner username>/<name>.
	Name  string
	Perms uint32

	// A folder or file: its ID and where it is in the owner's files.
	ResourceID string
	Path       string
	File       *FileMeta

	// A volume.
	Volume *volume.Volume

	// MountedAt is where a folder is mounted in the user's files, or the
	// areas they have in a storage volume.
	MountedAt []string
}

// Writable reports whether the user may change what the entry holds.
func (e *SharedEntry) Writable() bool { return e.Perms&plugin.PermWrite != 0 }

// IsStorage reports whether the entry is a storage volume, where the user
// can make areas of their own.
func (e *SharedEntry) IsStorage() bool {
	return e.Kind == SharedVolume && e.Volume.Kind == volume.Storage
}

// SharedOwner is the entries one person shared with the user.
type SharedOwner struct {
	User    *user.User
	Entries []SharedEntry
}

// List returns what was shared with u, by owner, owners and entries by name.
func (s *Shared) List(u *user.User) ([]SharedOwner, error) {
	records, err := seq.Collect(s.Shares.ListBySubject(u.ID))
	if err != nil {
		return nil, err
	}
	ownerIDs := make([]string, 0, len(records))
	for _, rec := range records {
		ownerIDs = append(ownerIDs, rec.OwnerUserID)
	}
	owners, err := s.Users.GetByIDs(ownerIDs)
	if err != nil {
		return nil, fmt.Errorf("owners of shares: %w", err)
	}

	type key struct{ owner, kind, id string }
	merged := map[key]*SharedEntry{}
	var order []key
	fileIDs := map[string][]int64{} // owner username -> shared file IDs
	for _, rec := range records {
		owner := owners[rec.OwnerUserID]
		if owner == nil || owner.ID == u.ID {
			continue // gone, or their own share with everyone
		}
		var kind SharedKind
		switch rec.ResourceType {
		case mount.SharedDirType:
			kind = SharedFolder
		case PluginID + ":file":
			kind = SharedFile
		case volume.ShareResourceType:
			kind = SharedVolume
		default:
			continue
		}
		k := key{owner.ID, string(kind), rec.ResourceID}
		if e := merged[k]; e != nil {
			e.Perms |= rec.Permissions
			continue
		}
		merged[k] = &SharedEntry{Kind: kind, Owner: owner, ResourceID: rec.ResourceID, Perms: rec.Permissions}
		order = append(order, k)
		if kind != SharedVolume {
			if id, err := strconv.ParseInt(rec.ResourceID, 10, 64); err == nil {
				fileIDs[owner.Username] = append(fileIDs[owner.Username], id)
			}
		}
	}

	metas := map[string]map[int64]*FileMeta{}
	for username, ids := range fileIDs {
		m, err := s.Files.GetByIDs(username, ids)
		if err != nil {
			return nil, fmt.Errorf("files %s shared: %w", username, err)
		}
		metas[username] = m
	}

	mounts, err := seq.Collect(s.Mounts.ListByUser(u.ID))
	if err != nil {
		return nil, err
	}

	byOwner := map[string]*SharedOwner{}
	var result []*SharedOwner
	for _, k := range order {
		e := merged[k]
		switch e.Kind {
		case SharedVolume:
			vol, err := s.Volumes.GetByID(e.ResourceID)
			if errors.Is(err, volume.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			// Only shares its owner (or an admin, for a system volume)
			// made count; Permissions says what they grant together.
			if e.Perms, err = s.Access.Permissions(u.ID, vol.ID); err != nil {
				return nil, err
			}
			if e.Perms == 0 {
				continue
			}
			e.Volume = vol
			e.Name = vol.Name
			for _, m := range mounts {
				if m.VolumeID == vol.ID && m.Kind != mount.Share && m.UserPath != "/" {
					e.MountedAt = append(e.MountedAt, m.UserPath)
				}
			}
		default:
			id, err := strconv.ParseInt(e.ResourceID, 10, 64)
			if err != nil {
				continue
			}
			meta := metas[e.Owner.Username][id]
			if meta == nil || meta.IsDir != (e.Kind == SharedFolder) {
				continue // gone
			}
			e.File, e.Path, e.Name = meta, meta.Path, meta.Name
			if e.Path == "/" {
				e.Name = e.Owner.Username
			}
			for _, m := range mounts {
				if m.Kind == mount.Share && m.ShareOwnerID == e.Owner.ID && m.ShareResourceID == e.ResourceID {
					e.MountedAt = append(e.MountedAt, m.UserPath)
				}
			}
		}
		o := byOwner[e.Owner.ID]
		if o == nil {
			o = &SharedOwner{User: e.Owner}
			byOwner[e.Owner.ID] = o
			result = append(result, o)
		}
		o.Entries = append(o.Entries, *e)
	}

	out := make([]SharedOwner, len(result))
	for i, o := range result {
		uniqueNames(o.Entries)
		out[i] = *o
	}
	slices.SortFunc(out, func(a, b SharedOwner) int { return strings.Compare(a.User.Username, b.User.Username) })
	return out, nil
}

// uniqueNames sorts entries by name and gives each a name of its own: the
// second "Photos" (by ID, so it keeps its name) is "Photos (2)".
func uniqueNames(entries []SharedEntry) {
	slices.SortStableFunc(entries, func(a, b SharedEntry) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		if c := strings.Compare(string(a.Kind), string(b.Kind)); c != 0 {
			return c
		}
		return compareIDs(a.ResourceID, b.ResourceID)
	})
	seen := map[string]int{}
	for i := range entries {
		base := entries[i].Name
		n := seen[strings.ToLower(base)] + 1
		seen[strings.ToLower(base)] = n
		if n > 1 {
			entries[i].Name = base + " (" + strconv.Itoa(n) + ")"
		}
	}
}

// compareIDs orders numeric IDs as numbers, others as text.
func compareIDs(a, b string) int {
	x, errA := strconv.ParseInt(a, 10, 64)
	y, errB := strconv.ParseInt(b, 10, 64)
	if errA == nil && errB == nil {
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	return strings.Compare(a, b)
}

// ErrNotShared is a shared entry that is not there: never shared, no longer
// shared, or gone.
var ErrNotShared = errors.New("nothing is shared there")

// Lookup returns what ownerName shared with u under name.
func (s *Shared) Lookup(u *user.User, ownerName, name string) (*SharedEntry, error) {
	owners, err := s.List(u)
	if err != nil {
		return nil, err
	}
	for _, o := range owners {
		if o.User.Username != ownerName {
			continue
		}
		for i := range o.Entries {
			if o.Entries[i].Name == name {
				return &o.Entries[i], nil
			}
		}
	}
	return nil, ErrNotShared
}

// Within returns where rel, a path inside a shared folder, is in its owner's
// files. It cannot leave the folder.
func (e *SharedEntry) Within(rel string) string {
	return path.Join(e.Path, path.Clean("/"+rel))
}

// FS returns a shared folder as a filesystem: the owner's files beneath it,
// kept as the plugin keeps them, read-only unless the share allows writing.
func (s *Shared) FS(e *SharedEntry) (vfs.FS, error) {
	if e.Kind != SharedFolder {
		return nil, fmt.Errorf("%s is not a folder: %w", e.Name, ErrNotShared)
	}
	fsys, err := s.Files.FS(e.Owner.Username)
	if err != nil {
		return nil, err
	}
	return vfs.Sub(fsys, e.Path, !e.Writable()), nil
}

// ErrMountPoint is a place in a user's files something cannot be mounted.
var ErrMountPoint = errors.New("cannot mount there")

// MountPoint checks userPath is somewhere in u's files a mount can go: not
// their root, the trash, or anything that is already there.
func (s *Shared) MountPoint(u *user.User, userPath string) (string, error) {
	userPath = path.Clean("/" + strings.TrimSpace(userPath))
	first, _, _ := strings.Cut(strings.TrimPrefix(userPath, "/"), "/")
	if userPath == "/" || first == vfs.TrashDir {
		return "", fmt.Errorf("%w: choose a folder inside your files", ErrMountPoint)
	}
	fsys, err := s.Files.userFS(u.Username)
	if err != nil {
		return "", err
	}
	if _, err := fsys.Stat(userPath); err == nil {
		return "", fmt.Errorf("%w: %s already exists; choose a new name", ErrMountPoint, userPath)
	} else if !errors.Is(err, vfs.ErrNotFound) && !errors.Is(err, iofs.ErrNotExist) {
		return "", err
	}
	if st, ok := fsys.(interface {
		ShareTarget(string) (string, string, bool)
	}); ok {
		if _, _, shared := st.ShareTarget(path.Dir(userPath)); shared {
			return "", fmt.Errorf("%w: %s is in a shared folder", ErrMountPoint, path.Dir(userPath))
		}
	}
	return userPath, nil
}

// MountFolder mounts a folder shared with u at userPath in their files.
func (s *Shared) MountFolder(u *user.User, e *SharedEntry, userPath string) (*mount.Mount, error) {
	if e.Kind != SharedFolder {
		return nil, fmt.Errorf("%w: only a shared folder can be mounted", ErrMountPoint)
	}
	userPath, err := s.MountPoint(u, userPath)
	if err != nil {
		return nil, err
	}
	return s.Mounts.CreateShare(u.ID, userPath, e.Owner.ID, e.ResourceID)
}

// AddArea makes (or finds again) an area of u's in the storage volume vol and
// mounts it at userPath. The area is <username>/<name> in the volume, the
// name taken from the folder it is mounted at.
func (s *Shared) AddArea(u *user.User, vol *volume.Volume, userPath string) (*mount.Mount, error) {
	ok, err := s.Access.MayMakeAreas(u.ID, vol)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s does not keep storage for you", ErrMountPoint, vol.Name)
	}
	userPath, err = s.MountPoint(u, userPath)
	if err != nil {
		return nil, err
	}
	return s.Mounts.CreateArea(u.ID, userPath, vol.ID, u.Username+"/"+path.Base(userPath))
}

// Unmount removes u's mount at userPath, leaving what it held where it is.
func (s *Shared) Unmount(u *user.User, userPath string) error {
	userPath = path.Clean("/" + userPath)
	if userPath == "/" {
		return fmt.Errorf("%w: your files are not a mount", ErrMountPoint)
	}
	m, err := s.Mounts.GetByUserPath(u.ID, userPath)
	if err != nil {
		return err
	}
	return s.Mounts.Delete(m.ID)
}
