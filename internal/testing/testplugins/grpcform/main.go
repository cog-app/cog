// Command cog-plugin-grpcform is a process plugin that reports what a form
// POST carried across the gRPC bridge.
package main

import (
	"context"
	"fmt"
	"net/http"

	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/plugcmd"
)

type formPlugin struct{}

func (p *formPlugin) ID() string { return "grpcform" }

func (p *formPlugin) Register(ctx plugin.PluginContext) error {
	ctx.RegisterAction("POST", "echo", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "field=%q", r.FormValue("field"))
	}))
	return nil
}

func (p *formPlugin) Start(_ context.Context) error { return nil }
func (p *formPlugin) Stop() error                   { return nil }

func main() { plugcmd.Run(&formPlugin{}) }
