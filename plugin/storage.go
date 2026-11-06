package plugin

import (
	"io"
	"io/fs"
)

// Storage is a directory the host keeps for a plugin: files the plugin
// stores outside its database, such as attachments, photos and images.
//
// Names are slash-separated paths relative to the directory and must satisfy
// fs.ValidPath, so "..", absolute paths and empty elements are refused; a
// symlink inside cannot lead out of it either. The host decides where the
// directory lives, so a plugin never builds a path into the data directory.
//
// A user's storage (PluginContext.UserStorage) counts toward their quota:
// Put refuses with ErrQuotaExceeded a write that would take them over it,
// leaving what was there before in place. Cache and global storage are not
// counted.
type Storage interface {
	// Open opens a file for reading.
	Open(name string) (StoredFile, error)
	// Put replaces name with content in one step, creating its parent
	// directories, and returns the bytes written. Readers see the old file
	// or the new one, never part of either.
	Put(name string, content io.Reader) (int64, error)
	// Take moves the file fromName in from to name here, replacing what is
	// there, and returns its size: how a file a program wrote through a
	// cache's LocalPath becomes one of the user's, counted like a Put.
	// Within one user's directories it is a rename, so nothing is copied.
	Take(name string, from Storage, fromName string) (int64, error)
	// Remove removes a file, or an empty directory. A name that does not
	// exist is not an error.
	Remove(name string) error
	// RemoveAll removes name and everything under it. A name that does not
	// exist is not an error.
	RemoveAll(name string) error
	// Stat describes a file or directory.
	Stat(name string) (fs.FileInfo, error)
	// ReadDir lists a directory; "." is the storage itself. A directory that
	// does not exist reads as empty.
	ReadDir(name string) ([]fs.DirEntry, error)
	// LocalPath returns the local path of name, creating its parent
	// directories, for a program that needs one: a converter's input or
	// output. Bytes written through it bypass Put, so they are not counted;
	// write a user's files with Put, and leave LocalPath outputs to caches.
	LocalPath(name string) (string, error)
}

// StoredFile is an open file from a Storage.
type StoredFile interface {
	io.ReadSeekCloser
	Stat() (fs.FileInfo, error)
}

// thumbnailSizes are the sizes ThumbnailSize rounds to.
var thumbnailSizes = []int{32, 64, 128, 256, 512, 1024}

// ThumbnailSize rounds a requested thumbnail size up to one of a few, so a
// cache holds a handful of sizes of an image rather than one per size
// asked for. Clients scale what they get to fit.
func ThumbnailSize(n int) int {
	for _, s := range thumbnailSizes {
		if n <= s {
			return s
		}
	}
	return thumbnailSizes[len(thumbnailSizes)-1]
}
