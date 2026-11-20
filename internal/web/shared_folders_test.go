package web_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/mount"
	cogssh "git.duckfam.us/jj-software/cog/internal/ssh"
	"git.duckfam.us/jj-software/cog/internal/ssh/shell"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/vfs"
)

// photoLibrary is the setup the feature is for: an admin keeps a user
// storage volume on another disk and shares it with everyone; jonathan keeps
// his photos in an area of it and shares that folder with wife, who mounts it.
type photoLibrary struct {
	env      *cogtest.Env
	fp       *files.Plugin
	admin    *cogtest.User
	jonathan *cogtest.User
	wife     *cogtest.User
	vol      *volume.Volume
	dir      string // the volume on disk
	share    string // jonathan's share of /Photos with wife
}

func newPhotoLibrary(t *testing.T, perms uint32) *photoLibrary {
	t.Helper()
	env := cogtest.New(t)
	l := &photoLibrary{env: env, fp: env.Providers.Shared.Files}
	l.admin = env.CreateUser(t, "admin", "pass")
	l.jonathan = env.CreateUser(t, "jonathan", "pass")
	l.wife = env.CreateUser(t, "wife", "pass")
	require.True(t, l.admin.IsAdmin)
	require.False(t, l.jonathan.IsAdmin)

	l.dir = t.TempDir()
	vol, err := env.Providers.Volumes.Create("hdd", "", "file://"+l.dir, nil, "{}")
	require.NoError(t, err)
	require.NoError(t, env.Providers.Volumes.SetKind(vol.ID, volume.Storage))
	l.vol, err = env.Providers.Volumes.GetByID(vol.ID)
	require.NoError(t, err)
	_, err = env.Providers.Shares.Create(l.admin.ID, volume.ShareResourceType, vol.ID, "INSTANCE", "", plugin.PermRead)
	require.NoError(t, err)

	m, err := env.Providers.Shared.AddArea(l.jonathan.User, l.vol, "/Photos")
	require.NoError(t, err)
	require.Equal(t, mount.Area, m.Kind)
	require.Equal(t, "jonathan/Photos", m.VolumeSubpath)

	_, err = l.fp.Upload("jonathan", "/Photos/beach.jpg", strings.NewReader("sand"))
	require.NoError(t, err)
	photos, err := l.fp.GetByPath("jonathan", "/Photos")
	require.NoError(t, err)
	rec, err := env.Providers.Shares.Create(l.jonathan.ID, mount.SharedDirType, files.IDString(photos), "USER", l.wife.ID, perms)
	require.NoError(t, err)
	l.share = rec.ID
	return l
}

// mountForWife puts jonathan's photos in wife's files at /Photos.
func (l *photoLibrary) mountForWife(t *testing.T) {
	t.Helper()
	e, err := l.env.Providers.Shared.Lookup(l.wife.User, "jonathan", "Photos")
	require.NoError(t, err)
	_, err = l.env.Providers.Shared.MountFolder(l.wife.User, e, "/Photos")
	require.NoError(t, err)
}

// listNames lists dir in username's files, by name.
func listNames(t *testing.T, fp *files.Plugin, username, dir string) []string {
	t.Helper()
	items, err := fp.List(username, dir)
	require.NoError(t, err)
	var out []string
	for _, f := range items {
		out = append(out, f.Name)
	}
	return out
}

func TestStorageAreaIsPrivate(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead)

	// The bytes are on the volume, in jonathan's area.
	data, err := os.ReadFile(filepath.Join(l.dir, "jonathan", "Photos", "beach.jpg"))
	require.NoError(t, err)
	require.Equal(t, "sand", string(data))

	// Sharing the volume let wife keep storage there, not look at it.
	perms, err := l.env.Providers.Access.WholePermissions(l.wife.ID, l.vol)
	require.NoError(t, err)
	require.Equal(t, uint32(0), perms)
	cookie, _ := sessionAndCSRF(t, l.env, l.wife.ID)
	l.env.Get(t, "/volumes/"+l.vol.ID+"/browse", cookie).Status(http.StatusForbidden)

	// A mount of the whole volume does nothing for her.
	_, err = l.env.Providers.Mounts.Create(l.wife.ID, "/hdd", l.vol.ID, "", false)
	require.NoError(t, err)
	_, err = l.fp.List("wife", "/hdd")
	require.NoError(t, err)
	exists, err := l.fp.Exists("wife", "/hdd/jonathan/Photos/beach.jpg")
	require.NoError(t, err)
	require.False(t, exists)

	// The admin sees it whole.
	perms, err = l.env.Providers.Access.WholePermissions(l.admin.ID, l.vol)
	require.NoError(t, err)
	require.Equal(t, volume.OwnerPerms, perms)
}

