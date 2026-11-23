//go:build nosetup

package cog

import (
	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/plugin"
)

// A nosetup build (an image, a distribution's package) is installed and
// updated by whoever packaged it: it has no setup or update commands, and
// bare `cog` always serves.

func launchedForSetup() bool                           { return false }
func runSetupGUI() error                               { return nil }
func waitBeforeClosing()                               {}
func addSetupCommands(*cobra.Command, []plugin.Plugin) {}
