// Package ssh implements an embedded SSH server for Cog, providing SFTP,
// SCP, rsync, and interactive shell access.
package ssh

import (
	"errors"
	"fmt"
	"log/slog"
	"net"

	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/ssh/keys"

	gossh "golang.org/x/crypto/ssh"
)

// Server is an embedded SSH server that provides file transfer and shell access.
type Server struct {
	cfg       config.Config
	providers *services.Providers
	keys      *keys.Store
	log       *slog.Logger
	listener  net.Listener
	sshConfig *gossh.ServerConfig
}

// New creates a new SSH server. Call ListenAndServe to start accepting connections.
func New(cfg config.Config, p *services.Providers, ks *keys.Store, log *slog.Logger) (*Server, error) {
	hostKey, err := LoadOrGenerateHostKey(cfg.SSHHostKeyPath, cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("load host key: %w", err)
	}

	auth := &authCallbacks{
		users:        p.Users,
		appPasswords: p.AppPasswords,
		keys:         ks,
		guard:        p.Guard,
	}

	sshCfg := &gossh.ServerConfig{
		PasswordCallback:  auth.passwordCallback,
		PublicKeyCallback: auth.publicKeyCallback,
	}
	sshCfg.AddHostKey(hostKey)

	return &Server{
		cfg:       cfg,
		providers: p,
		keys:      ks,
		log:       log.With("component", "ssh"),
		sshConfig: sshCfg,
	}, nil
}

// ListenAndServe starts accepting SSH connections. Blocks until Close is called.
// If a listener was already set (e.g. for tests), it is reused.
func (s *Server) ListenAndServe() error {
	if s.listener == nil {
		ln, err := net.Listen("tcp", s.cfg.SSHAddr)
		if err != nil {
			return fmt.Errorf("ssh listen: %w", err)
		}
		s.listener = ln
	}
	s.log.Info("SSH server listening", "addr", s.listener.Addr())

	for {
		conn, err := s.listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return nil // Close was called
		}
		if err != nil {
			return fmt.Errorf("ssh accept: %w", err)
		}
		go s.handleConn(conn)
	}
}

// Close stops the SSH server.
func (s *Server) Close() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	sshConn, chans, reqs, err := gossh.NewServerConn(conn, s.sshConfig)
	if err != nil {
		s.log.Warn("ssh handshake failed", "addr", conn.RemoteAddr(), "err", err)
		return
	}
	defer sshConn.Close()

	username := sshConn.Permissions.Extensions["username"]
	s.log.Info("ssh connection", "user", username, "addr", conn.RemoteAddr())

	// Discard global requests (keepalive, etc.)
	go gossh.DiscardRequests(reqs)

	for newChan := range chans {
		chType := newChan.ChannelType()
		if chType != "session" {
			s.log.Debug("ssh rejecting unknown channel", "type", chType)
			newChan.Reject(gossh.UnknownChannelType, "unsupported channel type")
			continue
		}
		s.log.Debug("ssh accepting channel", "type", chType)
		channel, requests, err := newChan.Accept()
		if err != nil {
			s.log.Warn("accept channel failed", "err", err)
			continue
		}
		go s.handleSession(channel, requests, username)
	}
}