func TestStorageAreaNamesAndReattach(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead)
	sh := l.env.Providers.Shared

	m, err := sh.AddArea(l.jonathan.User, l.vol, "/Backup/Photos")
	require.NoError(t, err)
	require.Equal(t, "jonathan/Photos-2", m.VolumeSubpath)

	// Unmounted, the area stays on disk, and the same name brings it back.
	require.NoError(t, sh.Unmount(l.jonathan.User, "/Photos"))
	_, err = os.Stat(filepath.Join(l.dir, "jonathan", "Photos", "beach.jpg"))
	require.NoError(t, err)
	m, err = sh.AddArea(l.jonathan.User, l.vol, "/Photos")
	require.NoError(t, err)
	require.Equal(t, "jonathan/Photos", m.VolumeSubpath)
	require.Equal(t, []string{"beach.jpg"}, listNames(t, l.fp, "jonathan", "/Photos"))

	// Somewhere that is taken is refused.
	_, err = sh.AddArea(l.jonathan.User, l.vol, "/Photos/beach.jpg")
	require.True(t, errors.Is(err, files.ErrMountPoint), "want files.ErrMountPoint")
	_, err = sh.AddArea(l.jonathan.User, l.vol, "/")
	require.True(t, errors.Is(err, files.ErrMountPoint), "want files.ErrMountPoint")
}

func TestSharedFolderMountWritesAsOwner(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead|plugin.PermWrite)
	l.mountForWife(t)

	require.Equal(t, []string{"beach.jpg"}, listNames(t, l.fp, "wife", "/Photos"))

	// Her phone backs up into it: the owner's files gain it, on the volume.
	_, err := l.fp.Upload("wife", "/Photos/kids.jpg", strings.NewReader("smiles"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(l.dir, "jonathan", "Photos", "kids.jpg"))
	require.NoError(t, err)
	meta, err := l.fp.GetByPath("jonathan", "/Photos/kids.jpg")
	require.NoError(t, err)
	require.Equal(t, int64(6), meta.Size)
	_, err = l.fp.CreateDir("wife", "/Photos/2026")
	require.NoError(t, err)
	_, err = l.fp.GetByPath("jonathan", "/Photos/2026")
	require.NoError(t, err)

	// The bytes count against jonathan on the volume, not against her.
	byVol, root, err := l.fp.UsageByVolume("jonathan")
	require.NoError(t, err)
	require.Equal(t, int64(len("sand")+len("smiles")), byVol[l.vol.ID])
	require.Equal(t, int64(0), byVol[root])
	byVol, _, err = l.fp.UsageByVolume("wife")
	require.NoError(t, err)
	require.Equal(t, int64(0), byVol[l.vol.ID])

	// A rename moves the owner's file, keeping its ID.
	_, err = l.fp.Rename("wife", "/Photos/kids.jpg", "/Photos/2026/kids.jpg")
	require.NoError(t, err)
	moved, err := l.fp.GetByPath("jonathan", "/Photos/2026/kids.jpg")
	require.NoError(t, err)
	require.Equal(t, meta.ID, moved.ID)

	// What she deletes goes to his trash, his to restore.
	require.NoError(t, l.fp.Trash("wife", "/Photos/2026/kids.jpg"))
	trash, err := l.fp.ListTrash("jonathan")
	require.NoError(t, err)
	require.Equal(t, 1, len(trash))
	require.Equal(t, "kids.jpg", trash[0].OriginalName)
	trash, err = l.fp.ListTrash("wife")
	require.NoError(t, err)
	require.Equal(t, 0, len(trash))

	// The mount point itself is unmounted, not deleted.
	require.Error(t, l.fp.Trash("wife", "/Photos"))
	require.Error(t, l.fp.HardDelete("wife", "/Photos"))
	_, err = l.fp.Rename("wife", "/Photos", "/Pictures")
	require.Error(t, err)
	// Nor can anything leave it by a rename.
	_, err = l.fp.Rename("wife", "/Photos/beach.jpg", "/beach.jpg")
	require.True(t, errors.Is(err, vfs.ErrCrossMount), "want vfs.ErrCrossMount")
}

