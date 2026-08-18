package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/plugpb"
	"git.duckfam.us/jj-software/cog/ui"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// grpcPlugin adapts an external plugin process into the Plugin interface.
type grpcPlugin struct {
	id  string
	log *slog.Logger

	// link is the current connection to the plugin process. It is nil while the
	// process is down, which the proxy handlers report as an outage instead of
	// dialing a socket nobody is listening on.
	link atomic.Pointer[grpcLink]

	// spawn starts a fresh process. Held so the supervisor can restart one.
	spawn func() (*procHandle, error)

	// name resolves the plugin's display name for the error page. It is a
	// function because the nav label it reads is registered after this exists.
	name func() string

	quitOnce sync.Once
	quit     chan struct{} // closed by Stop to end supervision
	done     chan struct{} // closed when supervision has reaped the process

	mu        sync.Mutex
	proc      *procHandle // current process; nil while down
	routeKeys []string    // keys from the first Register, re-checked on restart
}

// grpcLink pairs a connection with its client so both can be swapped atomically
// when the process behind them is replaced.
type grpcLink struct {
	conn   *grpc.ClientConn
	client plugpb.PluginServiceClient
}

// procHandle is one running plugin process and the resources it owns.
type procHandle struct {
	cmd    *exec.Cmd
	tmpDir string
	link   *grpcLink
	stdout *procLogWriter
	stderr *procLogWriter
}

// signalStop asks the process to exit. It deliberately does not reap it: the
// supervisor goroutine owns cmd.Wait, and calling it twice is an error.
func (h *procHandle) signalStop() {
	if h.cmd.Process != nil {
		h.cmd.Process.Kill()
	}
}

// release frees everything except the process itself, which has already been
// reaped by whoever called cmd.Wait.
func (h *procHandle) release() {
	if h.link != nil {
		h.link.conn.Close()
	}
	h.stdout.Flush()
	h.stderr.Flush()
	os.RemoveAll(h.tmpDir)
}

// terminate kills and reaps a process that never reached supervision, so a
// failed startup leaves neither a zombie nor a temp directory behind.
func (h *procHandle) terminate() {
	h.signalStop()
	h.cmd.Wait()
	h.release()
}

func (g *grpcPlugin) ID() string { return g.id }

func (g *grpcPlugin) Register(ctx cogplugin.PluginContext) error {
	registrar := ctx // gRPC plugins only use Registrar methods
	link := g.link.Load()
	if link == nil {
		return fmt.Errorf("plugin %s: process is not running", g.id)
	}
	stream, err := link.client.Register(context.Background(), &plugpb.Empty{})
	if err != nil {
		return fmt.Errorf("grpc Register: %w", err)
	}

	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("grpc Register stream: %w", err)
		}

		if key, ok := routeKeyOf(ev); ok {
			g.routeKeys = append(g.routeKeys, key)
		}

		switch e := ev.Event.(type) {
		case *plugpb.RegistrationEvent_Api:
			registrar.RegisterAPI(e.Api.Path, g.proxyHandler(e.Api.RouteKey))
		case *plugpb.RegistrationEvent_Page:
			registrar.RegisterPage(e.Page.Path, g.proxyHandler(e.Page.RouteKey))
		case *plugpb.RegistrationEvent_Action:
			registrar.RegisterAction(e.Action.Method, e.Action.Path, g.proxyHandler(e.Action.RouteKey))
		case *plugpb.RegistrationEvent_Resource:
			registrar.RegisterResource(cogplugin.ResourceDef{
				Type:         e.Resource.Type,
				Table:        e.Resource.Table,
				IDColumn:     e.Resource.IdColumn,
				ParentColumn: e.Resource.ParentColumn,
				Handler:      g.resourceProxyHandler(e.Resource.RouteKey),
			})
		case *plugpb.RegistrationEvent_Hook:
			registrar.RegisterHook(e.Hook.HookName, func(ctx context.Context, data any) {
				// Hook callbacks over gRPC are fire-and-forget for now.
				registrar.Logger().Debug("hook fired (no-op over gRPC)", "hook", e.Hook.HookName)
			})
		case *plugpb.RegistrationEvent_NavItem:
			registrar.RegisterNavItem(cogplugin.NavItem{
				Label: e.NavItem.Label,
				URL:   e.NavItem.Url,
				Icon:  e.NavItem.Icon,
			})
		case *plugpb.RegistrationEvent_Widget:
			registrar.RegisterWidget(cogplugin.Widget{
				Title:    e.Widget.Title,
				Priority: int(e.Widget.Priority),
				Render:   g.widgetRenderer(e.Widget.WidgetKey),
			})
		}
	}
}

