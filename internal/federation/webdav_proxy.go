package federation

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
)

// WebDAVProxy proxies WebDAV requests to remote servers for received federated shares.
type WebDAVProxy struct {
	Log   *slog.Logger
	Store *Store
	HTTP  *http.Client
}

// NewWebDAVProxy creates a new WebDAV proxy.
func NewWebDAVProxy(log *slog.Logger, store *Store) *WebDAVProxy {
	return &WebDAVProxy{
		Log:   log,
		Store: store,
		// The remote host comes from a share another server sent.
		HTTP: &http.Client{
			Timeout:       60 * time.Second,
			Transport:     safehttp.Transport(),
			CheckRedirect: safehttp.NoRedirect,
		},
	}
}

// Proxy forwards a WebDAV request to the remote server for a federated share.
// subPath is the path relative to the federated mount point.
func (p *WebDAVProxy) Proxy(w http.ResponseWriter, r *http.Request, fs *FederatedShare, subPath string) {
	// Build remote URL.
	remoteBase := normalizeHost(fs.RemoteHost) + "/public.php/webdav"
	remoteURL := remoteBase + subPath

	// Create proxied request.
	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, remoteURL, r.Body)
	if err != nil {
		http.Error(w, "proxy error: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Copy relevant headers.
	for _, h := range []string{"Depth", "Content-Type", "Content-Length", "Destination", "Overwrite"} {
		if v := r.Header.Get(h); v != "" {
			proxyReq.Header.Set(h, v)
		}
	}

	// Authenticate with the shared secret token.
	proxyReq.SetBasicAuth("", fs.Token)

	resp, err := p.HTTP.Do(proxyReq)
	if err != nil {
		p.Log.Error("federation proxy failed", "remote", fs.RemoteHost, "err", err)
		http.Error(w, "remote server unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers.
	for k, v := range resp.Header {
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}

	// Rewrite hrefs in PROPFIND responses.
	if r.Method == "PROPFIND" && resp.StatusCode == 207 {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			p.Log.Error("federation proxy read failed", "remote", fs.RemoteHost, "err", err)
			http.Error(w, "proxy read error", http.StatusBadGateway)
			return
		}
		// Replace remote hrefs with local mount path hrefs.
		rewritten := strings.ReplaceAll(string(body), remoteBase, fs.MountPath)
		w.WriteHeader(resp.StatusCode)
		io.WriteString(w, rewritten)
		return
	}

	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		// The status is already sent; the client sees a truncated body.
		p.Log.Error("federation proxy copy failed", "remote", fs.RemoteHost, "err", err)
	}
}
