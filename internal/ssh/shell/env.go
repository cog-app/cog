package shell

import "mvdan.cc/sh/v3/expand"

// environ returns the initial environment for a shell session. $USER is
// read from the shell each time, so it follows su; a script's own
// assignment to it still wins.
func (s *Shell) environ() expand.Environ {
	return shellEnviron{s: s, base: expand.ListEnviron(
		"HOME=/files",
		"SHELL=cog",
		"PWD=/files",
		"PATH=",
	)}
}

type shellEnviron struct {
	s    *Shell
	base expand.Environ
}

func (e shellEnviron) user() expand.Variable {
	return expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: e.s.username}
}

func (e shellEnviron) Get(name string) expand.Variable {
	if name == "USER" {
		return e.user()
	}
	return e.base.Get(name)
}

func (e shellEnviron) Each(f func(name string, vr expand.Variable) bool) {
	if !f("USER", e.user()) {
		return
	}
	e.base.Each(f)
}
