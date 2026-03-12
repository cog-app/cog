package shell

import "mvdan.cc/sh/v3/expand"

// environ returns the initial environment for a shell session.
func (s *Shell) environ() expand.Environ {
	return expand.ListEnviron(
		"HOME=/files",
		"USER="+s.username,
		"SHELL=cog",
		"PWD=/files",
		"PATH=",
	)
}
