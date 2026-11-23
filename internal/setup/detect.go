package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/service"
)

// Install is an existing install setup found.
type Install struct {
	Scope  service.Scope
	Dir    string
	Config config.Config
	// ConfigErr is why the configuration could not be read: a system
	// install's, from setup run without administrator rights.
	ConfigErr error
	Status    service.Status
}

// Plan is a plan to change in.
func (in Install) Plan(source string) (Plan, error) {
	if in.ConfigErr != nil {
		p, err := DefaultPlan(in.Scope, source)
		p.Mode, p.Dir = ModeReconfig, in.Dir
		return p, err
	}
	return FromConfig(in.Config, source)
}

// Find returns the installs at the default places for each scope and the
// one the running binary belongs to.
func Find(exe string) ([]Install, error) {
	var dirs []string
	if exe != "" {
		dirs = append(dirs, filepath.Dir(filepath.Dir(exe)))
	}
	for _, scope := range []service.Scope{service.User, service.System} {
		d, err := DefaultDir(scope)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, d)
	}
	seen := map[string]bool{}
	var found []Install
	for _, dir := range dirs {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		path := filepath.Join(dir, config.FileName)
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		in := Install{Dir: dir}
		in.Config, in.ConfigErr = config.Load(path)
		in.Scope = service.Scope(in.Config.Install.Service)
		if in.ConfigErr != nil || in.Scope == "" {
			in.Scope = service.User
			if sys, err := DefaultDir(service.System); err == nil && sys == dir {
				in.Scope = service.System
			}
		}
		spec := Plan{Scope: in.Scope, Dir: dir}.Service()
		st, err := ops.Query(spec)
		if err != nil {
			return nil, err
		}
		in.Status = st
		found = append(found, in)
	}
	return found, nil
}