func TestSharedFolderMountReadOnly(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead)
	l.mountForWife(t)

	require.Equal(t, []string{"beach.jpg"}, listNames(t, l.fp, "wife", "/Photos"))
	_, err := l.fp.Upload("wife", "/Photos/kids.jpg", strings.NewReader("smiles"))
	require.True(t, errors.Is(err, vfs.ErrReadOnly), "want vfs.ErrReadOnly")
	require.True(t, errors.Is(l.fp.Trash("wife", "/Photos/beach.jpg"), vfs.ErrReadOnly), "want vfs.ErrReadOnly")
	_, err = l.fp.CreateDir("wife", "/Photos/new")
	require.True(t, errors.Is(err, vfs.ErrReadOnly), "want vfs.ErrReadOnly")
}

func TestSharedFolderMountFollowsTheShare(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead)
	l.mountForWife(t)

	// Revoked, it is gone from her files; its mount stays, broken.
	require.NoError(t, l.env.Providers.Shares.Delete(l.share))
	exists, err := l.fp.Exists("wife", "/Photos/beach.jpg")
	require.NoError(t, err)
	require.False(t, exists)
	_, err = l.env.Providers.Mounts.GetByUserPath(l.wife.ID, "/Photos")
	require.NoError(t, err)
}

func TestSharedFolderMountFollowsAMove(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead)
	_, err := l.fp.Upload("jonathan", "/Docs/tax.pdf", strings.NewReader("1040"))
	require.NoError(t, err)
	docs, err := l.fp.GetByPath("jonathan", "/Docs")
	require.NoError(t, err)
	_, err = l.env.Providers.Shares.Create(l.jonathan.ID, mount.SharedDirType, files.IDString(docs), "USER", l.wife.ID, plugin.PermRead)
	require.NoError(t, err)
	e, err := l.env.Providers.Shared.Lookup(l.wife.User, "jonathan", "Docs")
	require.NoError(t, err)
	_, err = l.env.Providers.Shared.MountFolder(l.wife.User, e, "/Jonathan's docs")
	require.NoError(t, err)

	_, err = l.fp.Rename("jonathan", "/Docs", "/Archive/Papers")
	require.NoError(t, err)
	require.Equal(t, []string{"tax.pdf"}, listNames(t, l.fp, "wife", "/Jonathan's docs"))
}

func TestSharedFolderQuotaIsTheOwners(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead|plugin.PermWrite)
	l.mountForWife(t)
	// Ten bytes each on the volume: jonathan has used four.
	require.NoError(t, l.env.Providers.Quotas.Set(l.vol.ID, "DEFAULT", "", 10))

	_, err := l.fp.Upload("wife", "/Photos/big.jpg", strings.NewReader("twelve bytes"))
	require.True(t, errors.Is(err, plugin.ErrQuotaExceeded), "want plugin.ErrQuotaExceeded")
	_, err = l.fp.Upload("wife", "/Photos/ok.jpg", strings.NewReader("six!!!"))
	require.NoError(t, err)
	// Her own files are on her root volume, which the limit does not touch.
	_, err = l.fp.Upload("wife", "/notes.txt", strings.NewReader("twelve bytes"))
	require.NoError(t, err)
}

