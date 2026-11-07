package files

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// MaxCommentLength bounds a comment, which is read whole into every listing
// of its file's comments.
const MaxCommentLength = 16 << 10

// ErrCommentTooLong is a comment over MaxCommentLength.
var ErrCommentTooLong = errors.New("the comment is too long")

// Comment represents a user comment on a file.
type Comment struct {
	ID        int64  `json:"id"`
	FileID    int64  `json:"file_id"`
	ActorID   string `json:"actor_id"`
	Message   string `json:"message"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	Verb      string `json:"verb"`
}

// ListComments returns all comments for a file.
func (p *Plugin) ListComments(username string, fileID int64) ([]Comment, error) {
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT id, file_id, actor_id, message, created_at, updated_at, verb
		FROM comments WHERE file_id = ?
		ORDER BY created_at ASC`, fileID)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	defer rows.Close()

	var comments []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.FileID, &c.ActorID, &c.Message,
			&c.CreatedAt, &c.UpdatedAt, &c.Verb); err != nil {
			return nil, fmt.Errorf("scan comment: %w", err)
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// CreateComment adds a comment to a file.
func (p *Plugin) CreateComment(username string, fileID int64, actorID, message string) (*Comment, error) {
	if len(message) > MaxCommentLength {
		return nil, ErrCommentTooLong
	}
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	now := p.ctx.Now().Unix()
	res, err := db.Exec(`
		INSERT INTO comments (file_id, actor_id, message, created_at, updated_at, verb)
		VALUES (?, ?, ?, ?, ?, 'comment')`,
		fileID, actorID, message, now, now)
	if err != nil {
		return nil, fmt.Errorf("create comment: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create comment id: %w", err)
	}
	return &Comment{
		ID:        id,
		FileID:    fileID,
		ActorID:   actorID,
		Message:   message,
		CreatedAt: now,
		UpdatedAt: now,
		Verb:      "comment",
	}, nil
}

// UpdateComment modifies a comment's message.
func (p *Plugin) UpdateComment(username string, commentID int64, actorID, message string) error {
	db, err := p.DB(username)
	if err != nil {
		return err
	}

	now := p.ctx.Now().Unix()
	res, err := db.Exec(`
		UPDATE comments SET message = ?, updated_at = ?
		WHERE id = ? AND actor_id = ?`,
		message, now, commentID, actorID)
	if err != nil {
		return fmt.Errorf("update comment: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update comment rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("comment %d not found or not owned: %w", commentID, ErrNotFound)
	}
	return nil
}

// DeleteComment removes a comment. A comment that doesn't exist, or that
// actorID didn't write, is ErrNotFound.
func (p *Plugin) DeleteComment(username string, commentID int64, actorID string) error {
	db, err := p.DB(username)
	if err != nil {
		return err
	}

	res, err := db.Exec(`DELETE FROM comments WHERE id = ? AND actor_id = ?`,
		commentID, actorID)
	if err != nil {
		return fmt.Errorf("delete comment: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete comment rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("comment %d not found or not owned: %w", commentID, ErrNotFound)
	}
	return nil
}

// CommentCount returns the number of comments for a file.
func CommentCount(db *sql.DB, fileID int64) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM comments WHERE file_id = ?`, fileID).Scan(&count)
	return count, err
}

// CommentCounts returns comment counts for multiple file IDs in a single query.
func CommentCounts(db *sql.DB, fileIDs []int64) (map[int64]int, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	counts := make(map[int64]int)

	// Build placeholder string
	placeholders := make([]string, len(fileIDs))
	args := make([]any, len(fileIDs))
	for i, id := range fileIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	rows, err := db.Query(
		`SELECT file_id, COUNT(*) FROM comments WHERE file_id IN (`+strings.Join(placeholders, ",")+`) GROUP BY file_id`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("comment counts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fileID int64
		var count int
		if err := rows.Scan(&fileID, &count); err != nil {
			return nil, fmt.Errorf("scan count: %w", err)
		}
		counts[fileID] = count
	}
	return counts, rows.Err()
}
