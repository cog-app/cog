package vfs_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"git.duckfam.us/jj-software/cog/vfs"
)

// Writers racing to replace one file leave exactly one writer's bytes, and
// no temporary files behind.
func TestWriteFileAtomicConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "race.txt")

	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			body := strings.Repeat(fmt.Sprintf("writer-%d;", i), 1000)
			if err := vfs.WriteFileAtomic(path, strings.NewReader(body), 0o644); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(got), ";")
	if want := strings.Repeat(first+";", 1000); string(got) != want {
		t.Fatalf("file mixes writers: starts %q, %d bytes", first, len(got))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("left %d entries, want 1", len(entries))
	}
}

// A failed write leaves the old file as it was.
func TestWriteFileAtomicFailureKeepsOld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := vfs.WriteFileAtomic(path, failingReader{}, 0o644); err == nil {
		t.Fatal("want error")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "old" {
		t.Fatalf("got %q, %v; want old", got, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("left %d entries, want 1", len(entries))
	}
}

func TestIsPartial(t *testing.T) {
	if vfs.IsPartial("report.txt") || vfs.IsPartial(".hidden") {
		t.Fatal("an ordinary file reads as partial")
	}
	if !vfs.IsPartial(".report.txt.123.cog-partial") {
		t.Fatal("a partial file reads as complete")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }
