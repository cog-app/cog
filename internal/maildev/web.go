package maildev

import (
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net"
	"net/http"
)

// WebServer serves the maildev web GUI.
type WebServer struct {
	Mailbox  *Mailbox
	Addr     string
	listener net.Listener
	mux      *http.ServeMux
}

// Start begins serving the web GUI.
func (s *WebServer) Start() error {
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /{$}", s.handleInboxList)
	s.mux.HandleFunc("GET /inbox/{address}", s.handleInbox)
	s.mux.HandleFunc("GET /message/{id}", s.handleMessage)
	s.mux.HandleFunc("POST /clear", s.handleClear)
	s.mux.HandleFunc("GET /api/messages", s.handleAPIMessages)

	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	s.listener = ln
	s.Addr = ln.Addr().String()
	go func() {
		if err := http.Serve(ln, s.mux); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("[maildev] web: %v", err)
		}
	}()
	return nil
}

// Close stops the web server.
func (s *WebServer) Close() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// Handler returns the HTTP handler for embedding.
func (s *WebServer) Handler() http.Handler {
	if s.mux == nil {
		s.mux = http.NewServeMux()
		s.mux.HandleFunc("GET /{$}", s.handleInboxList)
		s.mux.HandleFunc("GET /inbox/{address}", s.handleInbox)
		s.mux.HandleFunc("GET /message/{id}", s.handleMessage)
		s.mux.HandleFunc("POST /clear", s.handleClear)
		s.mux.HandleFunc("GET /api/messages", s.handleAPIMessages)
	}
	return s.mux
}

func (s *WebServer) handleInboxList(w http.ResponseWriter, r *http.Request) {
	inboxes := s.Mailbox.Inboxes()
	render(w, tmplInboxList, struct {
		Inboxes    []Inbox
		TotalCount int
	}{
		Inboxes:    inboxes,
		TotalCount: s.Mailbox.Count(),
	})
}

func (s *WebServer) handleInbox(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("address")
	msgs := s.Mailbox.ForRecipient(addr)
	render(w, tmplInbox, struct {
		Address  string
		Messages []Message
	}{
		Address:  addr,
		Messages: msgs,
	})
}

// render executes t. Once it has written, the status is sent, so a failure
// can only be logged.
func render(w http.ResponseWriter, t *template.Template, data any) {
	if err := t.Execute(w, data); err != nil {
		log.Printf("[maildev] render %s: %v", t.Name(), err)
	}
}

func (s *WebServer) handleMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	msg := s.Mailbox.MessageByID(id)
	if msg == nil {
		http.NotFound(w, r)
		return
	}
	render(w, tmplMessage, msg)
}

func (s *WebServer) handleClear(w http.ResponseWriter, r *http.Request) {
	s.Mailbox.Clear()
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *WebServer) handleAPIMessages(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.Mailbox.All()); err != nil {
		log.Printf("[maildev] encode messages: %v", err)
	}
}

const pageCSS = `
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; color: #333; background: #f5f5f5; }
a { color: #0066cc; text-decoration: none; }
a:hover { text-decoration: underline; }
.header { background: #2d3436; color: #fff; padding: 1rem 1.5rem; display: flex; align-items: center; justify-content: space-between; }
.header h1 { font-size: 1.25rem; font-weight: 600; }
.header a { color: #dfe6e9; }
.container { max-width: 960px; margin: 1.5rem auto; padding: 0 1rem; }
.card { background: #fff; border-radius: 8px; box-shadow: 0 1px 3px rgba(0,0,0,0.1); margin-bottom: 1rem; overflow: hidden; }
.card-header { padding: 0.75rem 1rem; border-bottom: 1px solid #eee; font-weight: 600; display: flex; justify-content: space-between; align-items: center; }
.card-body { padding: 1rem; }
table { width: 100%; border-collapse: collapse; }
th, td { padding: 0.5rem 0.75rem; text-align: left; border-bottom: 1px solid #eee; }
th { font-weight: 600; font-size: 0.85rem; color: #636e72; text-transform: uppercase; }
tr:hover { background: #f8f9fa; }
.badge { display: inline-block; background: #0066cc; color: #fff; border-radius: 12px; padding: 0.15rem 0.5rem; font-size: 0.8rem; }
.btn { display: inline-block; padding: 0.4rem 0.8rem; border: 1px solid #ddd; border-radius: 4px; background: #fff; cursor: pointer; font-size: 0.85rem; }
.btn:hover { background: #f0f0f0; }
.btn-danger { border-color: #e74c3c; color: #e74c3c; }
.btn-danger:hover { background: #fde8e8; }
pre { background: #f8f9fa; padding: 1rem; border-radius: 4px; white-space: pre-wrap; word-break: break-word; font-size: 0.9rem; line-height: 1.5; }
.meta { color: #636e72; font-size: 0.85rem; }
.empty { padding: 2rem; text-align: center; color: #999; }
`

