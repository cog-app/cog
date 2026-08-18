// Command cog-plugin-grpccrash is a process plugin that fails in the two ways
// one can take itself down: a panic in a handler, and an outright exit. It is
// how the host's panic containment and process supervision are tested.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/plugcmd"
)

type crashPlugin struct{}

func (p *crashPlugin) ID() string { return "grpccrash" }

func (p *crashPlugin) Register(ctx plugin.PluginContext) error {
	// ping reports the process ID, which is how a test tells a restarted
	// process from the original one.
	ctx.RegisterAPI("ping", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "pong %d", os.Getpid())
	}))
	ctx.RegisterAPI("panic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("handler exploded")
	}))
	// The stderr line is what the host has to capture from a dying process.
	ctx.RegisterAPI("exit", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(os.Stderr, "plugin is about to exit")
		os.Exit(7)
	}))
	return nil
}

func (p *crashPlugin) Start(_ context.Context) error { return nil }
func (p *crashPlugin) Stop() error                   { return nil }

func main() { plugcmd.Run(&crashPlugin{}) }
