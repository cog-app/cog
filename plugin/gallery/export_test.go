package gallery

import (
	"context"
	"database/sql"
	"time"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

func (p *Plugin) OwnersFirst(usernames []string) ([]string, map[string][]string) {
	return p.ownersFirst(usernames)
}

func (p *Plugin) PreGenerateThumbnails(ctx context.Context, db *sql.DB, username string, deadline time.Time) (bool, error) {
	return p.preGenerateThumbnails(ctx, db, username, deadline)
}

func (p *Plugin) RunRemoteWorker(ctx context.Context, server string) error {
	return p.runRemoteWorker(ctx, server)
}

func (p *Plugin) UserCacheForTest(username string) (cogplugin.Storage, error) {
	return p.ctx.UserCache(username)
}