func TestSharedWithMePages(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead|plugin.PermWrite)
	cookie, token := sessionAndCSRF(t, l.env, l.wife.ID)

	l.env.Get(t, "/apps/files/shared", cookie).Status(http.StatusOK).Contains("Photos").Contains("hdd")
	l.env.Get(t, "/apps/files/shared/jonathan/Photos", cookie).Status(http.StatusOK).Contains("beach.jpg").Contains("Add to my files")
	l.env.Get(t, "/apps/files/shared/jonathan/Photos/beach.jpg", cookie).Status(http.StatusOK).Equals("sand")
	l.env.Get(t, "/apps/files/shared/jonathan/Photos/../../wife", cookie).Status(http.StatusNotFound)
	l.env.Get(t, "/apps/files/shared/admin/hdd", cookie).Status(http.StatusOK).Contains("Add storage")

	l.env.PostForm(t, "/apps/files/shared/jonathan/Photos", url.Values{
		"csrf_token": {token}, "action": {"mount"}, "path": {"/Family photos"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	require.Equal(t, []string{"beach.jpg"}, listNames(t, l.fp, "wife", "/Family photos"))

	l.env.PostForm(t, "/apps/files/shared/admin/hdd", url.Values{
		"csrf_token": {token}, "action": {"area"}, "path": {"/Backups"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	m, err := l.env.Providers.Mounts.GetByUserPath(l.wife.ID, "/Backups")
	require.NoError(t, err)
	require.Equal(t, "wife/Backups", m.VolumeSubpath)

	// Someone else's photos are not hers to see.
	other := l.env.CreateUser(t, "other", "pass")
	otherCookie, _ := sessionAndCSRF(t, l.env, other.ID)
	l.env.Get(t, "/apps/files/shared/jonathan/Photos", otherCookie).Status(http.StatusNotFound)
}

func TestSharedFolderOverWebDAV(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead|plugin.PermWrite)
	l.mountForWife(t)

	l.env.Put(t, "/remote.php/dav/files/wife/Photos/phone.jpg", strings.NewReader("selfie"), l.wife.Auth).Status(http.StatusCreated)
	_, err := l.fp.GetByPath("jonathan", "/Photos/phone.jpg")
	require.NoError(t, err)

	req := cogtest.NewRequest(t, "PROPFIND", l.env.Providers.Config.BaseURL+"/remote.php/dav/files/wife/Photos", nil)
	resp := l.env.Do(t, req, l.wife.Auth, cogtest.Header("Depth", "1")).Status(http.StatusMultiStatus)
	resp.Contains("<oc:owner-id>jonathan</oc:owner-id>").Contains("<oc:permissions>SMGCK</oc:permissions>").Contains("<oc:permissions>SMGDNVCK</oc:permissions>")
}

func TestShellLinksSharedFolder(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead)
	fsys, err := cogssh.SessionFS(l.env.Providers, l.env.Providers.SSHKeys, "wife")
	require.NoError(t, err)
	sh := shell.New("wife", fsys, nil, l.env.Providers.Users.Usernames, nil, slog.New(slog.DiscardHandler))
	run := func(cmd string) (string, error) {
		var out, errOut bytes.Buffer
		err := sh.Run(context.Background(), cmd, strings.NewReader(""), &out, &errOut)
		return out.String() + errOut.String(), err
	}

	out, err := run("ls /shared/jonathan/Photos")
	require.NoError(t, err)
	require.Contains(t, out, "beach.jpg")
	out, err = run("cat /shared/jonathan/Photos/beach.jpg")
	require.NoError(t, err)
	require.Equal(t, "sand", strings.TrimSpace(out))

	_, err = run("ln -s /shared/jonathan/Photos ~/Photos")
	require.NoError(t, err)
	require.Equal(t, []string{"beach.jpg"}, listNames(t, l.fp, "wife", "/Photos"))

	// Storage on a volume is linked the same way.
	_, err = run("ln -s /shared/admin/hdd /files/Backups")
	require.NoError(t, err)
	m, err := l.env.Providers.Mounts.GetByUserPath(l.wife.ID, "/Backups")
	require.NoError(t, err)
	require.Equal(t, mount.Area, m.Kind)

	// Removing the link unmounts; the photos stay jonathan's.
	_, err = run("rm ~/Photos")
	require.NoError(t, err)
	_, err = l.env.Providers.Mounts.GetByUserPath(l.wife.ID, "/Photos")
	require.True(t, errors.Is(err, mount.ErrNotFound))
	_, err = l.fp.GetByPath("jonathan", "/Photos/beach.jpg")
	require.NoError(t, err)

	out, err = run("ln -s /files/elsewhere /files/x")
	require.Error(t, err)
	require.Contains(t, out, "only a folder or volume")
}

func TestVolumeKindConvertsMounts(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	dir := t.TempDir()
	vol, err := env.Providers.Volumes.Create("hdd", "", "file://"+dir, nil, "{}")
	require.NoError(t, err)
	_, err = env.Providers.Shares.Create(admin.ID, volume.ShareResourceType, vol.ID, "USER", bob.ID, plugin.PermRead|plugin.PermWrite)
	require.NoError(t, err)
	bobs, err := env.Providers.Mounts.Create(bob.ID, "/hdd", vol.ID, "music", false)
	require.NoError(t, err)
	admins, err := env.Providers.Mounts.Create(admin.ID, "/hdd", vol.ID, "", false)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Volumes.SetKind(vol.ID, volume.Storage))
	got, err := env.Providers.Mounts.GetByID(bobs.ID)
	require.NoError(t, err)
	require.Equal(t, mount.Area, got.Kind)
	require.Equal(t, "music", got.VolumeSubpath) // the files stay put
	got, err = env.Providers.Mounts.GetByID(admins.ID)
	require.NoError(t, err)
	require.Equal(t, mount.Volume, got.Kind)

	require.NoError(t, env.Providers.Volumes.SetKind(vol.ID, volume.Raw))
	got, err = env.Providers.Mounts.GetByID(bobs.ID)
	require.NoError(t, err)
	require.Equal(t, mount.Volume, got.Kind)
}

func TestSharedFolderReshare(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead)
	l.mountForWife(t)
	require.True(t, errors.Is(l.fp.MayShare("wife", "/Photos/beach.jpg"), files.ErrNoReshare))
	require.NoError(t, l.fp.MayShare("jonathan", "/Photos/beach.jpg"))

	_, err := l.fp.List("wife", "/Photos")
	require.NoError(t, err)
	beach, err := l.fp.GetByPath("wife", "/Photos/beach.jpg")
	require.NoError(t, err)
	cookie, token := sessionAndCSRF(t, l.env, l.wife.ID)
	l.env.PostForm(t, "/api/shares/create", url.Values{
		"csrf_token": {token}, "resource_type": {"files:file"}, "resource_id": {files.IDString(beach)}, "subject_id": {l.admin.ID},
	}, cookie).Status(http.StatusForbidden)
}

// Two people who mount each other's folders inside their own make a loop;
// building either tree must end.
func TestSharedFolderMountLoopEnds(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead|plugin.PermWrite|plugin.PermReshare)
	l.mountForWife(t)
	require.NoError(t, l.fp.EnsureRootDir("wife"))
	_, err := l.fp.List("wife", "/") // her row of the mount point
	require.NoError(t, err)
	photos, err := l.fp.GetByPath("wife", "/Photos")
	require.NoError(t, err)
	// She shares her mount of his photos back with him, and he mounts it
	// inside the photos themselves.
	_, err = l.env.Providers.Shares.Create(l.wife.ID, mount.SharedDirType, files.IDString(photos), "USER", l.jonathan.ID, plugin.PermRead)
	require.NoError(t, err)
	e, err := l.env.Providers.Shared.Lookup(l.jonathan.User, "wife", "Photos")
	require.NoError(t, err)
	_, err = l.env.Providers.Shared.MountFolder(l.jonathan.User, e, "/Photos/loop")
	require.NoError(t, err)

	require.Equal(t, []string{"loop", "beach.jpg"}, listNames(t, l.fp, "jonathan", "/Photos"))
	_, err = l.fp.List("jonathan", "/Photos/loop")
	require.NoError(t, err)
	_, err = l.fp.List("wife", "/Photos")
	require.NoError(t, err)
}

