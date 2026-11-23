package gallery

import (
	"database/sql"
	"errors"
	"fmt"
)

// errDedupeRunning refuses a second deletion of identical copies while one
// runs.
var errDedupeRunning = errors.New("identical copies are already being deleted")

// identicalCopy is a file to delete because another, kept, has its bytes.
type identicalCopy struct {
	KeepPath   string
	CopyPath   string
	CopyFileID int64
	Size       int64
}

// IdenticalSummary is what deleting every identical copy would remove.
type IdenticalSummary struct {
	Copies int
	Bytes  int64
}

func identicalCopies(db, filesDB *sql.DB) ([]identicalCopy, error) {
	rows, err := db.Query(`SELECT km.file_path, cm.file_path, cm.file_id
		FROM duplicate_groups g
		JOIN duplicate_members k ON k.group_id = g.id AND k.is_keep = 1
		JOIN media km ON km.id = k.media_id
		JOIN duplicate_members c ON c.group_id = g.id AND c.is_keep = 0
		JOIN media cm ON cm.id = c.media_id
		WHERE g.kind = ? AND km.shared_from = '' AND cm.shared_from = ''
		ORDER BY g.id, cm.id`, DuplicateIdentical)
	if err != nil {
		return nil, fmt.Errorf("list identical copies: %w", err)
	}
	defer rows.Close()
	var out []identicalCopy
	for rows.Next() {
		var c identicalCopy
		if err := rows.Scan(&c.KeepPath, &c.CopyPath, &c.CopyFileID); err != nil {
			return nil, fmt.Errorf("list identical copies: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list identical copies: %w", err)
	}
	if filesDB == nil || len(out) == 0 {
		return out, nil
	}
	sizes, err := fileSizes(filesDB)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Size = sizes[out[i].CopyFileID]
	}
	return out, nil
}

// SummarizeIdentical counts the identical copies and the space they take.
func SummarizeIdentical(db, filesDB *sql.DB) (IdenticalSummary, error) {
	copies, err := identicalCopies(db, filesDB)
	if err != nil {
		return IdenticalSummary{}, err
	}
	s := IdenticalSummary{Copies: len(copies)}
	for _, c := range copies {
		s.Bytes += c.Size
	}
	return s, nil
}

// DedupeStatus is the user's deletion of identical copies running, or the
// last one since the server started.
func (p *Plugin) DedupeStatus(username string) (FileJob, bool) {
	return p.dedupes.status(username)
}

// StartDedupe moves every identical copy to the trash in the background,
// keeping the file each group keeps.
func (p *Plugin) StartDedupe(username string) error {
	return p.dedupes.start(username, false, func(j *fileJob) error {
		db, err := p.DB(username)
		if err != nil {
			return err
		}
		copies, err := identicalCopies(db, nil)
		if err != nil {
			return err
		}
		if len(copies) == 0 {
			return errors.New("there are no identical copies to delete")
		}
		j.begin(len(copies))
		return p.deleteIdenticalCopies(db, username, copies, j)
	})
}

// deleteIdenticalCopies deletes each copy after hashing it and its kept
// file again: the groups are from the last scan, and either file may have
// changed since.
func (p *Plugin) deleteIdenticalCopies(db *sql.DB, username string, copies []identicalCopy, j *fileJob) error {
	fp, err := p.filesPlugin()
	if err != nil {
		return err
	}
	kept := map[string]string{}
	for _, c := range copies {
		keepSum, ok := kept[c.KeepPath]
		if !ok {
			if keepSum, err = p.hashMediaFile(username, c.KeepPath); err != nil {
				j.fail(fmt.Errorf("%s: kept file %s unreadable: %w", c.CopyPath, c.KeepPath, err))
				continue
			}
			kept[c.KeepPath] = keepSum
		}
		sum, err := p.hashMediaFile(username, c.CopyPath)
		if err != nil {
			j.fail(fmt.Errorf("%s: %w", c.CopyPath, err))
			continue
		}
		if sum != keepSum {
			j.fail(fmt.Errorf("%s: no longer identical to %s", c.CopyPath, c.KeepPath))
			continue
		}
		if err := fp.Delete(username, c.CopyPath); err != nil {
			j.fail(fmt.Errorf("%s: %w", c.CopyPath, err))
			continue
		}
		if err := DeleteMediaByFileID(db, c.CopyFileID); err != nil {
			return fmt.Errorf("forget %s: %w", c.CopyPath, err)
		}
		j.step()
	}
	return PruneDuplicateGroups(db)
}