// routeKeyOf returns the route key a registration event carries, if any. Route
// keys are how the host addresses a handler inside the plugin process, so they
// are what has to survive a restart.
func routeKeyOf(ev *plugpb.RegistrationEvent) (string, bool) {
	switch e := ev.Event.(type) {
	case *plugpb.RegistrationEvent_Api:
		return e.Api.RouteKey, true
	case *plugpb.RegistrationEvent_Page:
		return e.Page.RouteKey, true
	case *plugpb.RegistrationEvent_Action:
		return e.Action.RouteKey, true
	case *plugpb.RegistrationEvent_Resource:
		return e.Resource.RouteKey, true
	case *plugpb.RegistrationEvent_Widget:
		return e.Widget.WidgetKey, true
	}
	return "", false
}

func (g *grpcPlugin) Start(ctx context.Context) error {
	link := g.link.Load()
	if link == nil {
		return fmt.Errorf("plugin %s: process is not running", g.id)
	}
	_, err := link.client.Start(ctx, &plugpb.Empty{})
	return err
}

func (g *grpcPlugin) Stop() error {
	var err error
	if link := g.link.Load(); link != nil {
		_, err = link.client.Stop(context.Background(), &plugpb.Empty{})
	}

	// End supervision before killing, so the exit is not treated as a crash
	// and restarted.
	g.quitOnce.Do(func() { close(g.quit) })

	g.mu.Lock()
	proc := g.proc
	g.mu.Unlock()
	if proc != nil {
		proc.signalStop()
	}
	if g.done != nil {
		<-g.done // the supervisor reaps the process and removes its temp dir
	}
	return err
}

// proxyHandler returns an http.Handler that forwards requests to the plugin via gRPC.
func (g *grpcPlugin) proxyHandler(routeKey string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.proxy(w, r, routeKey, "")
	})
}

// resourceProxyHandler returns a ResourceHandler that proxies to the plugin.
func (g *grpcPlugin) resourceProxyHandler(routeKey string) cogplugin.ResourceHandlerFunc {
	return func(w http.ResponseWriter, r *http.Request, resourceID string) {
		g.proxy(w, r, routeKey, resourceID)
	}
}