// A phone backing up a video sends it in chunks, staged before they are
// assembled: they are held to the quota of where the file goes, not the
// root volume's, which may be far smaller.
func TestChunkedUploadToSharedFolderUsesItsQuota(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead|plugin.PermWrite)
	l.mountForWife(t)
	root, err := l.env.Providers.Volumes.GetByName(volume.DefaultVolumeName)
	require.NoError(t, err)
	require.NoError(t, l.env.Providers.Quotas.Set(root.ID, "DEFAULT", "", 5))

	uploads := "/remote.php/dav/uploads/" + l.wife.ID
	dest := l.env.Providers.Config.BaseURL + "/remote.php/dav/files/" + l.wife.ID + "/Photos/video.mp4"
	toPhotos := cogtest.Header("Destination", dest)
	webdavReq(t, l.env, "MKCOL", uploads+"/video/", "", l.wife.Auth, toPhotos).Status(http.StatusCreated)
	webdavReq(t, l.env, "PUT", uploads+"/video/00001", "twelve bytes", l.wife.Auth, toPhotos).Status(http.StatusCreated)
	webdavReq(t, l.env, "MOVE", uploads+"/video/.file", "", l.wife.Auth, toPhotos).Status(http.StatusCreated)
	_, err = os.Stat(filepath.Join(l.dir, "jonathan", "Photos", "video.mp4"))
	require.NoError(t, err)

	// Bound for her own files, the same chunk is over her quota.
	webdavReq(t, l.env, "MKCOL", uploads+"/mine/", "", l.wife.Auth).Status(http.StatusCreated)
	webdavReq(t, l.env, "PUT", uploads+"/mine/00001", "twelve bytes", l.wife.Auth).Status(http.StatusInsufficientStorage)
}

