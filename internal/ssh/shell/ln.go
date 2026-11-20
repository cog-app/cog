package shell

import (
	"path"

	"github.com/spf13/cobra"
)

// Linker is a session filesystem that can put something shared with the
// user into their files: the only links a session can make are mounts.
type Linker interface {
	// Symlink mounts target, a folder or storage volume under /shared, at
	// link, a path under /files.
	Symlink(target, link string) error
	// IsLink reports whether p is a mount made by Symlink, or another
	// mount of the user's.
	IsLink(p string) (bool, error)
	// Unlink removes the mount at link, leaving what it held where it is.
	Unlink(link string) error
}

func (s *Shell) linker() (Linker, bool) {
	l, ok := s.fs.(Linker)
	return l, ok
}

// isLink reports whether vp is a mount, which rm and unlink unmount.
func (s *Shell) isLink(vp string) (bool, error) {
	l, ok := s.linker()
	if !ok {
		return false, nil
	}
	return l.IsLink(vp)
}

func (s *Shell) newLnCmd() *cobra.Command {
	var symbolic bool
	cmd := &cobra.Command{
		Use:   "ln -s <target> <link>",
		Short: "Put a folder or storage shared with you into your files",
		Long: `ln -s mounts something from /shared in your files: a shared folder, which
then is in your files for every app, or a storage volume, which gets a folder
of yours kept on it. Remove the link with rm or unlink; what it holds stays.`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stderr, _, dir := getHC(cmd)
			if !symbolic {
				s.errorf(stderr, "ln: only symbolic links (-s) can be made\n")
				return exitErr(1)
			}
			l, ok := s.linker()
			if !ok {
				s.errorf(stderr, "ln: links are not available here\n")
				return exitErr(1)
			}
			target := s.toVirtualPath(dir, args[0])
			link := s.toVirtualPath(dir, args[1])
			// ln -s target dir puts the link in dir under the target's name.
			if fi, err := s.fs.Stat(link); err == nil && fi.IsDir {
				if mounted, err := s.isLink(link); err == nil && !mounted {
					link = path.Join(link, path.Base(target))
				}
			}
			if err := l.Symlink(target, link); err != nil {
				s.errorf(stderr, "ln: %v\n", err)
				return exitErr(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&symbolic, "symbolic", "s", false, "Make a symbolic link (a mount)")
	return cmd
}

func (s *Shell) newUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "unlink <link>",
		Short:             "Remove a link made with ln -s, leaving what it held",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: s.completePaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stderr, _, dir := getHC(cmd)
			vp := s.toVirtualPath(dir, args[0])
			mounted, err := s.isLink(vp)
			if err != nil {
				s.errorf(stderr, "unlink: %v\n", err)
				return exitErr(1)
			}
			if !mounted {
				s.errorf(stderr, "unlink: %s is not a link\n", args[0])
				return exitErr(1)
			}
			if err := s.unlink(vp); err != nil {
				s.errorf(stderr, "unlink: %v\n", err)
				return exitErr(1)
			}
			return nil
		},
	}
}

func (s *Shell) unlink(vp string) error {
	l, _ := s.linker()
	return l.Unlink(vp)
}
