// Package plugcmd provides the entry point for building external Cog plugin binaries.
// A plugin binary calls plugcmd.Run with its plugin implementation; the function
// starts a gRPC server on the unix socket specified by the COG_PLUGIN_SOCKET
// environment variable and serves requests from the Cog host.
package plugcmd

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"sync"
	"time"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/plugpb"
	"git.duckfam.us/jj-software/cog/vfs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Run starts a gRPC server for the given plugin on the unix socket path
// provided by the COG_PLUGIN_SOCKET environment variable.
// This function blocks until the host closes the connection or Stop is called.
func Run(p cogplugin.Plugin) {
	socketPath := os.Getenv("COG_PLUGIN_SOCKET")
	if socketPath == "" {
		log.Fatal("COG_PLUGIN_SOCKET not set")
	}

	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("listen %s: %v", socketPath, err)
	}

	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(recoverUnary),
		grpc.ChainStreamInterceptor(recoverStream),
	)
	plugpb.RegisterPluginServiceServer(srv, &pluginServer{plugin: p})

	if err := srv.Serve(lis); err != nil {
		log.Fatalf("grpc serve: %v", err)
	}
}

// recoverUnary turns a panic in a plugin handler into an error for the host.
//
// Without it the panic unwinds out of the gRPC handler and kills the plugin
// process. The host is still holding the socket path that process was listening
// on, so every later request fails with "connection refused" until the whole
// server restarts — one bad request takes the app down for good. The host gives
// in-process plugins the same protection; this is the out-of-process half.
//
// A panic in a goroutine the plugin starts itself is still fatal: nothing here
// can see it. Plugins must recover in their own goroutines.
func recoverUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			logPanic(info.FullMethod, rec)
			err = status.Errorf(codes.Internal, "plugin panic: %v", rec)
		}
	}()
	return handler(ctx, req)
}

// recoverStream is recoverUnary for the streaming methods (Register).
func recoverStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			logPanic(info.FullMethod, rec)
			err = status.Errorf(codes.Internal, "plugin panic: %v", rec)
		}
	}()
	return handler(srv, ss)
}

// logPanic writes the panic and its stack to stderr, which the host captures
// into its own log.
func logPanic(method string, rec any) {
	log.Printf("panic in plugin handler %s: %v\n%s", method, rec, debug.Stack())
}

// pluginServer implements plugpb.PluginServiceServer by delegating to a cogplugin.Plugin.
type pluginServer struct {
	plugpb.UnimplementedPluginServiceServer
	plugin cogplugin.Plugin

	mu       sync.Mutex
	handlers map[string]http.Handler     // route_key -> handler
	widgets  map[string]cogplugin.Widget // widget_key -> widget
	hooks    map[string][]cogplugin.HookCallback
}

func (s *pluginServer) GetID(_ context.Context, _ *plugpb.Empty) (*plugpb.IDResponse, error) {
	return &plugpb.IDResponse{Id: s.plugin.ID()}, nil
}

func (s *pluginServer) Register(_ *plugpb.Empty, stream grpc.ServerStreamingServer[plugpb.RegistrationEvent]) error {
	s.mu.Lock()
	s.handlers = make(map[string]http.Handler)
	s.widgets = make(map[string]cogplugin.Widget)
	s.hooks = make(map[string][]cogplugin.HookCallback)
	s.mu.Unlock()

	base := &capturingRegistrar{
		pluginID: s.plugin.ID(),
		stream:   stream,
		server:   s,
	}

	// Use trusted registrar if trust env vars are set.
	var reg cogplugin.PluginContext
	if tr := newTrustedRegistrar(base); tr != nil {
		reg = tr
	} else {
		reg = base
	}

	return s.plugin.Register(reg)
}

func (s *pluginServer) Start(ctx context.Context, _ *plugpb.Empty) (*plugpb.Empty, error) {
	return &plugpb.Empty{}, s.plugin.Start(ctx)
}

func (s *pluginServer) Stop(_ context.Context, _ *plugpb.Empty) (*plugpb.Empty, error) {
	return &plugpb.Empty{}, s.plugin.Stop()
}

