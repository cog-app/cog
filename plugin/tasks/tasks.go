package tasks

import (
	"context"
	"database/sql"
	"embed"
	"io"
	"log/slog"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

//go:generate go tool migratesql .

//go:embed sql
var sqlFS embed.FS

const PluginID = "tasks"

// Plugin is the tasks plugin.
type Plugin struct {
	ctx plugin.PluginContext
	log *slog.Logger
}

// New creates a new Tasks plugin instance.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) ID() string { return PluginID }

// DB returns the user's database for this plugin.
//
// The host owns the database: where it lives, how it is opened, and applying
// the schema registered in Register. This is a name for it, not a place that
// does any of that work.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	return p.ctx.PluginDB(username)
}

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx
	ctx.RegisterSchema(sqlFS)
	p.log = ctx.Logger()

	ctx.RegisterNavItem(plugin.NavItem{Label: "Tasks", URL: "/apps/tasks", Icon: `<svg class="nav-icon" viewBox="0 0 24 24"><path d="M9 11l3 3L22 4"></path><path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11"></path></svg>`})
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
