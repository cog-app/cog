package service

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestUnit(t *testing.T) {
	s := Spec{Name: "cog", Description: "Cog", Scope: System, Exe: "/opt/my cog/bin/cog",
		Args: []string{"serve", "--config", "/opt/my cog/cog.toml"}, Dir: "/opt/my cog", Account: "cog"}
	unit := Unit(s)
	require.Contains(t, unit, `ExecStart="/opt/my cog/bin/cog" serve --config "/opt/my cog/cog.toml"`+"\n")
	require.Contains(t, unit, "User=cog\n")
	require.Contains(t, unit, "Restart=always\n", "an update's exit starts the new binary")
	require.Contains(t, unit, "AmbientCapabilities=CAP_NET_BIND_SERVICE\n")
	require.Contains(t, unit, "WantedBy=multi-user.target\n")

	s.Scope = User
	unit = Unit(s)
	require.False(t, strings.Contains(unit, "User="), "a user service runs as its user")
	require.Contains(t, unit, "WantedBy=default.target\n")
}

func TestSystemdCommandEscapes(t *testing.T) {
	require.Equal(t, `/bin/cog "100%%" "$$HOME" "a\"b"`, systemdCommand("/bin/cog", []string{"100%", "$HOME", `a"b`}))
}
