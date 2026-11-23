// Package version is the version of this build.
package version

import (
	"runtime"
	"runtime/debug"
)

// Version is the release this binary was built as ("v1.2.3"), set by the
// release workflow with -ldflags "-X .../internal/version.Version=v1.2.3".
// A build without it reports the module version, or "dev".
var Version = ""

// Get returns this build's version.
func Get() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

// Platform is the release asset this binary runs as: "linux-amd64",
// "windows-amd64", "darwin-arm64"...
func Platform() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}
