package maildev_test

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/maildev"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// --- Mailbox basics ---

func TestMailbox_DeliverAndAll(t *testing.T) {
	mb := maildev.New()
	require.Equal(t, 0, mb.Count())

	mb.Deliver(maildev.Message{From: "a@x", To: []string{"b@y"}, Subject: "s1"})
	mb.Deliver(maildev.Message{From: "a@x", To: []string{"b@y"}, Subject: "s2"})

	require.Equal(t, 2, mb.Count())
	all := mb.All()
	require.Equal(t, 2, len(all))
	// All returns newest first.
	require.Equal(t, "s2", all[0].Subject)
	require.Equal(t, "s1", all[1].Subject)
}

func TestMailbox_AssignsIDAndDate(t *testing.T) {
	mb := maildev.New()
	mb.Deliver(maildev.Message{To: []string{"a@x"}})

	last := mb.Last()
	require.NotNil(t, last)
	require.True(t, last.ID != "")
	require.False(t, last.Date.IsZero())
}

func TestMailbox_LastEmpty(t *testing.T) {
	mb := maildev.New()
	require.Nil(t, mb.Last())
}

func TestMailbox_Clear(t *testing.T) {
	mb := maildev.New()
	mb.Deliver(maildev.Message{To: []string{"a@x"}})
	require.Equal(t, 1, mb.Count())
	require.Equal(t, 1, mb.InboxCount())

	mb.Clear()
	require.Equal(t, 0, mb.Count())
	require.Equal(t, 0, mb.InboxCount())
}

func TestMailbox_Inboxes_SortedAndPerRecipient(t *testing.T) {
	mb := maildev.New()
	mb.Deliver(maildev.Message{To: []string{"charlie@x"}})
	mb.Deliver(maildev.Message{To: []string{"alice@x"}})
	mb.Deliver(maildev.Message{To: []string{"bob@x"}})
	mb.Deliver(maildev.Message{To: []string{"alice@x"}}) // second to alice

	inboxes := mb.Inboxes()
	require.Equal(t, 3, len(inboxes))
	require.Equal(t, "alice@x", inboxes[0].Address)
	require.Equal(t, "bob@x", inboxes[1].Address)
	require.Equal(t, "charlie@x", inboxes[2].Address)

	require.Equal(t, 2, len(inboxes[0].Messages))
	require.Equal(t, 1, len(inboxes[1].Messages))
}

func TestMailbox_ForRecipient_CaseInsensitive(t *testing.T) {
	mb := maildev.New()
	mb.Deliver(maildev.Message{To: []string{"Alice@Example.com"}, Subject: "hi"})

	msgs := mb.ForRecipient("alice@example.com")
	require.Equal(t, 1, len(msgs))
	require.Equal(t, "hi", msgs[0].Subject)
}

func TestMailbox_ForRecipient_Unknown(t *testing.T) {
	mb := maildev.New()
	require.Nil(t, mb.ForRecipient("nobody@x"))
}

func TestMailbox_ForRecipient_NewestFirst(t *testing.T) {
	mb := maildev.New()
	for _, s := range []string{"s1", "s2", "s3"} {
		mb.Deliver(maildev.Message{To: []string{"a@x"}, Subject: s})
	}
	got := mb.ForRecipient("a@x")
	require.Equal(t, 3, len(got))
	require.Equal(t, "s3", got[0].Subject)
	require.Equal(t, "s1", got[2].Subject)
}

func TestMailbox_MessageByID(t *testing.T) {
	mb := maildev.New()
	mb.Deliver(maildev.Message{ID: "abc123", To: []string{"a@x"}})

	got := mb.MessageByID("abc123")
	require.NotNil(t, got)
	require.Equal(t, "abc123", got.ID)

	require.Nil(t, mb.MessageByID("no-such-id"))
}

// --- CaptureHook + ParseRaw ---

func TestCaptureHook_ParsesPlainEmail(t *testing.T) {
	mb := maildev.New()
	hook := mb.CaptureHook()

	raw := []byte("From: a@x\r\n" +
		"To: b@y\r\n" +
		"Subject: hello\r\n" +
		"Date: Tue, 28 May 2024 12:00:00 GMT\r\n" +
		"\r\n" +
		"body text\r\n")
	hook([]string{"b@y"}, raw)

	msgs := mb.All()
	require.Equal(t, 1, len(msgs))
	require.Equal(t, "a@x", msgs[0].From)
	require.Equal(t, "hello", msgs[0].Subject)
	require.True(t, strings.Contains(msgs[0].Body, "body text"))
}