var tmplInboxList = template.Must(template.New("inboxes").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Maildev</title><style>` + pageCSS + `</style></head>
<body>
<div class="header">
	<h1>Maildev</h1>
	<span class="meta">{{ .TotalCount }} message(s)</span>
</div>
<div class="container">
	<div class="card">
		<div class="card-header">
			<span>Inboxes</span>
			<form method="post" action="/clear" style="display:inline"><button class="btn btn-danger" type="submit">Clear All</button></form>
		</div>
		{{ if .Inboxes }}
		<table>
			<thead><tr><th>Address</th><th>Messages</th></tr></thead>
			<tbody>
			{{ range .Inboxes }}
			<tr><td><a href="/inbox/{{ .Address }}">{{ .Address }}</a></td><td><span class="badge">{{ len .Messages }}</span></td></tr>
			{{ end }}
			</tbody>
		</table>
		{{ else }}
		<div class="empty">No emails received yet.</div>
		{{ end }}
	</div>
</div>
</body></html>`))

var tmplInbox = template.Must(template.New("inbox").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>{{ .Address }} — Maildev</title><style>` + pageCSS + `</style></head>
<body>
<div class="header">
	<h1><a href="/">Maildev</a> / {{ .Address }}</h1>
</div>
<div class="container">
	<div class="card">
		<div class="card-header">Messages</div>
		{{ if .Messages }}
		<table>
			<thead><tr><th>From</th><th>Subject</th><th>Date</th></tr></thead>
			<tbody>
			{{ range .Messages }}
			<tr><td>{{ .From }}</td><td><a href="/message/{{ .ID }}">{{ .Subject }}</a></td><td class="meta">{{ .Date.Format "2006-01-02 15:04" }}</td></tr>
			{{ end }}
			</tbody>
		</table>
		{{ else }}
		<div class="empty">No messages.</div>
		{{ end }}
	</div>
</div>
</body></html>`))

var tmplMessage = template.Must(template.New("message").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>{{ .Subject }} — Maildev</title><style>` + pageCSS + `
.tabs { display:flex; gap:0; border-bottom:2px solid #eee; margin-bottom:1rem; }
.tab { padding:0.5rem 1rem; cursor:pointer; border-bottom:2px solid transparent; margin-bottom:-2px; color:#666; }
.tab.active { border-bottom-color:#0066cc; color:#0066cc; font-weight:600; }
.tab-content { display:none; }
.tab-content.active { display:block; }
</style></head>
<body>
<div class="header">
	<h1><a href="/">Maildev</a></h1>
</div>
<div class="container">
	<div class="card">
		<div class="card-header">{{ .Subject }}</div>
		<div class="card-body">
			<p class="meta" style="margin-bottom:0.5rem"><strong>From:</strong> {{ .From }}</p>
			<p class="meta" style="margin-bottom:0.5rem"><strong>To:</strong> {{ range $i, $a := .To }}{{ if $i }}, {{ end }}{{ $a }}{{ end }}</p>
			<p class="meta" style="margin-bottom:1rem"><strong>Date:</strong> {{ .Date.Format "2006-01-02 15:04:05" }}</p>
			{{ if .HTMLBody }}
			<div class="tabs">
				<div class="tab active" onclick="switchTab('html',this)">HTML</div>
				<div class="tab" onclick="switchTab('text',this)">Text</div>
			</div>
			<div id="tab-html" class="tab-content active">
				<iframe srcdoc="{{ .HTMLBody }}" style="width:100%;height:400px;border:1px solid #eee;border-radius:4px" sandbox></iframe>
			</div>
			<div id="tab-text" class="tab-content">
				<pre>{{ .Body }}</pre>
			</div>
			<script>
			function switchTab(name, el) {
				document.querySelectorAll('.tab').forEach(function(t){t.classList.remove('active')});
				document.querySelectorAll('.tab-content').forEach(function(t){t.classList.remove('active')});
				el.classList.add('active');
				document.getElementById('tab-'+name).classList.add('active');
			}
			</script>
			{{ else }}
			<pre>{{ .Body }}</pre>
			{{ end }}
		</div>
	</div>
</div>
</body></html>`))
