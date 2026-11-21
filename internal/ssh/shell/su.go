package shell

import (
	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/vfs"
)

// SwitchUser resolves username for su: the account's canonical name and the
// filesystem a session of theirs works in. It decides whether the switch is
// allowed, each time it is asked, so a session that outlives its user's
// admin rights cannot use them.
type SwitchUser func(username string) (string, vfs.FS, error)

// SetSwitchUser offers su in the shell. Only an admin's session is given
// one: a shell without it has no su command, in help or completion either.
func (s *Shell) SetSwitchUser(f SwitchUser) { s.switchUser = f }

// identity is who a shell acts as, and where it was, before an su.
type identity struct {
	username string
	fs       vfs.FS
	dir      string
}

func (s *Shell) newSuCmd() *cobra.Command {
	var login bool
	cmd := &cobra.Command{
		Use:   "su [-] <user>",
		Short: "Act as another user until exit",
		Long: `su acts as another user: their files, shares and volumes, starting in
their files. exit, or Ctrl-D, returns to who you were and where you were.`,
		Args: cobra.RangeArgs(1, 2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) > 0 && args[0] != "-" {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return s.completeUsernames(toComplete)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stderr, _, dir := getHC(cmd)
			// "su - bob", as people type it out of habit: every su here is
			// a login, into the user's files.
			if args[0] == "-" {
				args = args[1:]
			}
			if len(args) != 1 {
				s.errorf(stderr, "usage: su [-] <user>\n")
				return exitErr(1)
			}
			name, fsys, err := s.switchUser(args[0])
			if err != nil {
				s.errorf(stderr, "su: %v\n", err)
				return exitErr(1)
			}
			s.suStack = append(s.suStack, identity{username: s.username, fs: s.fs, dir: dir})
			s.become(name, fsys, home)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&login, "login", "l", false, "Start in the user's files (every su does)")
	return cmd
}

// suExit leaves the innermost su, returning to the identity and directory
// it was entered from.
func (s *Shell) suExit() {
	prev := s.suStack[len(s.suStack)-1]
	s.suStack = s.suStack[:len(s.suStack)-1]
	s.become(prev.username, prev.fs, prev.dir)
}

// become makes the shell act as username in fsys, in dir. $USER follows,
// since the environment reads it from the shell.
func (s *Shell) become(username string, fsys vfs.FS, dir string) {
	s.username = username
	s.fs = fsys
	s.runner.Dir = dir
}
