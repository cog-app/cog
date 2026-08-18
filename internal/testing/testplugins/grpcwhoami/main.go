// Command cog-plugin-grpcwhoami is a process plugin that reports what its
// request context carried.
package main

import (
	"context"
	"fmt"
	"net/http"

	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/plugcmd"
)

type identityPlugin struct{}

func (p *identityPlugin) ID() string { return "grpcwhoami" }

func (p *identityPlugin) Register(ctx plugin.PluginContext) error {
	ctx.RegisterAPI("whoami", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := plugin.UserFromContext(r.Context())
		if u == nil {
			fmt.Fprint(w, "no user")
			return
		}
		fmt.Fprintf(w, "id=%s username=%s display=%s admin=%t helper=%s",
			u.ID, u.Username, u.DisplayName, u.IsAdmin, plugin.Username(r))
	}))
	ctx.RegisterAPI("db", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		db, err := plugin.UserDB(r)
		if err != nil {
			fmt.Fprintf(w, "err=%v", err)
			return
		}
		var n int
		if err := db.QueryRow("SELECT 1").Scan(&n); err != nil {
			fmt.Fprintf(w, "query err=%v", err)
			return
		}
		fmt.Fprintf(w, "ok=%d", n)
	}))
	return nil
}

func (p *identityPlugin) Start(_ context.Context) error { return nil }
func (p *identityPlugin) Stop() error                   { return nil }

func main() { plugcmd.Run(&identityPlugin{}) }
