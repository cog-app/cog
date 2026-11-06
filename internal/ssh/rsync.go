package ssh

import (
	"fmt"

	gossh "golang.org/x/crypto/ssh"
)

// serveRsync handles an rsync --server exec request. Returns exit code.
//
// The rsync wire protocol (delta transfer algorithm, file list encoding, etc.)
// is complex and no suitable pure-Go library exists for the --server mode.
// This is a stub that returns a clear error to the client.
func (s *Server) serveRsync(channel gossh.Channel, command string) int {
	fmt.Fprintf(channel.Stderr(), "rsync: server-side rsync is not yet implemented in Cog\n")
	fmt.Fprintf(channel.Stderr(), "rsync: use SFTP or SCP for file transfers instead\n")
	s.log.Info("rsync requested but not implemented", "command", command)
	return 1
}
