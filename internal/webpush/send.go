package webpush

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Message is one push message before encryption.
type Message struct {
	Body        []byte
	ContentType string // of Body, before encryption
	// TTL is how long the push service keeps the message for a user agent
	// that is not connected (RFC 8030 section 5.2). Zero means deliver now
	// or never.
	TTL time.Duration
	// Topic lets the push service replace an undelivered message with a
	// newer one of the same topic (RFC 8030 section 5.4): at most 32
	// base64url characters, visible to the push service.
	Topic string
	// Urgency is "very-low", "low", "normal" or "high" (RFC 8030 section
	// 5.3); empty sends none, which means normal.
	Urgency string
}

// ErrGone reports that the push service no longer knows the subscription
// (404 or 410): it will never deliver to it again, so it should be removed.
var ErrGone = errors.New("webpush: subscription is gone")

// StatusError is a push service's refusal of a message, other than ErrGone.
type StatusError struct {
	Code int
	// RetryAfter is when the service asked to be tried again, if it did.
	RetryAfter time.Duration
	Body       string // the start of the response, for the log
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("webpush: push service answered %d", e.Code)
	}
	return fmt.Sprintf("webpush: push service answered %d: %s", e.Code, e.Body)
}

// Sender delivers messages to push services.
type Sender struct {
	// Client makes the requests. Endpoints come from clients, so it should
	// be internal/safehttp's.
	Client *http.Client
	VAPID  *VAPID
	Now    func() time.Time
}

// Send encrypts m for the subscription at endpoint and posts it to the push
// service (RFC 8030 section 5).
func (s *Sender) Send(ctx context.Context, endpoint string, k Keys, m Message) error {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("webpush: endpoint %q is not an http(s) URL", endpoint)
	}
	body, err := Encrypt(m.Body, k)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webpush: request: %w", err)
	}
	req.Header.Set("TTL", strconv.FormatInt(int64(m.TTL/time.Second), 10))
	req.Header.Set("Content-Encoding", "aes128gcm")
	if m.ContentType != "" {
		req.Header.Set("Content-Type", m.ContentType)
	} else {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	if m.Topic != "" {
		req.Header.Set("Topic", m.Topic)
	}
	if m.Urgency != "" {
		req.Header.Set("Urgency", m.Urgency)
	}
	if s.VAPID != nil {
		auth, err := s.VAPID.Authorization(endpoint, s.Now())
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", auth)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("webpush: post to push service: %w", err)
	}
	defer resp.Body.Close()
	snippet, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return fmt.Errorf("webpush: read push service response: %w", err)
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	}
	return &StatusError{Code: resp.StatusCode, RetryAfter: retryAfter(resp.Header.Get("Retry-After"), s.Now()), Body: string(snippet)}
}

// retryAfter reads a Retry-After header, in seconds or as a date.
func retryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}
