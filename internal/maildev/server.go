package maildev

import (
	"errors"
	"fmt"
)

// Server combines the SMTP and Web servers with a shared mailbox.
type Server struct {
	Mailbox *Mailbox
	SMTP    SMTPServer
	Web     WebServer
}

// NewServer creates a maildev server with the given addresses.
func NewServer(smtpAddr, httpAddr string) *Server {
	mbox := New()
	return &Server{
		Mailbox: mbox,
		SMTP:    SMTPServer{Mailbox: mbox, Addr: smtpAddr},
		Web:     WebServer{Mailbox: mbox, Addr: httpAddr},
	}
}

// Start launches both the SMTP and web servers.
func (s *Server) Start() error {
	if err := s.SMTP.Start(); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if err := s.Web.Start(); err != nil {
		if cerr := s.SMTP.Close(); cerr != nil {
			return errors.Join(fmt.Errorf("web: %w", err), fmt.Errorf("close smtp: %w", cerr))
		}
		return fmt.Errorf("web: %w", err)
	}
	return nil
}

// Close stops both servers.
func (s *Server) Close() error {
	var errs []error
	if err := s.SMTP.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close smtp: %w", err))
	}
	if err := s.Web.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close web: %w", err))
	}
	return errors.Join(errs...)
}