func TestParseRaw_Multipart(t *testing.T) {
	raw := []byte("From: a@x\r\n" +
		"To: b@y\r\n" +
		"Subject: multi\r\n" +
		"MIME-Version: 1.0\r\n" +
		`Content-Type: multipart/alternative; boundary="bnd"` + "\r\n\r\n" +
		"--bnd\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"plain part\r\n" +
		"--bnd\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<p>html part</p>\r\n" +
		"--bnd--\r\n")

	msg := maildev.ParseRaw([]string{"b@y"}, raw)
	require.True(t, strings.Contains(msg.Body, "plain part"))
	require.True(t, strings.Contains(msg.HTMLBody, "html part"))
}

func TestParseRaw_Malformed_FallsBackToRawBody(t *testing.T) {
	raw := []byte("totally not an email")
	msg := maildev.ParseRaw([]string{"x@x"}, raw)
	require.Equal(t, "totally not an email", msg.Body)
}

func TestParseRaw_DateFallback(t *testing.T) {
	raw := []byte("From: a@x\r\n" +
		"To: b@y\r\n" +
		"Subject: nodate\r\n" +
		"\r\n" +
		"body\r\n")
	msg := maildev.ParseRaw([]string{"b@y"}, raw)
	// No Date header → falls back to time.Now().
	require.False(t, msg.Date.IsZero())
	require.True(t, time.Since(msg.Date) < time.Minute)
}

// --- Web handlers ---

func TestWeb_InboxList(t *testing.T) {
	mb := maildev.New()
	mb.Deliver(maildev.Message{To: []string{"a@x"}, Subject: "s"})
	srv := &maildev.WebServer{Mailbox: mb}

	resp := serveReq(t, srv, "GET", "/", "")
	require.Equal(t, 200, resp.Code)
	require.True(t, strings.Contains(resp.Body.String(), "Maildev"))
	require.True(t, strings.Contains(resp.Body.String(), "a@x"))
}

func TestWeb_InboxList_Empty(t *testing.T) {
	srv := &maildev.WebServer{Mailbox: maildev.New()}
	resp := serveReq(t, srv, "GET", "/", "")
	require.Equal(t, 200, resp.Code)
	require.True(t, strings.Contains(resp.Body.String(), "No emails received"))
}

func TestWeb_Inbox(t *testing.T) {
	mb := maildev.New()
	mb.Deliver(maildev.Message{To: []string{"a@x"}, From: "sender@x", Subject: "subj1"})
	srv := &maildev.WebServer{Mailbox: mb}

	resp := serveReq(t, srv, "GET", "/inbox/a@x", "")
	require.Equal(t, 200, resp.Code)
	require.True(t, strings.Contains(resp.Body.String(), "subj1"))
	require.True(t, strings.Contains(resp.Body.String(), "sender@x"))
}

func TestWeb_Inbox_Empty(t *testing.T) {
	srv := &maildev.WebServer{Mailbox: maildev.New()}
	resp := serveReq(t, srv, "GET", "/inbox/nobody@x", "")
	require.Equal(t, 200, resp.Code)
	require.True(t, strings.Contains(resp.Body.String(), "No messages"))
}

func TestWeb_Message(t *testing.T) {
	mb := maildev.New()
	mb.Deliver(maildev.Message{ID: "abc", To: []string{"a@x"}, Subject: "subj", Body: "plain"})
	srv := &maildev.WebServer{Mailbox: mb}

	resp := serveReq(t, srv, "GET", "/message/abc", "")
	require.Equal(t, 200, resp.Code)
	require.True(t, strings.Contains(resp.Body.String(), "subj"))
	require.True(t, strings.Contains(resp.Body.String(), "plain"))
}

func TestWeb_Message_NotFound(t *testing.T) {
	srv := &maildev.WebServer{Mailbox: maildev.New()}
	resp := serveReq(t, srv, "GET", "/message/missing", "")
	require.Equal(t, 404, resp.Code)
}

func TestWeb_Clear(t *testing.T) {
	mb := maildev.New()
	mb.Deliver(maildev.Message{To: []string{"a@x"}})
	srv := &maildev.WebServer{Mailbox: mb}

	resp := serveReq(t, srv, "POST", "/clear", "")
	require.Equal(t, 303, resp.Code)
	require.Equal(t, 0, mb.Count())
}

func TestWeb_APIMessages_JSON(t *testing.T) {
	mb := maildev.New()
	mb.Deliver(maildev.Message{ID: "x1", To: []string{"a@x"}, Subject: "json"})
	srv := &maildev.WebServer{Mailbox: mb}

	resp := serveReq(t, srv, "GET", "/api/messages", "")
	require.Equal(t, 200, resp.Code)
	require.Equal(t, "application/json", resp.Header().Get("Content-Type"))

	var msgs []map[string]any
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &msgs))
	require.Equal(t, 1, len(msgs))
	require.Equal(t, "json", msgs[0]["Subject"])
}

