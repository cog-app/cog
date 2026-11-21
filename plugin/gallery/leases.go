package gallery

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"
)

// leaseDuration is how long a photo stays with whoever claimed it. A remote
// worker takes a few at a time and finishes them in well under this; one
// that goes away mid-batch gives them back when it runs out.
const leaseDuration = 10 * time.Minute

const localHolder = "local"

// unleased is the condition for a photo nobody holds a live lease on; its
// one parameter is the time now, in Unix seconds.
const unleased = `NOT EXISTS (SELECT 1 FROM analysis_leases l WHERE l.media_id = m.id AND l.expires > ?)`

// claimLease takes the lease on a photo for holder unless someone else holds
// a live one, and returns the token that proves it.
func (p *Plugin) claimLease(db *sql.DB, mediaID int64, holder string) (token string, ok bool, err error) {
	token = rand.Text()
	now := p.ctx.Now()
	res, err := db.Exec(`INSERT INTO analysis_leases (media_id, holder, token, expires) VALUES (?, ?, ?, ?)
		ON CONFLICT (media_id) DO UPDATE SET holder = excluded.holder, token = excluded.token, expires = excluded.expires
		WHERE analysis_leases.expires <= ?`,
		mediaID, holder, token, now.Add(leaseDuration).Unix(), now.Unix())
	if err != nil {
		return "", false, fmt.Errorf("lease media %d: %w", mediaID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", false, fmt.Errorf("lease media %d: %w", mediaID, err)
	}
	return token, n == 1, nil
}

// holdsLease reports whether token is still the lease on a photo. A lease
// that ran out but that nobody has taken since still counts: the work done
// under it is as good as anyone's.
func holdsLease(db *sql.DB, mediaID int64, token string) (bool, error) {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM analysis_leases WHERE media_id = ? AND token = ?`, mediaID, token).Scan(&n); err != nil {
		return false, fmt.Errorf("check lease on media %d: %w", mediaID, err)
	}
	return n == 1, nil
}

func releaseLease(db *sql.DB, mediaID int64, token string) error {
	if _, err := db.Exec(`DELETE FROM analysis_leases WHERE media_id = ? AND token = ?`, mediaID, token); err != nil {
		return fmt.Errorf("release lease on media %d: %w", mediaID, err)
	}
	return nil
}