func (s *pluginServer) HandleHTTPRequest(_ context.Context, req *plugpb.HTTPRequest) (*plugpb.HTTPResponse, error) {
	s.mu.Lock()
	handler, ok := s.handlers[req.RouteKey]
	s.mu.Unlock()
	if !ok {
		return &plugpb.HTTPResponse{StatusCode: 404, Body: []byte("handler not found")}, nil
	}

	// Reconstruct http.Request.
	httpReq := &http.Request{
		Method: req.Method,
		URL:    &url.URL{Path: req.Path, RawQuery: req.Query},
		Header: make(http.Header),
		Body:   io.NopCloser(bytes.NewReader(req.Body)),
	}
	for k, vals := range req.Headers {
		httpReq.Header[k] = vals.Values
	}

	rec := &responseCapture{header: make(http.Header), statusCode: 200}
	handler.ServeHTTP(rec, httpReq)

	respHeaders := make(map[string]*plugpb.HeaderValues, len(rec.header))
	for k, v := range rec.header {
		respHeaders[k] = &plugpb.HeaderValues{Values: v}
	}

	return &plugpb.HTTPResponse{
		StatusCode: int32(rec.statusCode),
		Headers:    respHeaders,
		Body:       rec.body.Bytes(),
	}, nil
}

func (s *pluginServer) RenderWidget(_ context.Context, req *plugpb.RenderWidgetRequest) (*plugpb.RenderWidgetResponse, error) {
	s.mu.Lock()
	w, ok := s.widgets[req.WidgetKey]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("widget %q not found", req.WidgetKey)
	}

	var buf bytes.Buffer
	if err := w.Render(context.Background(), &buf, req.UserId); err != nil {
		return nil, err
	}
	return &plugpb.RenderWidgetResponse{Html: buf.Bytes()}, nil
}

// capturingRegistrar implements cogplugin.Registrar by streaming events to the host.
type capturingRegistrar struct {
	pluginID string
	stream   grpc.ServerStreamingServer[plugpb.RegistrationEvent]
	server   *pluginServer
	keySeq   int
	schema   fs.FS
}

// RegisterSchema records the plugin's schema. A trusted plugin applies it when
// opening a database; an untrusted one has no database access at all, so the
// registration is kept but unused until the protocol can carry it host-side.
func (r *capturingRegistrar) RegisterSchema(fsys fs.FS) {
	r.schema = fsys
}

func (r *capturingRegistrar) nextKey(prefix string) string {
	r.keySeq++
	return fmt.Sprintf("%s:%s:%d", r.pluginID, prefix, r.keySeq)
}

func (r *capturingRegistrar) storeHandler(key string, h http.Handler) {
	r.server.mu.Lock()
	r.server.handlers[key] = h
	r.server.mu.Unlock()
}

func (r *capturingRegistrar) RegisterAPI(path string, handler http.Handler) {
	key := r.nextKey("api")
	r.storeHandler(key, handler)
	r.stream.Send(&plugpb.RegistrationEvent{
		Event: &plugpb.RegistrationEvent_Api{
			Api: &plugpb.RegisterAPIEvent{Path: path, RouteKey: key},
		},
	})
}

func (r *capturingRegistrar) RegisterPage(path string, handler http.Handler) {
	key := r.nextKey("page")
	r.storeHandler(key, handler)
	r.stream.Send(&plugpb.RegistrationEvent{
		Event: &plugpb.RegistrationEvent_Page{
			Page: &plugpb.RegisterPageEvent{Path: path, RouteKey: key},
		},
	})
}

func (r *capturingRegistrar) RegisterAction(method, path string, handler http.Handler) {
	key := r.nextKey("action")
	r.storeHandler(key, handler)
	r.stream.Send(&plugpb.RegistrationEvent{
		Event: &plugpb.RegistrationEvent_Action{
			Action: &plugpb.RegisterActionEvent{Method: method, Path: path, RouteKey: key},
		},
	})
}

func (r *capturingRegistrar) RegisterResource(def cogplugin.ResourceDef) {
	key := r.nextKey("resource")
	if def.Handler != nil {
		r.storeHandler(key, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			resourceID := req.URL.Query().Get("resource_id")
			def.Handler.ServeResource(w, req, resourceID)
		}))
	}
	r.stream.Send(&plugpb.RegistrationEvent{
		Event: &plugpb.RegistrationEvent_Resource{
			Resource: &plugpb.RegisterResourceEvent{
				Type:         def.Type,
				Table:        def.Table,
				IdColumn:     def.IDColumn,
				ParentColumn: def.ParentColumn,
				RouteKey:     key,
			},
		},
	})
}

