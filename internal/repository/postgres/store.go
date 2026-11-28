// Package postgres implements durable meeting storage with pgxpool.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/true18/meeting-notes/internal/domain"
)

// Store persists application data in PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// Open creates and verifies a PostgreSQL connection pool.
func Open(ctx context.Context, dsn string, timeout time.Duration) (*Store, error) {
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pool, err := pgxpool.New(connectCtx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return &Store{pool: pool}, nil
}

// New uses an existing pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the pool to migration wiring.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close closes the underlying pool.
func (s *Store) Close() { s.pool.Close() }

// EnsureUser creates or returns a user by external identifier.
func (s *Store) EnsureUser(ctx context.Context, externalID string) (domain.User, error) {
	var user domain.User
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users(external_id) VALUES ($1)
		ON CONFLICT (external_id) DO UPDATE SET external_id = EXCLUDED.external_id
		RETURNING id, external_id, created_at`, externalID).Scan(&user.ID, &user.ExternalID, &user.CreatedAt)
	if err != nil {
		return domain.User{}, fmt.Errorf("ensure user: %w", err)
	}
	return user, nil
}

// CreateMeetingWithTask atomically creates a meeting and its processing task.
func (s *Store) CreateMeetingWithTask(ctx context.Context, userID int64, title, path, format string) (domain.MeetingView, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.MeetingView{}, fmt.Errorf("begin meeting transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var result domain.MeetingView
	err = tx.QueryRow(ctx, `
		INSERT INTO meetings(user_id, title, file_path, file_format)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, title, file_path, file_format, created_at`, userID, title, path, format).
		Scan(&result.ID, &result.UserID, &result.Title, &result.FilePath, &result.Format, &result.CreatedAt)
	if err != nil {
		return domain.MeetingView{}, fmt.Errorf("insert meeting: %w", err)
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO processing_tasks(meeting_id, status) VALUES ($1, $2)
		RETURNING id, status, updated_at`, result.ID, domain.StatusCreated).
		Scan(&result.TaskID, &result.Status, &result.UpdatedAt)
	if err != nil {
		return domain.MeetingView{}, fmt.Errorf("insert processing task: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.MeetingView{}, fmt.Errorf("commit meeting transaction: %w", err)
	}
	return result, nil
}

// ListMeetings returns only meetings owned by userID.
func (s *Store) ListMeetings(ctx context.Context, userID int64) ([]domain.MeetingView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.user_id, m.title, m.file_path, m.file_format, m.created_at,
		       t.id, t.status, t.error_message, t.updated_at, COALESCE(s.text, '')
		FROM meetings m
		JOIN processing_tasks t ON t.meeting_id = m.id
		LEFT JOIN summaries s ON s.meeting_id = m.id
		WHERE m.user_id = $1
		ORDER BY m.created_at DESC, m.id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list meetings: %w", err)
	}
	defer rows.Close()
	var meetings []domain.MeetingView
	for rows.Next() {
		var item domain.MeetingView
		if err := rows.Scan(&item.ID, &item.UserID, &item.Title, &item.FilePath, &item.Format, &item.CreatedAt,
			&item.TaskID, &item.Status, &item.ErrorMessage, &item.UpdatedAt, &item.Summary); err != nil {
			return nil, fmt.Errorf("scan meeting: %w", err)
		}
		meetings = append(meetings, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate meetings: %w", err)
	}
	return meetings, nil
}

// GetMeeting returns a user-owned meeting and status.
func (s *Store) GetMeeting(ctx context.Context, userID, meetingID int64) (domain.MeetingView, error) {
	var item domain.MeetingView
	err := s.pool.QueryRow(ctx, `
		SELECT m.id, m.user_id, m.title, m.file_path, m.file_format, m.created_at,
		       t.id, t.status, t.error_message, t.updated_at, COALESCE(s.text, '')
		FROM meetings m
		JOIN processing_tasks t ON t.meeting_id = m.id
		LEFT JOIN summaries s ON s.meeting_id = m.id
		WHERE m.id = $1 AND m.user_id = $2`, meetingID, userID).
		Scan(&item.ID, &item.UserID, &item.Title, &item.FilePath, &item.Format, &item.CreatedAt,
			&item.TaskID, &item.Status, &item.ErrorMessage, &item.UpdatedAt, &item.Summary)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.MeetingView{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.MeetingView{}, fmt.Errorf("get meeting: %w", err)
	}
	return item, nil
}

// GetTranscript returns a transcript only through an ownership-filtered query.
func (s *Store) GetTranscript(ctx context.Context, userID, meetingID int64) (string, error) {
	var text sql.NullString
	err := s.pool.QueryRow(ctx, `
		SELECT tr.text
		FROM meetings m
		LEFT JOIN transcripts tr ON tr.meeting_id = m.id
		WHERE m.id = $1 AND m.user_id = $2`, meetingID, userID).Scan(&text)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get transcript: %w", err)
	}
	if !text.Valid {
		return "", domain.ErrNotReady
	}
	return text.String, nil
}

