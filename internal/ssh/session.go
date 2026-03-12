package ssh

import (
	"context"
	"encoding/binary"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/ssh/shell"
	"git.duckfam.us/jj-software/cog/vfs"

	gossh "golang.org/x/crypto/ssh"
)

// handleSession dispatches SSH session requests (exec, subsystem, shell).
func (s *Server) handleSession(channel gossh.Channel, requests <-chan *gossh.Request, username string) {
	defer channel.Close()

	filesDir := s.providers.Plugins.UserFilesDir(username)
	userFS, _ := s.providers.Plugins.UserFS(username)

	// Overlay .ssh/authorized_keys on the VFS so ssh-copy-id and
	// cat .ssh/authorized_keys work transparently.
	if userFS != nil {
		ofs := vfs.NewOverlayFS(userFS)
		ofs.Bind("/.ssh/authorized_keys", newAuthKeysFileHandler(s.keys, username))
		userFS = ofs
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var termWidth, termHeight int
	var hasPTY bool

	for req := range requests {
		s.log.Debug("handling request", "type", req.Type)
		switch req.Type {
		case "pty-req":
			termWidth, termHeight = parsePtyReq(req.Payload)
			hasPTY = true
			if req.WantReply {
				req.Reply(true, nil)
			}

		case "window-change":
			if len(req.Payload) >= 8 {
				termWidth = int(binary.BigEndian.Uint32(req.Payload))
				termHeight = int(binary.BigEndian.Uint32(req.Payload[4:]))
			}
			if req.WantReply {
				req.Reply(true, nil)
			}

		case "env":
			// Ignore environment variables.
			if req.WantReply {
				req.Reply(false, nil)
			}

		case "subsystem":
			name := parseStringPayload(req.Payload)
			if req.WantReply {
				req.Reply(name == "sftp", nil)
			}
			if name == "sftp" {
				s.serveSFTP(channel, filesDir, userFS)
			}
			return

		case "exec":
			command := parseStringPayload(req.Payload)
			if req.WantReply {
				req.Reply(true, nil)
			}
			var exitCode int
			switch {
			case strings.HasPrefix(command, "scp "):
				exitCode = s.serveSCP(channel, filesDir, userFS, command)
			case strings.HasPrefix(command, "rsync "):
				exitCode = s.serveRsync(channel, filesDir, command)
			default:
				exitCode = s.execShell(ctx, channel, username, filesDir, userFS, command, hasPTY, termWidth, termHeight)
			}
			sendExitStatus(channel, exitCode)
			return

		case "shell":
			if req.WantReply {
				req.Reply(true, nil)
			}
			exitCode := s.interactiveShell(ctx, channel, username, filesDir, userFS, hasPTY, termWidth, termHeight)
			sendExitStatus(channel, exitCode)
			return

		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func (s *Server) execShell(ctx context.Context, channel gossh.Channel, username, filesDir string, userFS vfs.FS, command string, hasPTY bool, termWidth, termHeight int) int {
	sh := shell.New(username, filesDir, userFS, s.providers.Plugins.UserCommands(), s.providers.Users.Usernames, s.log)
	if hasPTY {
		sh.SetSSHMode(termWidth, termHeight)
	}
	if err := sh.Run(ctx, command, channel, channel, channel.Stderr()); err != nil {
		s.log.Debug("exec error", "user", username, "cmd", command, "err", err)
		return 1
	}
	return 0
}

func (s *Server) interactiveShell(ctx context.Context, channel gossh.Channel, username, filesDir string, userFS vfs.FS, hasPTY bool, termWidth, termHeight int) int {
	sh := shell.New(username, filesDir, userFS, s.providers.Plugins.UserCommands(), s.providers.Users.Usernames, s.log)
	if hasPTY {
		sh.SetSSHMode(termWidth, termHeight)
	}
	if err := sh.RunInteractive(ctx, channel, channel, channel.Stderr()); err != nil {
		s.log.Debug("shell error", "user", username, "err", err)
		return 1
	}
	return 0
}

// parsePtyReq extracts terminal dimensions from a pty-req payload.
func parsePtyReq(payload []byte) (width, height int) {
	// pty-req: string term, uint32 width-char, uint32 height-row, ...
	if len(payload) < 4 {
		return 80, 24
	}
	termLen := int(binary.BigEndian.Uint32(payload[:4]))
	rest := payload[4+termLen:]
	if len(rest) < 8 {
		return 80, 24
	}
	width = int(binary.BigEndian.Uint32(rest[:4]))
	height = int(binary.BigEndian.Uint32(rest[4:8]))
	return width, height
}

// parseStringPayload reads a uint32-length-prefixed string from SSH payload.
func parseStringPayload(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	n := binary.BigEndian.Uint32(payload[:4])
	if int(n) > len(payload)-4 {
		return ""
	}
	return string(payload[4 : 4+n])
}

// sendExitStatus sends the exit-status message to the channel.
func sendExitStatus(channel gossh.Channel, code int) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(code))
	channel.SendRequest("exit-status", false, payload)
}