func (g *grpcPlugin) proxy(w http.ResponseWriter, r *http.Request, routeKey, resourceID string) {
	link := g.link.Load()
	if link == nil {
		g.log.Warn("request for plugin whose process is down",
			"plugin", g.id, "method", r.Method, "path", r.URL.Path)
		g.errorPage(w, r, http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		g.log.Error("read request body for plugin",
			"plugin", g.id, "method", r.Method, "path", r.URL.Path, "err", err)
		g.errorPage(w, r, http.StatusBadRequest)
		return
	}
	headers := make(map[string]*plugpb.HeaderValues, len(r.Header))
	for k, v := range r.Header {
		headers[k] = &plugpb.HeaderValues{Values: v}
	}

	// The host wrapped this handler in injectPluginContext, so the request
	// context already holds who is asking and their CSRF token. None of it
	// crosses a socket by itself: send it, or the plugin serves every request
	// as though nobody were logged in.
	req := &plugpb.HTTPRequest{
		RouteKey:   routeKey,
		Method:     r.Method,
		Path:       r.URL.Path,
		Query:      r.URL.RawQuery,
		Headers:    headers,
		Body:       body,
		ResourceId: resourceID,
		CsrfToken:  cogplugin.CSRFTokenFromContext(r.Context()),
	}
	if u := cogplugin.UserFromContext(r.Context()); u != nil {
		req.UserId = u.ID
		req.Username = u.Username
		req.DisplayName = u.DisplayName
		req.IsAdmin = u.IsAdmin
	}

	resp, err := link.client.HandleHTTPRequest(r.Context(), req)
	if err != nil {
		g.log.Error("plugin request failed",
			"plugin", g.id, "method", r.Method, "path", r.URL.Path, "err", err)
		g.errorPage(w, r, statusForRPCError(err))
		return
	}

	for k, vals := range resp.Headers {
		for _, v := range vals.Values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(int(resp.StatusCode))
	w.Write(resp.Body)
}

// statusForRPCError maps a failed plugin call to a status for the browser. A
// transport failure means the process is gone or going, which is temporary;
// anything else is the plugin reporting a fault, including a panic the plugin's
// recovery interceptor turned into codes.Internal.
func statusForRPCError(err error) int {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// errorPage reports a plugin failure to whoever opened the page. The underlying
// error is logged rather than rendered: a unix socket path and a gRPC dial
// message tell a user nothing and leak host layout into the browser.
func (g *grpcPlugin) errorPage(w http.ResponseWriter, r *http.Request, code int) {
	name := g.displayName()

	var heading, message, retry string
	switch code {
	case http.StatusServiceUnavailable:
		w.Header().Set("Retry-After", "5")
		heading = name + " is temporarily unavailable"
		message = "The app stopped running and is being restarted. This usually takes a few seconds."
		retry = r.URL.Path
	case http.StatusBadRequest:
		heading = name + " could not read that request"
		message = "Something went wrong sending the request. Try again from the page you came from."
	default:
		heading = name + " hit an error"
		message = "The app failed to handle this request. The details are in the server log."
		retry = r.URL.Path
	}

	// Only a GET is safe to offer as a retry link; repeating anything else
	// would resubmit whatever the user just did.
	if r.Method != http.MethodGet {
		retry = ""
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := ui.AppErrorPage(name, heading, message, retry).Render(r.Context(), w); err != nil {
		g.log.Error("render plugin error page", "plugin", g.id, "err", err)
	}
}

// displayName is the plugin's nav label, falling back to its ID. It is resolved
// on each call because a plugin's nav item is registered after the plugin is
// built.
func (g *grpcPlugin) displayName() string {
	if g.name != nil {
		if n := g.name(); n != "" {
			return n
		}
	}
	return g.id
}

// widgetRenderer returns a Widget.Render function that renders via gRPC.
func (g *grpcPlugin) widgetRenderer(widgetKey string) func(ctx context.Context, w io.Writer, userID string) error {
	return func(ctx context.Context, w io.Writer, userID string) error {
		link := g.link.Load()
		if link == nil {
			return fmt.Errorf("plugin %s: process is not running", g.id)
		}
		resp, err := link.client.RenderWidget(ctx, &plugpb.RenderWidgetRequest{
			WidgetKey: widgetKey,
			UserId:    userID,
		})
		if err != nil {
			return fmt.Errorf("plugin %s: render widget: %w", g.id, err)
		}
		_, err = io.Copy(w, bytes.NewReader(resp.Html))
		return err
	}
}

// LoadExternal launches an external plugin binary and connects to it via gRPC.
// The binary is started with COG_PLUGIN_SOCKET set to a temporary unix socket.
func (r *Registry) LoadExternal(binaryPath string) error {
	return r.loadProcessPlugin(binaryPath, false)
}

// LoadExternalTrusted launches a trusted process plugin with full DB/filesystem
// access. Trust is conveyed via environment variables that the plugin SDK reads.
func (r *Registry) LoadExternalTrusted(binaryPath string) error {
	return r.loadProcessPlugin(binaryPath, true)
}

func (r *Registry) loadProcessPlugin(binaryPath string, trusted bool) error {
	spawn := func() (*procHandle, error) { return r.startProcess(binaryPath, trusted) }

	proc, err := spawn()
	if err != nil {
		return err
	}

	idResp, err := proc.link.client.GetID(context.Background(), &plugpb.Empty{})
	if err != nil {
		proc.terminate()
		return fmt.Errorf("grpc GetID: %w", err)
	}

	proc.stdout.setPlugin(idResp.Id)
	proc.stderr.setPlugin(idResp.Id)

	gp := &grpcPlugin{
		id:    idResp.Id,
		log:   r.log,
		spawn: spawn,
		name:  func() string { return r.pluginDisplayName(idResp.Id) },
		quit:  make(chan struct{}),
		done:  make(chan struct{}),
		proc:  proc,
	}
	gp.link.Store(proc.link)

	// Add mounts the plugin's routes, which capture gp — so a later restart can
	// swap the connection underneath them without touching the mux.
	if err := r.Add(gp); err != nil {
		proc.terminate()
		return err
	}

	go gp.supervise()
	return nil
}

// startProcess launches the plugin binary on a fresh socket and connects to it.
// The returned process has not been reaped: its caller either hands it to
// supervise or terminates it.
func (r *Registry) startProcess(binaryPath string, trusted bool) (*procHandle, error) {
	tmpDir, err := os.MkdirTemp("", "cog-plugin-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	socketPath := filepath.Join(tmpDir, "plugin.sock")

	cmd := exec.Command(binaryPath)
	cmd.Env = append(os.Environ(), "COG_PLUGIN_SOCKET="+socketPath)
	if trusted {
		absDataDir, err := filepath.Abs(r.dataDir)
		if err != nil {
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("abs data dir: %w", err)
		}
		cmd.Env = append(cmd.Env,
			"COG_TRUSTED=1",
			"COG_DATA_DIR="+absDataDir,
			"COG_CSRF_SECRET="+base64.StdEncoding.EncodeToString(r.csrfSecret),
		)
	}

	// Route the child's output through the host logger instead of inheriting
	// its file descriptors, so a panic trace lands in the log buffer the admin
	// UI reads and not only on the host's stderr.
	label := strings.TrimPrefix(filepath.Base(binaryPath), "cog-plugin-")
	h := &procHandle{
		cmd:    cmd,
		tmpDir: tmpDir,
		stdout: newProcLogWriter(r.log, slog.LevelInfo, label),
		stderr: newProcLogWriter(r.log, slog.LevelError, label),
	}
	cmd.Stdout = h.stdout
	cmd.Stderr = h.stderr

	if err := cmd.Start(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("start plugin %s: %w", binaryPath, err)
	}

	if err := waitForSocket(socketPath); err != nil {
		h.terminate()
		return nil, fmt.Errorf("plugin socket not ready: %w", err)
	}

	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		h.terminate()
		return nil, fmt.Errorf("grpc connect: %w", err)
	}
	h.link = &grpcLink{conn: conn, client: plugpb.NewPluginServiceClient(conn)}
	return h, nil
}

// Backoff bounds for restarting a crashed process plugin.
const (
	procMinBackoff = time.Second
	procMaxBackoff = 30 * time.Second
)

// supervise reaps the plugin process and restarts it when it exits on its own.
//
// Without this, a plugin that dies leaves its socket file behind: the host keeps
// the plugin marked started, keeps dialing a path with no listener, and answers
// every request with connection refused until the whole server is restarted. The
// dead child also stays a zombie, because nothing ever called Wait.
func (g *grpcPlugin) supervise() {
	defer close(g.done)

	for {
		g.mu.Lock()
		proc := g.proc
		g.mu.Unlock()

		err := proc.cmd.Wait()

		// The process is gone. Stop routing to it before anything else.
		g.link.Store(nil)
		g.mu.Lock()
		g.proc = nil
		g.mu.Unlock()
		proc.release()

		select {
		case <-g.quit:
			return // Stop asked for this exit
		default:
		}
		g.log.Error("process plugin exited", "plugin", g.id, "err", err)

		if !g.restart() {
			return
		}
		g.log.Info("process plugin restarted", "plugin", g.id)

		// Stop may have been called while the restart was in flight, in which
		// case it found no process to signal — this one would outlive the host.
		// Killing it here sends the loop back to Wait and out through quit.
		select {
		case <-g.quit:
			g.mu.Lock()
			proc := g.proc
			g.mu.Unlock()
			if proc != nil {
				proc.signalStop()
			}
		default:
		}
	}
}

// restart relaunches the plugin, backing off between attempts, and reports
// whether a new process is running. It gives up only when Stop is called: a
// plugin that cannot be restarted keeps saying so in the log, and its routes
// keep reporting an outage, which beats silently unmounting an app.
func (g *grpcPlugin) restart() bool {
	backoff := procMinBackoff
	for {
		select {
		case <-time.After(backoff):
		case <-g.quit:
			return false
		}

		proc, err := g.spawn()
		if err != nil {
			g.log.Error("process plugin restart failed", "plugin", g.id, "err", err)
			backoff = min(2*backoff, procMaxBackoff)
			continue
		}
		if err := g.adopt(proc); err != nil {
			g.log.Error("process plugin restart rejected", "plugin", g.id, "err", err)
			proc.terminate()
			backoff = min(2*backoff, procMaxBackoff)
			continue
		}
		return true
	}
}

// adopt points the mounted routes at a newly started process. The process has to
// be the same plugin exposing the same route keys, because the host's routes were
// mounted once and cannot be remounted on a running mux.
func (g *grpcPlugin) adopt(proc *procHandle) error {
	idResp, err := proc.link.client.GetID(context.Background(), &plugpb.Empty{})
	if err != nil {
		return fmt.Errorf("grpc GetID: %w", err)
	}
	if idResp.Id != g.id {
		return fmt.Errorf("binary now reports plugin ID %q, was %q", idResp.Id, g.id)
	}
	proc.stdout.setPlugin(g.id)
	proc.stderr.setPlugin(g.id)

	// Re-run Register against the process only: it rebuilds the plugin's own
	// route-key table. The host side is already mounted, so the events are
	// checked rather than replayed.
	if err := g.reregister(proc.link); err != nil {
		return err
	}
	if _, err := proc.link.client.Start(context.Background(), &plugpb.Empty{}); err != nil {
		return fmt.Errorf("grpc Start: %w", err)
	}

	g.mu.Lock()
	g.proc = proc
	g.mu.Unlock()
	g.link.Store(proc.link)
	return nil
}

func (g *grpcPlugin) reregister(link *grpcLink) error {
	stream, err := link.client.Register(context.Background(), &plugpb.Empty{})
	if err != nil {
		return fmt.Errorf("grpc Register: %w", err)
	}
	var keys []string
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("grpc Register stream: %w", err)
		}
		if key, ok := routeKeyOf(ev); ok {
			keys = append(keys, key)
		}
	}
	if !slices.Equal(keys, g.routeKeys) {
		return fmt.Errorf("route keys changed across restart (had %v, now %v); mounted routes would address the wrong handlers",
			g.routeKeys, keys)
	}
	return nil
}

// waitForSocket polls until the unix socket is connectable.
func waitForSocket(path string) error {
	for range 50 {
		conn, err := net.Dial("unix", path)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", path)
}
