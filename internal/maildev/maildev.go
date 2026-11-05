// Package maildev provides an in-memory SMTP server and web GUI for
// development and testing. Emails are organized into per-recipient inboxes
// that are created on the fly when a message is delivered.
package maildev

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"sort"
	"strings"
	"sync"
	"time"
)

// Message represents a captured email.
type Message struct {
	ID       string
	From     string
	To       []string
	Subject  string
	Body     string // plain text body
	HTMLBody string // HTML body (from multipart/alternative)
	Date     time.Time
	Raw      []byte // full RFC 822 message
}

// Inbox represents a per-address mailbox.
type Inbox struct {
	Address  string
	Messages []Message
}

// Mailbox stores captured emails organized by recipient address.
// Inboxes are created on the fly when a message is delivered to a new address.
type Mailbox struct {
	mu      sync.Mutex
	inboxes map[string]*Inbox
	all     []Message
}

// New creates an empty mailbox.
func New() *Mailbox {
	return &Mailbox{inboxes: make(map[string]*Inbox)}
}

// Deliver stores a message and routes it to recipient inboxes.
func (m *Mailbox) Deliver(msg Message) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if msg.ID == "" {
		msg.ID = randomID()
	}
	if msg.Date.IsZero() {
		msg.Date = time.Now()
	}

	m.all = append(m.all, msg)

	for _, addr := range msg.To {
		addr = strings.ToLower(strings.TrimSpace(addr))
		inbox, ok := m.inboxes[addr]
		if !ok {
			inbox = &Inbox{Address: addr}
			m.inboxes[addr] = inbox
		}
		inbox.Messages = append(inbox.Messages, msg)
	}
}

// All returns all messages newest first.
func (m *Mailbox) All() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Message, len(m.all))
	for i, msg := range m.all {
		out[len(m.all)-1-i] = msg
	}
	return out
}

// Last returns the most recently delivered message, or nil if empty.
func (m *Mailbox) Last() *Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.all) == 0 {
		return nil
	}
	msg := m.all[len(m.all)-1]
	return &msg
}

// Clear deletes all messages and inboxes.
func (m *Mailbox) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.all = nil
	m.inboxes = make(map[string]*Inbox)
}

// Count returns the total number of messages.
func (m *Mailbox) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.all)
}

// Inboxes returns all inboxes sorted by address.
func (m *Mailbox) Inboxes() []Inbox {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Inbox, 0, len(m.inboxes))
	for _, inbox := range m.inboxes {
		out = append(out, *inbox)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

// ForRecipient returns messages for a specific email address (newest first).
func (m *Mailbox) ForRecipient(email string) []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	inbox, ok := m.inboxes[strings.ToLower(email)]
	if !ok {
		return nil
	}
	out := make([]Message, len(inbox.Messages))
	for i, msg := range inbox.Messages {
		out[len(inbox.Messages)-1-i] = msg
	}
	return out
}

// InboxCount returns the number of distinct recipient inboxes.
func (m *Mailbox) InboxCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inboxes)
}

// MessageByID returns a message by its ID.
func (m *Mailbox) MessageByID(id string) *Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.all {
		if m.all[i].ID == id {
			msg := m.all[i]
			return &msg
		}
	}
	return nil
}

// CaptureHook returns a function compatible with email.SendHook that
// delivers messages into this mailbox by parsing raw RFC 822 data.
func (m *Mailbox) CaptureHook() func(to []string, raw []byte) {
	return func(to []string, raw []byte) {
		msg := ParseRaw(to, raw)
		m.Deliver(msg)
	}
}

// ParseRaw parses a raw RFC 822 message into a Message.
func ParseRaw(to []string, raw []byte) Message {
	msg := Message{
		ID:  randomID(),
		To:  to,
		Raw: raw,
	}

	parsed, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		// Show what arrived rather than drop it.
		log.Printf("[maildev] parse message: %v", err)
		msg.Body = string(raw)
		return msg
	}

	msg.From = parsed.Header.Get("From")
	msg.Subject = parsed.Header.Get("Subject")
	if dec, err := new(mime.WordDecoder).DecodeHeader(msg.Subject); err == nil {
		msg.Subject = dec
	} else {
		log.Printf("[maildev] decode subject: %v", err)
	}
	if msg.Date, err = parsed.Header.Date(); err != nil {
		msg.Date = time.Now()
	}

	contentType := parsed.Header.Get("Content-Type")
	mediaType, params, _ := mime.ParseMediaType(contentType)

	if mediaType == "multipart/alternative" {
		boundary := params["boundary"]
		if boundary != "" {
			parseMultipart(parsed.Body, boundary, &msg)
			return msg
		}
	}

	body, err := io.ReadAll(parsed.Body)
	if err != nil {
		log.Printf("[maildev] read message body: %v", err)
	}
	msg.Body = string(body)

	return msg
}

func parseMultipart(r io.Reader, boundary string, msg *Message) {
	mr := multipart.NewReader(r, boundary)
	for {
		part, err := mr.NextPart()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("[maildev] read multipart: %v", err)
			}
			break
		}
		ct := part.Header.Get("Content-Type")
		body, err := io.ReadAll(part)
		if err != nil {
			log.Printf("[maildev] read part %q: %v", ct, err)
		}
		mt, _, _ := mime.ParseMediaType(ct)
		switch mt {
		case "text/plain":
			msg.Body = string(body)
		case "text/html":
			msg.HTMLBody = string(body)
		}
	}
}

func randomID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