// GetSummary returns a summary only through an ownership-filtered query.
func (s *Store) GetSummary(ctx context.Context, userID, meetingID int64) (string, error) {
	var text sql.NullString
	err := s.pool.QueryRow(ctx, `
		SELECT s.text
		FROM meetings m
		LEFT JOIN summaries s ON s.meeting_id = m.id
		WHERE m.id = $1 AND m.user_id = $2`, meetingID, userID).Scan(&text)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get summary: %w", err)
	}
	if !text.Valid {
		return "", domain.ErrNotReady
	}
	return text.String, nil
}

// Search finds matching user-owned transcripts and summaries using PostgreSQL full-text search.
func (s *Store) Search(ctx context.Context, userID int64, query string, limit int) ([]domain.SearchResult, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.title, t.status, m.created_at,
		       ts_headline('russian', tr.text, plainto_tsquery('russian', $2), 'MaxFragments=2,MinWords=4,MaxWords=24')
		FROM meetings m
		JOIN processing_tasks t ON t.meeting_id = m.id
		JOIN transcripts tr ON tr.meeting_id = m.id
		LEFT JOIN summaries s ON s.meeting_id = m.id
		WHERE m.user_id = $1
		  AND (to_tsvector('russian', tr.text) @@ plainto_tsquery('russian', $2)
		       OR to_tsvector('russian', COALESCE(s.text, '')) @@ plainto_tsquery('russian', $2))
		ORDER BY m.created_at DESC, m.id DESC
		LIMIT $3`, userID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search meetings: %w", err)
	}
	defer rows.Close()
	var results []domain.SearchResult
	for rows.Next() {
		var result domain.SearchResult
		if err := rows.Scan(&result.MeetingID, &result.Title, &result.Status, &result.CreatedAt, &result.Snippet); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search results: %w", err)
	}
	return results, nil
}

// ChatContext returns bounded transcript context owned by a user.
func (s *Store) ChatContext(ctx context.Context, userID int64, meetingID *int64, limit int) ([]string, error) {
	query := `
		SELECT LEFT(tr.text, 4000)
		FROM meetings m
		JOIN transcripts tr ON tr.meeting_id = m.id
		WHERE m.user_id = $1`
	args := []any{userID}
	if meetingID != nil {
		query += ` AND m.id = $2`
		args = append(args, *meetingID)
	}
	query += ` ORDER BY m.created_at DESC, m.id DESC LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load chat context: %w", err)
	}
	defer rows.Close()
	var contextParts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, fmt.Errorf("scan chat context: %w", err)
		}
		contextParts = append(contextParts, text)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat context: %w", err)
	}
	if meetingID != nil && len(contextParts) == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM meetings WHERE id=$1 AND user_id=$2)`, *meetingID, userID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check meeting ownership: %w", err)
		}
		if !exists {
			return nil, domain.ErrNotFound
		}
		return nil, domain.ErrNotReady
	}
	return contextParts, nil
}

// ClaimTask atomically claims one created task across concurrent workers and processes.
func (s *Store) ClaimTask(ctx context.Context, lease time.Duration) (domain.ClaimedTask, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.ClaimedTask{}, false, fmt.Errorf("begin claim task: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var task domain.ClaimedTask
	err = tx.QueryRow(ctx, `
		SELECT t.id, t.attempt + 1, m.id, m.user_id, m.file_path, m.file_format, m.title
		FROM processing_tasks t
		JOIN meetings m ON m.id = t.meeting_id
		WHERE t.status = $1
		ORDER BY t.created_at, t.id
		FOR UPDATE OF t SKIP LOCKED
		LIMIT 1`, domain.StatusCreated).
		Scan(&task.TaskID, &task.Attempt, &task.MeetingID, &task.UserID, &task.FilePath, &task.FileFormat, &task.MeetingName)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ClaimedTask{}, false, nil
	}
	if err != nil {
		return domain.ClaimedTask{}, false, fmt.Errorf("select task: %w", err)
	}
	result, err := tx.Exec(ctx, `
		UPDATE processing_tasks
		SET status=$1, attempt=$4, error_message='', started_at=NOW(), lease_expires_at=NOW()+($5 * INTERVAL '1 second'), finished_at=NULL, updated_at=NOW()
		WHERE id=$2 AND status=$3`, domain.StatusProcessing, task.TaskID, domain.StatusCreated, task.Attempt, lease.Seconds())
	if err != nil || result.RowsAffected() != 1 {
		if err == nil {
			err = errors.New("task changed while claiming")
		}
		return domain.ClaimedTask{}, false, fmt.Errorf("mark task processing: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ClaimedTask{}, false, fmt.Errorf("commit claimed task: %w", err)
	}
	return task, true, nil
}

// SaveTranscript atomically stores text and advances a processing task.
func (s *Store) SaveTranscript(ctx context.Context, taskID, attempt, meetingID int64, text string) error {
	return s.withTaskTransaction(ctx, taskID, attempt, domain.StatusProcessing, domain.StatusTranscribed, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO transcripts(meeting_id, text) VALUES ($1, $2)
			ON CONFLICT (meeting_id) DO UPDATE SET text=EXCLUDED.text, saved_at=NOW()`, meetingID, text)
		return err
	})
}

