package ssh

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"github.com/gokrazy/rsync/rsyncd"
	"github.com/google/shlex"
	gossh "golang.org/x/crypto/ssh"

	"git.duckfam.us/jj-software/cog/vfs"
)

// serveRsync answers the command an rsync client runs over SSH ("rsync
// --server ..."), sending from or receiving into the user's files. As for
// scp, the user's home is /files: a path the client names is taken to be
// within it.
//
// The rsync server runs in this process, so it must not restrict the
// process: by default gokrazy/rsync confines the whole process with
// Landlock to the directories it serves, which would cut the server off from
// everything else it needs.
func (s *Server) serveRsync(ctx context.Context, channel gossh.Channel, userFS vfs.FS, command string) int {
	fail := func(err error) int {
		fmt.Fprintf(channel.Stderr(), "rsync: %v\n", err)
		return 1
	}
	args, err := rsyncServerArgs(command)
	if err != nil {
		return fail(err)
	}
	root, err := newRsyncRoot(userFS, "/files")
	if err != nil {
		s.log.Error("rsync: user files", "err", err)
		return fail(err)
	}
	module := rsyncd.Module{Name: "files", Root: root, Writable: true}
	srv, err := rsyncd.NewServer([]rsyncd.Module{module},
		rsyncd.DontRestrict(),
		rsyncd.WithLogger(rsyncLogger{s.log}),
		rsyncd.WithStderr(nopWriteCloser{channel.Stderr()}),
	)
	if err != nil {
		s.log.Error("rsync: server", "err", err)
		return fail(err)
	}
	// The session closes the channel after sending the exit status, so the
	// connection must not close it first.
	conn := rsyncd.NewConnection(io.NopCloser(channel), nopWriteCloser{channel}, "ssh")
	if err := srv.HandleConnArgs(ctx, conn, &module, args); err != nil {
		s.log.Debug("rsync error", "err", err)
		return fail(err)
	}
	return 0
}

// rsyncServerArgs splits the command an rsync client runs ("rsync --server
// [--sender] <options> . <paths>") into the arguments after "rsync", with
// each path made relative to /files. The client escapes spaces and other
// shell characters in what it sends, as for a shell.
func rsyncServerArgs(command string) ([]string, error) {
	fields, err := shlex.Split(command)
	if err != nil {
		return nil, fmt.Errorf("parse command: %w", err)
	}
	if len(fields) < 2 || fields[0] != "rsync" {
		return nil, fmt.Errorf("not an rsync command: %q", command)
	}
	args := fields[1:]
	if !strings.HasPrefix(args[0], "--server") {
		return nil, fmt.Errorf("only an rsync client may run rsync here")
	}
	// Options never equal "."; it separates them from the paths.
	dot := -1
	for i, a := range args {
		if a == "." {
			dot = i
			break
		}
	}
	if dot < 0 {
		return nil, fmt.Errorf("no path in %q", command)
	}
	for i := dot + 1; i < len(args); i++ {
		args[i] = rsyncPath(args[i])
	}
	return args, nil
}

// rsyncPath maps a path the client names to one relative to /files, keeping
// a trailing slash, which tells rsync to copy a directory's contents rather
// than the directory.
func rsyncPath(p string) string {
	rel := strings.TrimPrefix(path.Clean(strings.TrimPrefix(scpPath(p), "/files")), "/")
	if rel == "" || rel == "." {
		return "./"
	}
	if strings.HasSuffix(p, "/") {
		rel += "/"
	}
	return rel
}

// rsyncLogger sends gokrazy/rsync's progress lines to the server's log at
// debug level.
type rsyncLogger struct{ log *slog.Logger }

func (l rsyncLogger) Printf(msg string, a ...any) {
	l.log.Debug("rsync: " + fmt.Sprintf(msg, a...))
}

func (l rsyncLogger) Output(_ int, s string) error {
	l.log.Debug("rsync: " + strings.TrimSuffix(s, "\n"))
	return nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
