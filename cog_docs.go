package cog

import (
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	cogdocs "git.duckfam.us/jj-software/cog/DOCS"
	"git.duckfam.us/jj-software/cog/internal/docs"
	"git.duckfam.us/jj-software/cog/internal/version"
	"git.duckfam.us/jj-software/cog/ui"
)

// docsCmd builds the website: the docs every Cog serves at /docs/, as
// static pages with a search that runs in the browser.
func docsCmd() *cobra.Command {
	var out, serve string
	c := &cobra.Command{
		Use:   "docs",
		Short: "Build the documentation website",
		Long: "Build the documentation website as static files: the docs Cog serves at /docs/,\n" +
			"as they read outside a running Cog.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			d, err := docs.Load(cogdocs.FS, docs.Data{Site: true, Version: version.Get()})
			if err != nil {
				return err
			}
			static, err := fs.Sub(ui.StaticFS, "static")
			if err != nil {
				return fmt.Errorf("docs: %w", err)
			}
			if err := d.Write(out, static); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Built %d pages in %s\n", len(d.Pages), out)
			if serve == "" {
				return nil
			}
			fmt.Fprintf(c.OutOrStdout(), "Serving at http://%s/\n", serve)
			srv := &http.Server{Addr: serve, Handler: http.FileServer(http.Dir(out)), ReadHeaderTimeout: 10 * time.Second}
			return srv.ListenAndServe()
		},
	}
	c.Flags().StringVar(&out, "out", "public", "the folder to build the website in (replaced)")
	c.Flags().StringVar(&serve, "serve", "", "then serve it at this address, such as localhost:8080")
	return c
}
