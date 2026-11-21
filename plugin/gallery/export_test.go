package gallery

import (
	"context"
	"database/sql"
	"time"
)

func (p *Plugin) OwnersFirst(usernames []string) ([]string, map[string][]string) {
	return p.ownersFirst(usernames)
}

func (p *Plugin) PreGenerateThumbnails(ctx context.Context, db *sql.DB, username string, deadline time.Time) (bool, error) {
	return p.preGenerateThumbnails(ctx, db, username, deadline)
}
