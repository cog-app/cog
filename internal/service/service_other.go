//go:build !linux && !darwin && !windows

package service

import "os/exec"

// DefaultAccount is the account a system service would run as.
const DefaultAccount = "cog"

func Install(Spec) error                 { return ErrUnsupported }
func Uninstall(Spec) error               { return ErrUnsupported }
func Stop(Spec) error                    { return ErrUnsupported }
func Restart(Spec) error                 { return ErrUnsupported }
func Query(Spec) (Status, error)         { return Status{}, nil }
func CreateAccount(string, string) error { return ErrUnsupported }
func GiveDir(string, string) error       { return ErrUnsupported }
func RunAs(*exec.Cmd, string) error      { return ErrUnsupported }
func OpenFirewall([]Port) error          { return nil }
func IsAdmin() bool                      { return false }
