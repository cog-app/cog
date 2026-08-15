package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ChangeTable is the per-database queue of row changes awaiting indexing.
const ChangeTable = "_cog_row_changes"

// Row change operations. These match search.Op but are kept separate so this
// package does not depend on the indexer.
const (
	ChangeInsert = 1
	ChangeUpdate = 2
	ChangeDelete = 3
)

// RowChange identifies a row that changed. Only identity is recorded: the
// indexer reads the current contents back when it processes the change, so
// there is nothing here to keep in step with the table's columns.
type RowChange struct {
	Table string
	RowID int64
	Op    int
}

// EnsureChangeCapture creates the change queue and the triggers that fill it.
//
// Triggers replace connection-level update hooks, which only exist when the
// database is a local file opened by the process doing the indexing. A trigger
// runs inside the writer's transaction instead, so the change is committed
// with the write that caused it — it cannot be lost by a crash, and it is
// visible to whichever process gets there first rather than only to the one
// that happened to hold the connection.
//
// Safe to call on every open: everything is IF NOT EXISTS.
func EnsureChangeCapture(sdb *sql.DB, tables []string) error {
	if len(tables) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, `CREATE TABLE IF NOT EXISTS %s (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		tbl TEXT NOT NULL,
		rid INTEGER NOT NULL,
		op  INTEGER NOT NULL
	);`, ChangeTable)

	for _, table := range tables {
		if !safeIdent(table) {
			return fmt.Errorf("change capture: unsupported table name %q", table)
		}
		for _, t := range []struct {
			suffix string
			event  string
			row    string
			op     int
		}{
			{"ai", "INSERT", "NEW", ChangeInsert},
			{"au", "UPDATE", "NEW", ChangeUpdate},
			{"ad", "DELETE", "OLD", ChangeDelete},
		} {
			fmt.Fprintf(&b, `
			CREATE TRIGGER IF NOT EXISTS _cog_chg_%[1]s_%[2]s AFTER %[3]s ON %[1]s
			BEGIN
				INSERT INTO %[4]s (tbl, rid, op) VALUES ('%[1]s', %[5]s.rowid, %[6]d);
			END;`, table, t.suffix, t.event, ChangeTable, t.row, t.op)
		}
	}

	if _, err := sdb.Exec(b.String()); err != nil {
		return fmt.Errorf("change capture: %w", err)
	}

	return nil
}

// DrainChanges claims up to limit queued changes and removes them.
//
// The delete and the read are one statement, so two processes draining the
// same database cannot claim the same change. A claimed change that is then
// lost — the caller crashing before it indexes — is not recovered here; the
// periodic full sync is what covers that.
func DrainChanges(ctx context.Context, sdb *sql.DB, limit int) ([]RowChange, error) {
	query := fmt.Sprintf(`DELETE FROM %[1]s WHERE seq IN (
		SELECT seq FROM %[1]s ORDER BY seq LIMIT %[2]d
	) RETURNING tbl, rid, op`, ChangeTable, limit)

	rows, err := sdb.QueryContext(ctx, query)
	if err != nil {
		// No queue yet means nothing has been captured for this database.
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("drain changes: %w", err)
	}
	defer rows.Close()

	// Read everything before returning: the rows are deleted as they are
	// stepped, so abandoning the cursor early would leave the tail claimed by
	// nobody and deleted by nobody.
	var out []RowChange
	for rows.Next() {
		var c RowChange
		if err := rows.Scan(&c.Table, &c.RowID, &c.Op); err != nil {
			return nil, fmt.Errorf("drain changes: scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("drain changes: %w", err)
	}

	return out, nil
}

// safeIdent reports whether an identifier can be interpolated into DDL.
// Table names come from plugin registrations rather than user input, but
// they are still interpolated, so they are checked rather than trusted.
func safeIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}

	return true
}