func (r *capturingRegistrar) RegisterHook(hookName string, callback cogplugin.HookCallback) {
	r.server.mu.Lock()
	r.server.hooks[hookName] = append(r.server.hooks[hookName], callback)
	r.server.mu.Unlock()
	r.stream.Send(&plugpb.RegistrationEvent{
		Event: &plugpb.RegistrationEvent_Hook{
			Hook: &plugpb.RegisterHookEvent{HookName: hookName},
		},
	})
}

func (r *capturingRegistrar) RegisterNavItem(item cogplugin.NavItem) {
	r.stream.Send(&plugpb.RegistrationEvent{
		Event: &plugpb.RegistrationEvent_NavItem{
			NavItem: &plugpb.RegisterNavItemEvent{
				Label: item.Label,
				Url:   item.URL,
				Icon:  item.Icon,
			},
		},
	})
}

func (r *capturingRegistrar) RegisterWidget(w cogplugin.Widget) {
	key := r.nextKey("widget")
	r.server.mu.Lock()
	r.server.widgets[key] = w
	r.server.mu.Unlock()
	r.stream.Send(&plugpb.RegistrationEvent{
		Event: &plugpb.RegistrationEvent_Widget{
			Widget: &plugpb.RegisterWidgetEvent{
				Title:     w.Title,
				Priority:  int32(w.Priority),
				WidgetKey: key,
			},
		},
	})
}

func (r *capturingRegistrar) RegisterSearchSource(def cogplugin.SearchSourceDef) {
	// Search source registration is handled host-side; for gRPC plugins
	// we send it as a registration event. For now, this is a no-op stub
	// since gRPC search source support requires protocol extension.
}

// --- PluginContext service stubs (not available for gRPC plugins) ---

func (r *capturingRegistrar) PluginDB(username string, opts ...cogplugin.DBOption) (*sql.DB, error) {
	return nil, fmt.Errorf("PluginDB not available for gRPC plugins")
}
func (r *capturingRegistrar) UserFilesDir(username string) string { return "" }
func (r *capturingRegistrar) EnsureUserDirs(username string) error {
	return fmt.Errorf("EnsureUserDirs not available for gRPC plugins")
}
func (r *capturingRegistrar) DataDir() string                      { return "" }
func (r *capturingRegistrar) NavItems() []cogplugin.NavItem        { return nil }
func (r *capturingRegistrar) Widgets() []cogplugin.Widget          { return nil }
func (r *capturingRegistrar) CSRFSecret() []byte                   { return nil }
func (r *capturingRegistrar) ShareStore() cogplugin.ShareStore     { return nil }
func (r *capturingRegistrar) EmailSender() cogplugin.EmailSender   { return nil }
func (r *capturingRegistrar) UserLookup() cogplugin.UserLookup     { return nil }
func (r *capturingRegistrar) GroupStore() cogplugin.GroupStore     { return nil }
func (r *capturingRegistrar) FilesAccess() cogplugin.FilesAccess   { return nil }
func (r *capturingRegistrar) VolumeAccess() cogplugin.VolumeAccess { return nil }
func (r *capturingRegistrar) QuotaCheck(username string, additionalBytes int64) error {
	return nil
}
func (r *capturingRegistrar) UserVFS(username string) vfs.FS { return nil }
func (r *capturingRegistrar) Logger() *slog.Logger           { return slog.Default() }
func (r *capturingRegistrar) Now() time.Time                 { return time.Now() }
func (r *capturingRegistrar) RootDB() (*sql.DB, error) {
	return nil, fmt.Errorf("RootDB not available for gRPC plugins")
}
func (r *capturingRegistrar) RegisterDAVHandler(prefix string, handler http.Handler) {}
func (r *capturingRegistrar) RegisterSettingsPage(page cogplugin.SettingsPage)       {}
func (r *capturingRegistrar) RegisterCommand(def cogplugin.CommandDef)               {}
func (r *capturingRegistrar) ActivityLog() cogplugin.ActivityLogger                  { return nil }
func (r *capturingRegistrar) Cookies() cogplugin.CookieConfig                        { return cogplugin.CookieConfig{} }

// responseCapture is a minimal http.ResponseWriter for capturing plugin responses.
type responseCapture struct {
	header     http.Header
	statusCode int
	body       bytes.Buffer
}

func (r *responseCapture) Header() http.Header         { return r.header }
func (r *responseCapture) WriteHeader(code int)        { r.statusCode = code }
func (r *responseCapture) Write(b []byte) (int, error) { return r.body.Write(b) }