func TestWeb_HTMLMessage_RendersTabs(t *testing.T) {
	mb := maildev.New()
	mb.Deliver(maildev.Message{ID: "html1", To: []string{"a@x"},
		Subject: "html", Body: "plain", HTMLBody: "<b>html</b>"})
	srv := &maildev.WebServer{Mailbox: mb}

	resp := serveReq(t, srv, "GET", "/message/html1", "")
	require.Equal(t, 200, resp.Code)
	body := resp.Body.String()
	require.True(t, strings.Contains(body, "switchTab"))
}

// --- Server.Start + Close (smoke) ---

func TestServer_StartClose(t *testing.T) {
	// Bind both servers to ephemeral ports.
	s := maildev.NewServer("127.0.0.1:0", "127.0.0.1:0")
	require.NoError(t, s.Start())
	require.NoError(t, s.Close())
}

// --- SMTP end-to-end via net/smtp ---

func TestSMTP_EndToEnd(t *testing.T) {
	mb := maildev.New()
	srv := &maildev.SMTPServer{Mailbox: mb, Addr: "127.0.0.1:0"}
	require.NoError(t, srv.Start())
	defer srv.Close()

	c, err := smtp.Dial(srv.Addr)
	require.NoError(t, err)
	require.NoError(t, c.Hello("test"))
	require.NoError(t, c.Mail("alice@example.com"))
	require.NoError(t, c.Rcpt("bob@example.com"))

	w, err := c.Data()
	require.NoError(t, err)
	_, err = w.Write([]byte("From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: smtp-test\r\n" +
		"\r\n" +
		"hello over SMTP\r\n"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, c.Quit())

	deadline := time.Now().Add(2 * time.Second)
	for mb.Count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	require.Equal(t, 1, mb.Count())
	msg := mb.Last()
	require.Equal(t, "alice@example.com", msg.From)
	require.True(t, strings.Contains(msg.Subject, "smtp-test"))
	require.True(t, strings.Contains(msg.Body, "hello over SMTP"))
}

func TestSMTP_NOOP_RSET_UnknownCommand(t *testing.T) {
	mb := maildev.New()
	srv := &maildev.SMTPServer{Mailbox: mb, Addr: "127.0.0.1:0"}
	require.NoError(t, srv.Start())
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Addr)
	require.NoError(t, err)
	defer conn.Close()

	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	require.True(t, strings.HasPrefix(line, "220"))

	for _, cmd := range []struct {
		send string
		want string
	}{
		{"EHLO test\r\n", "250"},
		{"NOOP\r\n", "250"},
		{"RSET\r\n", "250"},
		{"BADCMD\r\n", "502"},
		{"QUIT\r\n", "221"},
	} {
		_, err := conn.Write([]byte(cmd.send))
		require.NoError(t, err)
		resp, _ := br.ReadString('\n')
		require.True(t, strings.HasPrefix(resp, cmd.want))
	}
}

func TestSMTP_DotStuffing(t *testing.T) {
	mb := maildev.New()
	srv := &maildev.SMTPServer{Mailbox: mb, Addr: "127.0.0.1:0"}
	require.NoError(t, srv.Start())
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Addr)
	require.NoError(t, err)
	defer conn.Close()
	br := bufio.NewReader(conn)
	br.ReadString('\n')

	send := func(s string) {
		conn.Write([]byte(s))
		br.ReadString('\n')
	}
	send("EHLO t\r\n")
	send("MAIL FROM:<a@x>\r\n")
	send("RCPT TO:<b@y>\r\n")
	send("DATA\r\n")

	body := "From: a@x\r\n" +
		"To: b@y\r\n" +
		"Subject: dotstuff\r\n" +
		"\r\n" +
		"..line-starting-with-dot\r\n" +
		".\r\n"
	conn.Write([]byte(body))
	br.ReadString('\n')

	conn.Write([]byte("QUIT\r\n"))
	br.ReadString('\n')

	deadline := time.Now().Add(2 * time.Second)
	for mb.Count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	msg := mb.Last()
	require.NotNil(t, msg)
	require.True(t, strings.Contains(msg.Body, ".line-starting-with-dot"))
	require.False(t, strings.Contains(msg.Body, "..line-starting-with-dot"))
}

// --- helper ---

func serveReq(t *testing.T, srv *maildev.WebServer, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w
}
