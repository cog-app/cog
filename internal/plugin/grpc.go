package plugin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/plugpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// grpcPlugin adapts an external plugin process into the Plugin interface.
type grpcPlugin struct {
	id     string
	conn   *grpc.ClientConn
	client plugpb.PluginServiceClient
	cmd    *exec.Cmd
}

func (g *grpcPlugin) ID() string { return g.id }

func (g *grpcPlugin) Register(registrar cogplugin.Registrar) error {
	stream, err := g.client.Register(context.Background(), &plugpb.Empty{})
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
				log.Printf("grpc plugin %s: hook %s fired (no-op over gRPC)", g.id, e.Hook.HookName)
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

func (g *grpcPlugin) Start(ctx context.Context) error {
	_, err := g.client.Start(ctx, &plugpb.Empty{})
	return err
}

func (g *grpcPlugin) Stop() error {
	_, err := g.client.Stop(context.Background(), &plugpb.Empty{})
	g.conn.Close()
	if g.cmd != nil && g.cmd.Process != nil {
		g.cmd.Process.Kill()
		g.cmd.Wait()
	}
	return err
}

// proxyHandler returns an http.Handler that forwards requests to the plugin via gRPC.
func (g *grpcPlugin) proxyHandler(routeKey string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		headers := make(map[string]*plugpb.HeaderValues, len(r.Header))
		for k, v := range r.Header {
			headers[k] = &plugpb.HeaderValues{Values: v}
		}

		resp, err := g.client.HandleHTTPRequest(r.Context(), &plugpb.HTTPRequest{
			RouteKey: routeKey,
			Method:   r.Method,
			Path:     r.URL.Path,
			Query:    r.URL.RawQuery,
			Headers:  headers,
			Body:     body,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("plugin %s error: %v", g.id, err), http.StatusBadGateway)
			return
		}

		for k, vals := range resp.Headers {
			for _, v := range vals.Values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(int(resp.StatusCode))
		w.Write(resp.Body)
	})
}

// resourceProxyHandler returns a ResourceHandler that proxies to the plugin.
func (g *grpcPlugin) resourceProxyHandler(routeKey string) cogplugin.ResourceHandlerFunc {
	return func(w http.ResponseWriter, r *http.Request, resourceID string) {
		body, _ := io.ReadAll(r.Body)
		headers := make(map[string]*plugpb.HeaderValues, len(r.Header))
		for k, v := range r.Header {
			headers[k] = &plugpb.HeaderValues{Values: v}
		}

		resp, err := g.client.HandleHTTPRequest(r.Context(), &plugpb.HTTPRequest{
			RouteKey:   routeKey,
			Method:     r.Method,
			Path:       r.URL.Path,
			Query:      r.URL.RawQuery,
			Headers:    headers,
			Body:       body,
			ResourceId: resourceID,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("plugin %s error: %v", g.id, err), http.StatusBadGateway)
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
}

// widgetRenderer returns a Widget.Render function that renders via gRPC.
func (g *grpcPlugin) widgetRenderer(widgetKey string) func(ctx context.Context, w io.Writer, userID string) error {
	return func(ctx context.Context, w io.Writer, userID string) error {
		resp, err := g.client.RenderWidget(ctx, &plugpb.RenderWidgetRequest{
			WidgetKey: widgetKey,
			UserId:    userID,
		})
		if err != nil {
			return err
		}
		_, err = io.Copy(w, bytes.NewReader(resp.Html))
		return err
	}
}

// LoadExternal launches an external plugin binary and connects to it via gRPC.
// The binary is started with COG_PLUGIN_SOCKET set to a temporary unix socket.
func (r *Registry) LoadExternal(binaryPath string) error {
	// Create temp socket.
	tmpDir, err := os.MkdirTemp("", "cog-plugin-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	socketPath := filepath.Join(tmpDir, "plugin.sock")

	// Launch the plugin process.
	cmd := exec.Command(binaryPath)
	cmd.Env = append(os.Environ(), "COG_PLUGIN_SOCKET="+socketPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("start plugin %s: %w", binaryPath, err)
	}

	// Wait for the socket to appear.
	if err := waitForSocket(socketPath); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		os.RemoveAll(tmpDir)
		return fmt.Errorf("plugin socket not ready: %w", err)
	}

	// Connect via gRPC.
	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		os.RemoveAll(tmpDir)
		return fmt.Errorf("grpc connect: %w", err)
	}

	client := plugpb.NewPluginServiceClient(conn)

	// Get the plugin ID.
	idResp, err := client.GetID(context.Background(), &plugpb.Empty{})
	if err != nil {
		conn.Close()
		cmd.Process.Kill()
		cmd.Wait()
		os.RemoveAll(tmpDir)
		return fmt.Errorf("grpc GetID: %w", err)
	}

	gp := &grpcPlugin{
		id:     idResp.Id,
		conn:   conn,
		client: client,
		cmd:    cmd,
	}

	return r.Add(gp)
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