// shellFor runs commands in username's SSH shell.
func shellFor(t *testing.T, l *photoLibrary, username string) func(string) (string, error) {
	t.Helper()
	fsys, err := cogssh.SessionFS(l.env.Providers, l.env.Providers.SSHKeys, username)
	require.NoError(t, err)
	sh := shell.New(username, fsys, nil, l.env.Providers.Users.Usernames, nil, slog.New(slog.DiscardHandler))
	return func(cmd string) (string, error) {
		var out, errOut bytes.Buffer
		err := sh.Run(context.Background(), cmd, strings.NewReader(""), &out, &errOut)
		return out.String() + errOut.String(), err
	}
}

func TestShellVolumesStorage(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead)
	wife := shellFor(t, l, "wife")

	out, err := wife("ls /volumes")
	require.NoError(t, err)
	require.Contains(t, out, "hdd")
	require.False(t, strings.Contains(out, "default"), "the data directory is not hers to see")
	// Her view of a storage volume is her own areas: none yet.
	out, err = wife("ls /volumes/hdd")
	require.NoError(t, err)
	require.False(t, strings.Contains(out, "jonathan"))

	_, err = wife("ln -s /volumes/hdd /files/Backups")
	require.NoError(t, err)
	m, err := l.env.Providers.Mounts.GetByUserPath(l.wife.ID, "/Backups")
	require.NoError(t, err)
	require.Equal(t, "wife/Backups", m.VolumeSubpath)
	_, err = l.fp.Upload("wife", "/Backups/phone.jpg", strings.NewReader("pic"))
	require.NoError(t, err)

	out, err = wife("ls /volumes/hdd/Backups")
	require.NoError(t, err)
	require.Contains(t, out, "phone.jpg")
	// Looking is all /volumes is for.
	_, err = wife("touch /volumes/hdd/Backups/new.txt")
	require.Error(t, err)

	// Unmounted and linked again by name, it is the same storage.
	_, err = wife("rm /files/Backups")
	require.NoError(t, err)
	_, err = wife("ln -s /volumes/hdd/Backups /files/Phone")
	require.NoError(t, err)
	require.Equal(t, []string{"phone.jpg"}, listNames(t, l.fp, "wife", "/Phone"))

	_, err = wife("ln -s /volumes/hdd/Backups/deeper /files/x")
	require.Error(t, err)
}

func TestShellVolumesWhole(t *testing.T) {
	l := newPhotoLibrary(t, plugin.PermRead)
	admin := shellFor(t, l, "admin")

	out, err := admin("ls /volumes/hdd/jonathan")
	require.NoError(t, err)
	require.Contains(t, out, "Photos")
	_, err = admin("ln -s /volumes/hdd/jonathan/Photos /files/JPhotos")
	require.NoError(t, err)
	m, err := l.env.Providers.Mounts.GetByUserPath(l.admin.ID, "/JPhotos")
	require.NoError(t, err)
	require.Equal(t, mount.Volume, m.Kind)
	require.Equal(t, []string{"beach.jpg"}, listNames(t, l.fp, "admin", "/JPhotos"))

	// The server's own files stay out of sight, and out of reach.
	out, err = admin("ls /volumes/default")
	require.NoError(t, err)
	require.False(t, strings.Contains(out, "system.db"))
	_, err = admin("cat /volumes/default/system.db")
	require.Error(t, err)
	_, err = admin("ln -s /volumes/default /files/data")
	require.Error(t, err)
}
