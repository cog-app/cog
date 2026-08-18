// Command cog-plugin-grpcping is a minimal process plugin used to smoke-test
// the gRPC bridge. It answers GET /apps/grpcping/ping with "pong".
package main

import (
	"context"
	"net/http"

	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/plugcmd"
)

type pingPlugin struct{}

func (p *pingPlugin) ID() string { return "grpcping" }

func (p *pingPlugin) Register(ctx plugin.PluginContext) error {
	ctx.RegisterAPI("ping", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("pong"))
	}))
	return nil
}

func (p *pingPlugin) Start(_ context.Context) error { return nil }
func (p *pingPlugin) Stop() error                   { return nil }

func main() { plugcmd.Run(&pingPlugin{}) }
