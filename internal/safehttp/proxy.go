package safehttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Proxy is an HTTP proxy on the loopback interface that holds the programs
// which make their own connections (yt-dlp) to the same policy as Client:
// every CONNECT and every plain request is checked against the blocked
// address ranges before it is dialed, redirects included, since the program
// follows them through the proxy. A program is pointed at it with
// --proxy http://127.0.0.1:<port>.
type Proxy struct {
	Log *slog.Logger

	once sync.Once
	addr string
	err  error
	srv  *http.Server
}

// Addr starts the proxy on first use and returns its URL.
func (p *Proxy) Addr() (string, error) {
	p.once.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			p.err = fmt.Errorf("safehttp proxy: listen: %w", err)
			return
		}
		p.addr = "http://" + ln.Addr().String()
		p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := p.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				p.log().Error("safehttp proxy", "err", err)
			}
		}()
	})
	return p.addr, p.err
}

// Close stops the proxy.
func (p *Proxy) Close() error {
	if p.srv == nil {
		return nil
	}
	return p.srv.Close()
}

func (p *Proxy) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "proxy request required", http.StatusBadRequest)
		return
	}
	p.forward(w, r)
}

// connect tunnels a TLS connection to a host that passes the policy.
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "bad CONNECT target", http.StatusBadRequest)
		return
	}
	if err := CheckHost(r.Context(), host); err != nil {
		p.log().Warn("safehttp proxy: refused CONNECT", "host", r.Host, "err", err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	upstream, err := DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		p.log().Warn("safehttp proxy: dial", "host", r.Host, "err", err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "proxy cannot hijack", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		p.log().Error("safehttp proxy: hijack", "err", err)
		return
	}
	defer client.Close()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	go func() {
		_, err := io.Copy(upstream, buf)
		if err != nil && !errors.Is(err, net.ErrClosed) {
			p.log().Debug("safehttp proxy: client to upstream", "err", err)
		}
		done <- struct{}{}
	}()
	go func() {
		_, err := io.Copy(client, upstream)
		if err != nil && !errors.Is(err, net.ErrClosed) {
			p.log().Debug("safehttp proxy: upstream to client", "err", err)
		}
		done <- struct{}{}
	}()
	<-done
}

// forward relays a plain HTTP request through the guarded transport. The
// program follows redirects itself, so each hop comes back here.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	req := r.Clone(context.WithoutCancel(r.Context()))
	req.RequestURI = ""
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		req.Header.Del(h)
	}
	resp, err := Transport().RoundTrip(req)
	if err != nil {
		p.log().Warn("safehttp proxy: request", "url", r.URL.String(), "err", err)
		status := http.StatusBadGateway
		if errors.Is(err, ErrBlockedIP) {
			status = http.StatusForbidden
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		if strings.EqualFold(k, "Connection") || strings.EqualFold(k, "Keep-Alive") || strings.EqualFold(k, "Transfer-Encoding") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		p.log().Debug("safehttp proxy: response body", "err", err)
	}
}
