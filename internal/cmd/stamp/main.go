// Command stamp sets the modification time of every file git tracks from a
// hash of its content, and of every directory holding one from a hash of its
// listing, so a CI job's go test can reuse results cached by an earlier job.
//
// go test caches a passing package until something it depends on changes.
// Source files are compared by content, but a file a test opens at run time,
// such as a snapshot golden, is compared by its size and modification time,
// and so is a directory a test lists. A fresh checkout gives every file the
// time it was checked out, so without this every package that reads testdata
// runs again in every job. A time derived from the content is the same in
// every checkout of the same bytes, and differs when they change.
//
// Usage: go tool stamp (from the repository root)
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// The times run from 1973 for 2^30 seconds, to 2007. They must be in the
// past: go test will not cache a result that read a file modified in the last
// two seconds, or in the future.
const (
	base = 100_000_000
	span = 1 << 30
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stamp:", err)
		os.Exit(1)
	}
}

func run() error {
	out, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %w", err)
	}
	dirs := map[string]bool{".": true}
	files := 0
	for name := range bytes.SplitSeq(bytes.TrimSuffix(out, []byte{0}), []byte{0}) {
		path := string(name)
		for d := filepath.Dir(path); !dirs[d]; d = filepath.Dir(d) {
			dirs[d] = true
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue // deleted in the working tree
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue // a symlink's time is its target's
		}
		sum, err := hashFile(path)
		if err != nil {
			return err
		}
		if err := setTime(path, sum); err != nil {
			return err
		}
		files++
	}
	// After the files: stamping a file does not touch its directory, but
	// creating one would, and a directory's time is part of what a test
	// that lists it depends on.
	for dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		h := sha256.New()
		for _, e := range entries { // sorted by name
			fmt.Fprintf(h, "%s\x00", e.Name())
		}
		if err := setTime(dir, h.Sum(nil)); err != nil {
			return err
		}
	}
	fmt.Printf("stamp: %d files, %d directories\n", files, len(dirs))
	return nil
}

func hashFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return h.Sum(nil), nil
}

func setTime(path string, sum []byte) error {
	t := time.Unix(base+int64(binary.BigEndian.Uint64(sum)%span), 0)
	if err := os.Chtimes(path, t, t); err != nil {
		return fmt.Errorf("stamp %s: %w", path, err)
	}
	return nil
}
