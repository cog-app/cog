//go:build !linux && !darwin && !windows

package setup

func elevate(string, []string, bool) error { return errElevation }
