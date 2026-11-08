package ssh

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
	"git.duckfam.us/jj-software/cog/internal/ssh/shell"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/vfs"

	gossh "golang.org/x/crypto/ssh"
	"mvdan.cc/sh/v3/interp"
)

// handleSession dispatches SSH session requests (exec, subsystem, shell).
func (s *Server) handleSession(channel gossh.Channel, requests <-chan *gossh.Request, username string) {
	defer channel.Close()

	userFS, err := SessionFS(s.providers, s.keys, username)
	if err != nil {
		s.log.Error("ssh session: user files unavailable", "user", username, "err", err)
		fmt.Fprintf(channel.Stderr(), "cog: your files are unavailable\n")
		sendExitStatus(channel, 1)
		return
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
				s.serveSFTP(channel, userFS)
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
				exitCode = s.serveSCP(channel, userFS, command)
			case strings.HasPrefix(command, "rsync "):
				exitCode = s.serveRsync(ctx, channel, userFS, command)
			default:
				exitCode = s.execShell(ctx, channel, username, userFS, command, hasPTY, termWidth, termHeight)
			}
			sendExitStatus(channel, exitCode)
			return

		case "shell":
			if req.WantReply {
				req.Reply(true, nil)
			}
			exitCode := s.interactiveShell(ctx, channel, username, userFS, hasPTY, termWidth, termHeight)
			sendExitStatus(channel, exitCode)
			return

		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func (s *Server) execShell(ctx context.Context, channel gossh.Channel, username string, userFS vfs.FS, command string, hasPTY bool, termWidth, termHeight int) int {
	sh := shell.New(username, userFS, s.providers.Plugins.UserCommands(), s.providers.Users.Usernames, s.providers.Search, s.log)
	if hasPTY {
		sh.SetSSHMode(termWidth, termHeight)
	}
	return s.exitCode(channel, username, sh.Run(ctx, command, channel, channel, channel.Stderr()))
}

// exitCode turns a shell's result into the session's exit status. A command's
// own exit status passes through, and it has already said why it failed; any
// other error (a parse error, a runner failure) is the user's to see too.
func (s *Server) exitCode(channel gossh.Channel, username string, err error) int {
	if err == nil {
		return 0
	}
	if status, ok := errors.AsType[interp.ExitStatus](err); ok {
		return int(status)
	}
	s.log.Debug("shell error", "user", username, "err", err)
	fmt.Fprintf(channel.Stderr(), "cog: %v\n", err)
	return 1
}

func (s *Server) interactiveShell(ctx context.Context, channel gossh.Channel, username string, userFS vfs.FS, hasPTY bool, termWidth, termHeight int) int {
	sh := shell.New(username, userFS, s.providers.Plugins.UserCommands(), s.providers.Users.Usernames, s.providers.Search, s.log)
	if hasPTY {
		sh.SetSSHMode(termWidth, termHeight)
	}
	return s.exitCode(channel, username, sh.RunInteractive(ctx, channel, channel, channel.Stderr()))
}

// parsePtyReq extracts terminal dimensions from a pty-req payload.
func parsePtyReq(payload []byte) (width, height int) {
	// pty-req: string term, uint32 width-char, uint32 height-row, ...
	if len(payload) < 4 {
		return 80, 24
	}
	termLen := int(binary.BigEndian.Uint32(payload[:4]))
	if termLen > len(payload)-4 {
		return 80, 24
	}
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

// sendExitStatus sends the exit-status message to the channel. A status is
// a byte, as a POSIX shell's is: code is taken modulo 256, so a negative or
// oversized code does not reach the client as some huge uint32.
func sendExitStatus(channel gossh.Channel, code int) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(code&0xff))
	if _, err := channel.SendRequest("exit-status", false, payload); err != nil {
		// The client has gone; there is no one left to tell.
		slog.Debug("send exit-status", "err", err)
	}
}

// SessionFS returns the filesystem an SSH session, or a shell run for
// username, works in: their files at /files as the files plugin keeps them,
// so what is written is held to the quota and recorded, beside
// .ssh/authorized_keys and a few devices at the virtual root, so ssh-copy-id
// and cat .ssh/authorized_keys work while the files keep a clean namespace.
func SessionFS(p *services.Providers, ks *keys.Store, username string) (vfs.FS, error) {
	plug, ok := p.Plugins.Plugin(files.PluginID)
	if !ok {
		return nil, errors.New("the files plugin is not loaded")
	}
	fp, ok := plug.(*files.Plugin)
	if !ok {
		return nil, fmt.Errorf("the files plugin is a %T", plug)
	}
	if err := fp.EnsureRootDir(username); err != nil {
		return nil, err
	}
	userFiles, err := fp.FS(username)
	if err != nil {
		return nil, err
	}

	ofs := vfs.NewOverlayFS(nil) // nil base — pure virtual root
	ofs.Mount("/files", userFiles)
	ofs.Bind("/.ssh/authorized_keys", newAuthKeysFileHandler(ks, username))
	ofs.Bind("/dev/null", newDevHandler(devNull, "null"))
	ofs.Bind("/dev/zero", newDevHandler(devZero, "zero"))
	ofs.Bind("/dev/urandom", newDevHandler(devURandom, "urandom"))
	return ofs, nil
}