// CompleteTask atomically stores a summary and marks the task completed.
func (s *Store) CompleteTask(ctx context.Context, taskID, attempt, meetingID int64, summary string) error {
	return s.withTaskTransaction(ctx, taskID, attempt, domain.StatusTranscribed, domain.StatusCompleted, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO summaries(meeting_id, text) VALUES ($1, $2)
			ON CONFLICT (meeting_id) DO UPDATE SET text=EXCLUDED.text, saved_at=NOW()`, meetingID, summary)
		return err
	})
}

func (s *Store) withTaskTransaction(ctx context.Context, taskID, attempt int64, from, to domain.TaskStatus, save func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin result transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := save(tx); err != nil {
		return fmt.Errorf("save task result: %w", err)
	}
	result, err := tx.Exec(ctx, `
		UPDATE processing_tasks
		SET status=$1, updated_at=NOW(), finished_at=CASE WHEN $1='completed' THEN NOW() ELSE finished_at END
		, lease_expires_at=CASE WHEN $1='completed' THEN NULL ELSE lease_expires_at END
		WHERE id=$2 AND status=$3 AND attempt=$4`, to, taskID, from, attempt)
	if err != nil {
		return fmt.Errorf("advance task status: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("advance task status: unexpected current state")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit result transaction: %w", err)
	}
	return nil
}

// FailTask saves a processing error unless the task is already completed.
func (s *Store) FailTask(ctx context.Context, taskID, attempt int64, message string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE processing_tasks
		SET status=$1, error_message=$2, updated_at=NOW(), finished_at=NOW(), lease_expires_at=NULL
		WHERE id=$3 AND status IN ($4, $5) AND attempt=$6`, domain.StatusFailed, message, taskID, domain.StatusProcessing, domain.StatusTranscribed, attempt)
	if err != nil {
		return fmt.Errorf("fail task: %w", err)
	}
	return nil
}

// RequeueTask returns interrupted work to the durable queue.
func (s *Store) RequeueTask(ctx context.Context, taskID, attempt int64, message string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE processing_tasks
		SET status=$1, error_message=$2, updated_at=NOW(), started_at=NULL, lease_expires_at=NULL, finished_at=NULL
		WHERE id=$3 AND status IN ($4, $5) AND attempt=$6`, domain.StatusCreated, message, taskID, domain.StatusProcessing, domain.StatusTranscribed, attempt)
	if err != nil {
		return fmt.Errorf("requeue task: %w", err)
	}
	return nil
}

// RecoverStaleTasks requeues tasks whose lease predates cutoff.
func (s *Store) RecoverStaleTasks(ctx context.Context) (int64, error) {
	result, err := s.pool.Exec(ctx, `
		UPDATE processing_tasks
		SET status=$1, error_message='recovered after interrupted worker', updated_at=NOW(), started_at=NULL, lease_expires_at=NULL
		WHERE status IN ($2, $3) AND lease_expires_at < NOW()`, domain.StatusCreated, domain.StatusProcessing, domain.StatusTranscribed)
	if err != nil {
		return 0, fmt.Errorf("recover stale tasks: %w", err)
	}
	return result.RowsAffected(), nil
}

// RetryTask requeues a failed task owned by userID.
func (s *Store) RetryTask(ctx context.Context, userID, meetingID int64) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE processing_tasks t
		SET status=$1, updated_at=NOW(), started_at=NULL, finished_at=NULL
		FROM meetings m
		WHERE t.meeting_id=m.id AND m.id=$2 AND m.user_id=$3 AND t.status=$4`,
		domain.StatusCreated, meetingID, userID, domain.StatusFailed)
	if err != nil {
		return fmt.Errorf("retry task: %w", err)
	}
	if result.RowsAffected() != 1 {
		return domain.ErrNotFound
	}
	return nil
}
