package tasks

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"

	internalplugin "git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/plugin"
)

//go:generate go tool templ generate -log-level warn
//go:generate go tool migratesql .

//go:embed sql/schema.sql
var sqlFS embed.FS

const PluginID = "tasks"

// Plugin is the tasks plugin.
type Plugin struct {
	internalplugin.Protected
	ctx plugin.PluginContext
}

// New creates a new Tasks plugin instance.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx

	ctx.RegisterNavItem(plugin.NavItem{Label: "Tasks", URL: "/apps/tasks", Icon: "tasks"})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Upcoming Tasks",
		Priority: 20,
		Render:   p.renderWidget,
	})
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "tasks",
		EnrichSQL:    "SELECT rowid, summary, description FROM tasks WHERE rowid IN (?)",
	})
	ctx.RegisterResource(plugin.ResourceDef{
		Type:     "list",
		Table:    "lists",
		IDColumn: "uid",
		PermissionLevels: []plugin.PermissionLevel{
			{Label: "Read-only", Permissions: plugin.PermRead},
			{Label: "Read/Write", Permissions: plugin.PermRead | plugin.PermWrite},
		},
	})

	// Web UI routes
	pages := NewPages(p)
	ctx.RegisterPage("{$}", pages.wrap(pages.Index))
	ctx.RegisterPage("list/{id}", pages.wrap(pages.ListView))
	ctx.RegisterPage("list/{id}/settings", pages.wrap(pages.ListSettings))
	ctx.RegisterPage("task/{id}", pages.wrap(pages.TaskDetail))
	ctx.RegisterAction("POST", "task", pages.wrap(pages.CreateTask))
	ctx.RegisterAction("POST", "task/{id}", pages.wrap(pages.UpdateTask))
	ctx.RegisterAction("POST", "task/{id}/toggle", pages.wrap(pages.ToggleTask))
	ctx.RegisterAction("DELETE", "task/{id}", pages.wrap(pages.DeleteTask))
	ctx.RegisterAction("POST", "list", pages.wrap(pages.CreateList))
	ctx.RegisterAction("POST", "list/{id}", pages.wrap(pages.UpdateList))
	ctx.RegisterAction("DELETE", "list/{id}", pages.wrap(pages.DeleteList))
	ctx.RegisterAction("POST", "reorder", pages.wrap(pages.Reorder))

	// TODO: CalDAV VTODO handler — not mounted yet.
	// Integration with the calendar plugin's calendar-home-set is a follow-up.
	// caldav := NewCalDAVHandler(p)

	// TODO: calendar:get_events hook for showing tasks with due dates on the calendar.
	// ctx.RegisterHook("calendar:get_events", p.calendarHook)

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return nil
	}
	tasks, _ := UpcomingTasks(db, 5)
	return upcomingTasksWidget(tasks).Render(ctx, w)
}

// DB returns the tasks plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.ctx.PluginDB(username)
	if err != nil {
		return nil, err
	}
	if err := plugin.Migrate(db, sqlFS); err != nil {
		return nil, fmt.Errorf("migrate tasks db for %s: %w", username, err)
	}
	return db, nil
}
