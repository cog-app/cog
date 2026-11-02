package maildev

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
)

// SMTPServer is a minimal SMTP server that captures emails into a Mailbox.
type SMTPServer struct {
	Mailbox  *Mailbox
	Addr     string
	listener net.Listener
}

// Start begins accepting SMTP connections.
func (s *SMTPServer) Start() error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return fmt.Errorf("smtp listen: %w", err)
	}
	s.listener = ln
	s.Addr = ln.Addr().String()
	go s.accept()
	return nil
}

// Close stops the SMTP server.
func (s *SMTPServer) Close() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func (s *SMTPServer) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("[maildev] smtp accept: %v", err)
			}
			return
		}
		go s.handleConn(conn)
	}
}

func (s *SMTPServer) handleConn(conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	write := func(msg string) { fmt.Fprintf(conn, "%s\r\n", msg) }

	write("220 maildev SMTP ready")

	var from string
	var to []string
	var inData bool
	var data bytes.Buffer

	defer func() {
		if err := scanner.Err(); err != nil {
			log.Printf("[maildev] smtp read: %v", err)
		}
	}()
	for scanner.Scan() {
		line := scanner.Text()

		if inData {
			if line == "." {
				inData = false
				msg := ParseRaw(to, data.Bytes())
				msg.From = from
				if len(msg.To) == 0 {
					msg.To = to
				}
				s.Mailbox.Deliver(msg)
				log.Printf("[maildev] received: %s → %v (%s)", from, to, msg.Subject)
				from = ""
				to = nil
				data.Reset()
				write("250 OK")
				continue
			}
			// Dot-stuffing: lines starting with ".." have one dot removed.
			if strings.HasPrefix(line, "..") {
				line = line[1:]
			}
			data.WriteString(line)
			data.WriteString("\r\n")
			continue
		}

		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			write("250 maildev")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			from = extractAddr(line[10:])
			write("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			to = append(to, extractAddr(line[8:]))
			write("250 OK")
		case upper == "DATA":
			inData = true
			data.Reset()
			write("354 Send data, end with <CRLF>.<CRLF>")
		case upper == "QUIT":
			write("221 Bye")
			return
		case upper == "RSET":
			from = ""
			to = nil
			data.Reset()
			write("250 OK")
		case upper == "NOOP":
			write("250 OK")
		default:
			write("502 Command not implemented")
		}
	}
}

// extractAddr extracts an email address from SMTP angle brackets.
func extractAddr(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "<"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j >= 0 {
			return s[i+1 : i+j]
		}
	}
	return s
}
