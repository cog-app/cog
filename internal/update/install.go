package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CanReplace reports why the binary at exe cannot be replaced in place,
// or nil when it can: an update writes the new binary beside it, so the
// directory has to be writable by the account the server runs as.
func CanReplace(exe string) error {
	f, err := os.CreateTemp(filepath.Dir(exe), ".cog-update-check-*")
	if err != nil {
		return fmt.Errorf("cannot write beside %s: %w", exe, err)
	}
	name := f.Name()
	return errors.Join(f.Close(), os.Remove(name))
}

// Install downloads rel's binary for platform, checks it against the
// signed hash and that it runs and reports rel's version, then puts it in
// place of exe. The running process keeps running the old binary until
// it restarts.
func Install(ctx context.Context, client *http.Client, rel *Release, platform, exe string) error {
	asset, ok := rel.Assets[platform]
	if !ok {
		return fmt.Errorf("%s has no binary for %s", rel.Version, platform)
	}
	want, err := hex.DecodeString(asset.SHA256)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("%s: bad checksum %q", rel.Version, asset.SHA256)
	}
	f, err := os.CreateTemp(filepath.Dir(exe), ".cog-update-*"+filepath.Ext(exe))
	if err != nil {
		return fmt.Errorf("cannot write beside %s: %w", exe, err)
	}
	tmp := f.Name()
	done := false
	defer func() {
		if !done {
			// The download failed or did not check out: it is not kept.
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return fmt.Errorf("download %s: %w", rel.Version, err)
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", rel.Version, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", rel.Version, res.Status)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, asset.Size+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", rel.Version, err)
	}
	if n != asset.Size {
		return fmt.Errorf("download %s: got %d bytes, the release list says %d", rel.Version, n, asset.Size)
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return fmt.Errorf("download %s: the binary does not match the release list's checksum", rel.Version)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return fmt.Errorf("make %s executable: %w", tmp, err)
	}
	if err := keepOwner(tmp, exe); err != nil {
		return err
	}
	if err := checkRuns(ctx, tmp, rel.Version); err != nil {
		return err
	}
	if err := replace(tmp, exe); err != nil {
		return err
	}
	done = true
	return nil
}

// checkRuns runs the downloaded binary's version command, so a binary
// that cannot start here (a library it needs is missing) is found before
// it replaces one that can.
func checkRuns(ctx context.Context, bin, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return fmt.Errorf("the downloaded %s does not run here: %w", version, err)
	}
	if got := strings.TrimSpace(string(out)); got != version {
		return fmt.Errorf("the downloaded binary says it is %q, not %s", got, version)
	}
	return nil
}

// replace puts the file at src in place of dst. Windows will not replace a
// running program's file but will rename it, so there the old binary is
// moved aside to dst.old first, and removed by Cleanup when the new one
// starts.
func replace(src, dst string) error {
	if !renameOverRunning {
		old := dst + ".old"
		if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove the binary an earlier update left: %w", err)
		}
		if err := os.Rename(dst, old); err != nil {
			return fmt.Errorf("move the running binary aside: %w", err)
		}
		if err := os.Rename(src, dst); err != nil {
			return errors.Join(fmt.Errorf("put the new binary in place: %w", err), os.Rename(old, dst))
		}
		return nil
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("put the new binary in place: %w", err)
	}
	return nil
}

// Cleanup removes the binary an update moved aside, which Windows only
// lets go of once it no longer runs.
func Cleanup(exe string) error {
	if err := os.Remove(exe + ".old"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove the binary before the last update: %w", err)
	}
	return nil
}
