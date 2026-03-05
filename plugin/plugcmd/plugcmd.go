// Package plugcmd provides the entry point for building external Cog plugin binaries.
// A plugin binary calls plugcmd.Run with its plugin implementation; the function
// starts a gRPC server on the unix socket specified by the COG_PLUGIN_SOCKET
// environment variable and serves requests from the Cog host.
package plugcmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/plugpb"
	"google.golang.org/grpc"
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

	srv := grpc.NewServer()
	plugpb.RegisterPluginServiceServer(srv, &pluginServer{plugin: p})

	if err := srv.Serve(lis); err != nil {
		log.Fatalf("grpc serve: %v", err)
	}
}

// pluginServer implements plugpb.PluginServiceServer by delegating to a cogplugin.Plugin.
type pluginServer struct {
	plugpb.UnimplementedPluginServiceServer
	plugin cogplugin.Plugin

	mu       sync.Mutex
	handlers map[string]http.Handler        // route_key -> handler
	widgets  map[string]cogplugin.Widget     // widget_key -> widget
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

	reg := &capturingRegistrar{
		pluginID: s.plugin.ID(),
		stream:   stream,
		server:   s,
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

// responseCapture is a minimal http.ResponseWriter for capturing plugin responses.
type responseCapture struct {
	header     http.Header
	statusCode int
	body       bytes.Buffer
}

func (r *responseCapture) Header() http.Header         { return r.header }
func (r *responseCapture) WriteHeader(code int)         { r.statusCode = code }
func (r *responseCapture) Write(b []byte) (int, error)  { return r.body.Write(b) }
