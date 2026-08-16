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
// A trigger runs inside the writer's transaction, so the change commits with
// the write that caused it: a crash between the two cannot lose it, and a
// rolled-back write leaves nothing queued. It is also visible to any process
// that reads the queue, not only to the one holding the connection — which is
// what connection-level update hooks are limited to.
//
// Safe to call on every open: everything is IF NOT EXISTS.
func EnsureChangeCapture(sdb *sql.DB, tables []string) error {
	if len(tables) == 0 {
		return nil
	}

	// seq is a plain rowid alias, deliberately not AUTOINCREMENT: that keeps a
	// separate sequence table updated on every insert, and this table takes an
	// insert for every row written anywhere in the database. Rowids are only
	// reused after the highest row is deleted, and draining takes the lowest
	// rows first, so the ordering the drain relies on holds — and once the
	// queue empties there is nothing left to order.
	var b strings.Builder
	fmt.Fprintf(&b, `CREATE TABLE IF NOT EXISTS %s (
		seq INTEGER PRIMARY KEY,
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

	return dropStaleTriggers(sdb, tables)
}

// dropStaleTriggers removes capture triggers for tables outside the given set.
//
// Capture costs a write per row, so a table nobody indexes should not have a
// trigger: the changes it queued would be drained and discarded.
func dropStaleTriggers(sdb *sql.DB, tables []string) error {
	wanted := make(map[string]struct{}, len(tables)*3)
	for _, table := range tables {
		for _, suffix := range []string{"ai", "au", "ad"} {
			wanted[fmt.Sprintf("_cog_chg_%s_%s", table, suffix)] = struct{}{}
		}
	}

	// GLOB rather than LIKE: in LIKE, the leading underscore is a wildcard.
	rows, err := sdb.Query(`SELECT name FROM sqlite_master WHERE type = 'trigger' AND name GLOB '_cog_chg_*'`)
	if err != nil {
		return fmt.Errorf("change capture: list triggers: %w", err)
	}

	var stale []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("change capture: scan trigger: %w", err)
		}
		if _, ok := wanted[name]; !ok {
			stale = append(stale, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("change capture: list triggers: %w", err)
	}

	for _, name := range stale {
		if !safeIdent(name) {
			continue
		}
		if _, err := sdb.Exec("DROP TRIGGER IF EXISTS " + name); err != nil {
			return fmt.Errorf("change capture: drop %s: %w", name, err)
		}
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
