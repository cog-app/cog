package cog

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestNamesBuiltin: only a command line naming no built-in command starts
// the plugins to find theirs.
func TestNamesBuiltin(t *testing.T) {
	root := &cobra.Command{Use: "cog", RunE: func(*cobra.Command, []string) error { return nil }}
	user := &cobra.Command{Use: "user"}
	user.AddCommand(&cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error { return nil }})
	root.AddCommand(user, &cobra.Command{Use: "shell", RunE: func(*cobra.Command, []string) error { return nil }})
	root.InitDefaultHelpCmd()

	for args, want := range map[string]bool{
		"user list":      true,
		"shell":          true,
		"":               false, // bare cog serves
		"help":           false, // lists plugin commands too
		"recipes import": false, // a plugin's command
	} {
		require.Equal(t, want, namesBuiltin(root, strings.Fields(args)), "%q", args)
	}
}
