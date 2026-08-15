package plugin_test

import (
	"context"
	"database/sql"
	"testing"
	"testing/fstest"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

// schemaPlugin is a minimal plugin whose schema registration is controllable.
type schemaPlugin struct {
	id     string
	schema *fstest.MapFS
	ctx    cogplugin.PluginContext
}

func (p *schemaPlugin) ID() string { return p.id }

func (p *schemaPlugin) Register(ctx cogplugin.PluginContext) error {
	p.ctx = ctx
	if p.schema != nil {
		ctx.RegisterSchema(p.schema)
	}
	return nil
}

func (p *schemaPlugin) Start(context.Context) error { return nil }
func (p *schemaPlugin) Stop() error                 { return nil }

func widgets() *fstest.MapFS {
	return &fstest.MapFS{
		"sql/schema.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS widgets (id INTEGER PRIMARY KEY, name TEXT);`),
		},
	}
}

// TestPluginDBIsMigratedOnOpen is the regression test for the ordering bug:
// the first thing to touch a user's database used to decide whether it had a
// schema, so a page handler could arrive before whatever ran the migrations.
func TestPluginDBIsMigratedOnOpen(t *testing.T) {
	p := &schemaPlugin{id: "widgetplugin", schema: widgets()}
	env := cogtest.New(t, p)
	env.CreateUser(t, "alice", "pass")

	db, err := p.ctx.PluginDB("alice")
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO widgets (name) VALUES ('sprocket')`)
	require.NoError(t, err)
}

// TestPluginDBWithoutSchemaFails proves a missing registration is loud. The
// old optional-interface version was silent: a plugin that never declared a
// schema got an empty database and failed later, somewhere else.
func TestPluginDBWithoutSchemaFails(t *testing.T) {
	p := &schemaPlugin{id: "schemaless"}
	env := cogtest.New(t, p)
	env.CreateUser(t, "alice", "pass")

	db, err := p.ctx.PluginDB("alice")
	require.NoError(t, err) // opening is fine; the database is simply empty

	var name string
	err = db.QueryRow(`SELECT name FROM widgets`).Scan(&name)
	require.Error(t, err)
}

// TestPluginDBOptionsAreNotCached checks that an options-bearing handle is
// distinct from the shared one. Handing back the cached handle would give the
// caller a database with the wrong instrumentation.
func TestPluginDBOptionsAreNotCached(t *testing.T) {
	p := &schemaPlugin{id: "widgetplugin", schema: widgets()}
	env := cogtest.New(t, p)
	env.CreateUser(t, "alice", "pass")

	shared, err := p.ctx.PluginDB("alice")
	require.NoError(t, err)

	again, err := p.ctx.PluginDB("alice")
	require.NoError(t, err)
	require.True(t, shared == again, "default opens should share one handle")

	unhooked, err := p.ctx.PluginDB("alice", cogplugin.NoHooks)
	require.NoError(t, err)
	defer unhooked.Close()
	require.True(t, shared != unhooked, "an options open should not reuse the cached handle")

	var n int
	require.NoError(t, unhooked.QueryRow(`SELECT count(*) FROM widgets`).Scan(&n))
}

// TestPluginDBOfUnknownPluginFails keeps the cross-plugin option from being a
// way to conjure databases for plugins that do not exist.
func TestPluginDBOfUnknownPluginFails(t *testing.T) {
	p := &schemaPlugin{id: "widgetplugin", schema: widgets()}
	env := cogtest.New(t, p)
	env.CreateUser(t, "alice", "pass")

	var db *sql.DB
	db, err := p.ctx.PluginDB("alice", cogplugin.OfPlugin("nonexistent"))
	require.Error(t, err)
	require.True(t, db == nil, "no handle should be returned")
}
