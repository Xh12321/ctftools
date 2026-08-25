package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Xh12321/ctftools/internal/platform"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// Common sentinel errors.
var (
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("conflict")
	ErrInvalidInput  = errors.New("invalid input")
	ErrInvalidStatus = errors.New("invalid status transition")
)

// Store is the SQLite-backed persistence layer for tasks, events and usage.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at path and runs migrations.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("database path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	dsn := path
	if path != ":memory:" {
		// Enable foreign keys and reasonable busy timeout via URI.
		dsn = "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	} else {
		dsn = "file:memdb?mode=memory&cache=shared"
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single writer keeps sequence assignment simple and safe.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB exposes the underlying *sql.DB for advanced tests.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    parent_task_id TEXT NOT NULL DEFAULT '',
    handoff_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL,
    category TEXT NOT NULL,
    description TEXT NOT NULL,
    prompt TEXT NOT NULL DEFAULT '',
    target TEXT NOT NULL DEFAULT '',
    flag_format TEXT NOT NULL DEFAULT '',
    model_profile TEXT NOT NULL DEFAULT '',
    model_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    image TEXT NOT NULL,
    runtime TEXT NOT NULL DEFAULT '',
    container_id TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_events (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    source TEXT NOT NULL,
    event_type TEXT NOT NULL,
    turn_id TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL DEFAULT '',
    payload BLOB NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(task_id, sequence)
);
CREATE INDEX IF NOT EXISTS idx_task_events_task_sequence
ON task_events(task_id, sequence);
CREATE TABLE IF NOT EXISTS model_usage (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    cached_input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    usage_reported INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    status_code INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_model_usage_task_created
ON model_usage(task_id, created_at);
CREATE INDEX IF NOT EXISTS idx_model_usage_created
ON model_usage(created_at);
CREATE TABLE IF NOT EXISTS app_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}

	// Ensure default settings exist.
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE key = 'max_concurrent_tasks'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		now := platform.FormatTime(platform.NowUTC())
		if _, err := s.db.Exec(
			`INSERT INTO app_settings(key, value, updated_at) VALUES ('max_concurrent_tasks', ?, ?)`,
			"2", now,
		); err != nil {
			return err
		}
	}
	return nil
}

// CreateTask inserts a new root task and emits task.created.
func (s *Store) CreateTask(ctx context.Context, in platform.CreateTask) (platform.Task, error) {
	if err := in.Validate(); err != nil {
		return platform.Task{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	cat, err := platform.ParseCategory(string(in.Category))
	if err != nil {
		return platform.Task{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	now := platform.NowUTC()
	task := platform.Task{
		ID:           platform.NewID(),
		Title:        strings.TrimSpace(in.Title),
		Category:     cat,
		Description:  strings.TrimSpace(in.Description),
		Prompt:       strings.TrimSpace(in.Prompt),
		Target:       strings.TrimSpace(in.Target),
		FlagFormat:   strings.TrimSpace(in.FlagFormat),
		ModelProfile: strings.TrimSpace(in.ModelProfile),
		ModelID:      strings.TrimSpace(in.ModelID),
		Status:       platform.StatusQueued,
		Image:        strings.TrimSpace(in.Image),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if task.Image == "" {
		task.Image = cat.DefaultImage("0.1.0")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return platform.Task{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := insertTaskTx(tx, task); err != nil {
		return platform.Task{}, fmt.Errorf("insert task: %w", err)
	}

	payload := platform.JSONPayload(map[string]any{
		"title":    task.Title,
		"category": task.Category,
		"status":   task.Status,
		"image":    task.Image,
	})
	if _, err := appendEventTx(tx, task.ID, platform.SourceSystem, platform.EventTaskCreated, "", "", payload, now); err != nil {
		return platform.Task{}, err
	}
	if _, err := appendEventTx(tx, task.ID, platform.SourceSystem, platform.EventTaskQueued, "", "", platform.JSONPayload(map[string]any{
		"status": task.Status,
	}), now); err != nil {
		return platform.Task{}, err
	}

	if err := tx.Commit(); err != nil {
		return platform.Task{}, err
	}
	return task, nil
}

func insertTaskTx(tx *sql.Tx, t platform.Task) error {
	_, err := tx.Exec(`
INSERT INTO tasks (
 id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, model_profile, model_id, status, image,
 runtime, container_id, last_error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ParentTaskID, t.HandoffID, t.Title, string(t.Category), t.Description, t.Prompt, t.Target,
		t.FlagFormat, t.ModelProfile, t.ModelID, string(t.Status), t.Image,
		t.Runtime, t.ContainerID, t.LastError,
		platform.FormatTime(t.CreatedAt), platform.FormatTime(t.UpdatedAt),
	)
	return err
}

// GetTask loads a task by id.
func (s *Store) GetTask(ctx context.Context, id string) (platform.Task, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, model_profile, model_id, status, image,
 runtime, container_id, last_error, created_at, updated_at
FROM tasks WHERE id = ?`, id)
	return scanTask(row)
}

// ListTasks returns root tasks ordered by created_at descending.
func (s *Store) ListTasks(ctx context.Context) ([]platform.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, model_profile, model_id, status, image,
 runtime, container_id, last_error, created_at, updated_at
FROM tasks WHERE parent_task_id = '' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// ListSubtasks returns child tasks of a parent.
func (s *Store) ListSubtasks(ctx context.Context, parentID string) ([]platform.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, model_profile, model_id, status, image,
 runtime, container_id, last_error, created_at, updated_at
FROM tasks WHERE parent_task_id = ? ORDER BY created_at ASC`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// ListTasksByStatus returns tasks in a given status ordered for scheduling.
func (s *Store) ListTasksByStatus(ctx context.Context, status platform.TaskStatus) ([]platform.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, model_profile, model_id, status, image,
 runtime, container_id, last_error, created_at, updated_at
FROM tasks WHERE status = ? ORDER BY updated_at ASC, created_at ASC`, string(status))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// UpdateTaskStatus updates lifecycle fields and optionally appends an event.
// The update is rejected when expectedFrom is non-empty and does not match.
func (s *Store) UpdateTaskStatus(ctx context.Context, id string, expectedFrom []platform.TaskStatus, to platform.TaskStatus, runtime, containerID, lastError string, eventType, eventSource string, payload any) (platform.Task, platform.TaskEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return platform.Task{}, platform.TaskEvent{}, err
	}
	defer func() { _ = tx.Rollback() }()

	task, err := getTaskTx(tx, id)
	if err != nil {
		return platform.Task{}, platform.TaskEvent{}, err
	}
	if len(expectedFrom) > 0 {
		ok := false
		for _, st := range expectedFrom {
			if task.Status == st {
				ok = true
				break
			}
		}
		if !ok {
			return platform.Task{}, platform.TaskEvent{}, fmt.Errorf("%w: cannot move from %s to %s", ErrInvalidStatus, task.Status, to)
		}
	}

	now := platform.NowUTC()
	task.Status = to
	if runtime != "" || to == platform.StatusSettled || to == platform.StatusCancelled {
		// Allow clearing by explicit empty when caller passes a sentinel? keep as-is unless provided.
	}
	if runtime != keepField {
		task.Runtime = runtime
	}
	if containerID != keepField {
		task.ContainerID = containerID
	}
	// lastError always applied when provided via this path (may clear).
	task.LastError = lastError
	task.UpdatedAt = now

	if _, err := tx.Exec(`
UPDATE tasks SET status = ?, runtime = ?, container_id = ?, last_error = ?, updated_at = ?
WHERE id = ?`,
		string(task.Status), task.Runtime, task.ContainerID, task.LastError, platform.FormatTime(task.UpdatedAt), task.ID,
	); err != nil {
		return platform.Task{}, platform.TaskEvent{}, err
	}

	var ev platform.TaskEvent
	if eventType != "" {
		src := eventSource
		if src == "" {
			src = platform.SourceSystem
		}
		pl := platform.JSONPayload(payload)
		if payload == nil {
			pl = platform.JSONPayload(map[string]any{
				"status":      task.Status,
				"runtime":     task.Runtime,
				"containerId": task.ContainerID,
				"lastError":   task.LastError,
			})
		}
		ev, err = appendEventTx(tx, task.ID, src, eventType, "", "", pl, now)
		if err != nil {
			return platform.Task{}, platform.TaskEvent{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return platform.Task{}, platform.TaskEvent{}, err
	}
	return task, ev, nil
}

// keepField is a sentinel meaning "do not change this field".
// Empty string is a valid value for runtime/container_id, so callers that want
// to leave fields untouched should pass keepField via the helper methods.
const keepField = "\x00keep"

// Transition is a convenience wrapper that keeps runtime/container unless set.
type StatusUpdate struct {
	To          platform.TaskStatus
	From        []platform.TaskStatus
	Runtime     *string
	ContainerID *string
	LastError   *string
	EventType   string
	EventSource string
	Payload     any
}

// ApplyStatusUpdate applies a structured status change.
func (s *Store) ApplyStatusUpdate(ctx context.Context, id string, u StatusUpdate) (platform.Task, platform.TaskEvent, error) {
	runtime := keepField
	containerID := keepField
	lastError := ""
	clearError := false

	if u.Runtime != nil {
		runtime = *u.Runtime
	}
	if u.ContainerID != nil {
		containerID = *u.ContainerID
	}
	if u.LastError != nil {
		lastError = *u.LastError
		clearError = true
	}

	// Load current to preserve last_error when not specified.
	if !clearError {
		cur, err := s.GetTask(ctx, id)
		if err != nil {
			return platform.Task{}, platform.TaskEvent{}, err
		}
		lastError = cur.LastError
		// Also preserve runtime/container when not set — handled by keepField.
		_ = cur
	}

	return s.UpdateTaskStatus(ctx, id, u.From, u.To, runtime, containerID, lastError, u.EventType, u.EventSource, u.Payload)
}

// UpdatePrompt updates the task prompt when the agent is not running.
func (s *Store) UpdatePrompt(ctx context.Context, id, prompt string) (platform.Task, platform.TaskEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return platform.Task{}, platform.TaskEvent{}, err
	}
	defer func() { _ = tx.Rollback() }()

	task, err := getTaskTx(tx, id)
	if err != nil {
		return platform.Task{}, platform.TaskEvent{}, err
	}
	if task.Status.IsActive() {
		return platform.Task{}, platform.TaskEvent{}, fmt.Errorf("%w: the task prompt cannot be changed while the agent is running", ErrConflict)
	}

	now := platform.NowUTC()
	task.Prompt = prompt
	task.UpdatedAt = now
	if _, err := tx.Exec(`UPDATE tasks SET prompt = ?, updated_at = ? WHERE id = ?`, prompt, platform.FormatTime(now), id); err != nil {
		return platform.Task{}, platform.TaskEvent{}, err
	}
	ev, err := appendEventTx(tx, id, platform.SourceUser, platform.EventTaskPromptUpdated, "", "", platform.JSONPayload(map[string]any{
		"prompt": prompt,
	}), now)
	if err != nil {
		return platform.Task{}, platform.TaskEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return platform.Task{}, platform.TaskEvent{}, err
	}
	return task, ev, nil
}

// AppendEvent appends an event and returns the assigned sequence.
func (s *Store) AppendEvent(ctx context.Context, taskID, source, eventType, turnID, toolCallID string, payload any) (platform.TaskEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return platform.TaskEvent{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// Ensure task exists.
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id = ?`, taskID).Scan(&exists); err != nil {
		return platform.TaskEvent{}, err
	}
	if exists == 0 {
		return platform.TaskEvent{}, fmt.Errorf("%w: task %s", ErrNotFound, taskID)
	}

	ev, err := appendEventTx(tx, taskID, source, eventType, turnID, toolCallID, platform.JSONPayload(payload), platform.NowUTC())
	if err != nil {
		return platform.TaskEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return platform.TaskEvent{}, err
	}
	return ev, nil
}

func appendEventTx(tx *sql.Tx, taskID, source, eventType, turnID, toolCallID string, payload json.RawMessage, at time.Time) (platform.TaskEvent, error) {
	var seq int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(sequence), 0) + 1 FROM task_events WHERE task_id = ?`, taskID).Scan(&seq); err != nil {
		return platform.TaskEvent{}, err
	}
	if payload == nil {
		payload = json.RawMessage(`{}`)
	}
	ev := platform.TaskEvent{
		ID:         platform.NewID(),
		TaskID:     taskID,
		Sequence:   seq,
		Source:     source,
		Type:       eventType,
		TurnID:     turnID,
		ToolCallID: toolCallID,
		Payload:    payload,
		CreatedAt:  at,
	}
	if _, err := tx.Exec(`
INSERT INTO task_events (
 id, task_id, sequence, source, event_type, turn_id, tool_call_id, payload, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.TaskID, ev.Sequence, ev.Source, ev.Type, ev.TurnID, ev.ToolCallID, []byte(ev.Payload), platform.FormatTime(ev.CreatedAt),
	); err != nil {
		return platform.TaskEvent{}, fmt.Errorf("insert event: %w", err)
	}
	return ev, nil
}

// ListEvents returns events for a task with sequence > after, up to limit.
func (s *Store) ListEvents(ctx context.Context, taskID string, after int64, limit int) ([]platform.TaskEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, task_id, sequence, source, event_type, turn_id, tool_call_id, payload, created_at
FROM task_events WHERE task_id = ? AND sequence > ? ORDER BY sequence LIMIT ?`, taskID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []platform.TaskEvent
	for rows.Next() {
		var (
			ev      platform.TaskEvent
			payload []byte
			created string
		)
		if err := rows.Scan(&ev.ID, &ev.TaskID, &ev.Sequence, &ev.Source, &ev.Type, &ev.TurnID, &ev.ToolCallID, &payload, &created); err != nil {
			return nil, err
		}
		ev.Payload = json.RawMessage(payload)
		t, err := platform.ParseTime(created)
		if err != nil {
			return nil, err
		}
		ev.CreatedAt = t
		out = append(out, ev)
	}
	return out, rows.Err()
}

// MaxEventSequence returns the highest sequence for a task (0 if none).
func (s *Store) MaxEventSequence(ctx context.Context, taskID string) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) FROM task_events WHERE task_id = ?`, taskID).Scan(&seq)
	return seq, err
}

// InsertModelUsage stores one usage row.
func (s *Store) InsertModelUsage(ctx context.Context, u platform.ModelUsage) (platform.ModelUsage, error) {
	if u.ID == "" {
		u.ID = platform.NewID()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = platform.NowUTC()
	}
	reported := 0
	if u.UsageReported {
		reported = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO model_usage (
 id, task_id, model, input_tokens, cached_input_tokens, output_tokens,
 reasoning_tokens, total_tokens, usage_reported, latency_ms, status_code,
 status, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.TaskID, u.Model, u.InputTokens, u.CachedInputTokens, u.OutputTokens,
		u.ReasoningTokens, u.TotalTokens, reported, u.LatencyMs, u.StatusCode,
		u.Status, platform.FormatTime(u.CreatedAt),
	)
	if err != nil {
		return platform.ModelUsage{}, err
	}
	return u, nil
}

// SummarizeModelUsage aggregates usage by day (UTC date).
func (s *Store) SummarizeModelUsage(ctx context.Context, since time.Time) (platform.ModelUsageSummary, error) {
	q := `
SELECT substr(created_at, 1, 10) AS day,
       COUNT(*),
       COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status_code < 200 OR status_code >= 400 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN usage_reported = 1 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(input_tokens), 0),
       COALESCE(SUM(cached_input_tokens), 0),
       COALESCE(SUM(output_tokens), 0),
       COALESCE(SUM(reasoning_tokens), 0),
       COALESCE(SUM(total_tokens), 0)
FROM model_usage`
	args := []any{}
	if !since.IsZero() {
		q += ` WHERE created_at >= ?`
		args = append(args, platform.FormatTime(since))
	}
	q += ` GROUP BY day ORDER BY day DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return platform.ModelUsageSummary{}, err
	}
	defer rows.Close()

	var days []platform.ModelUsageDay
	for rows.Next() {
		var d platform.ModelUsageDay
		if err := rows.Scan(
			&d.Date, &d.RequestCount, &d.SuccessfulRequests, &d.FailedRequests, &d.ReportedRequests,
			&d.InputTokens, &d.CachedInputTokens, &d.OutputTokens, &d.ReasoningTokens, &d.TotalTokens,
		); err != nil {
			return platform.ModelUsageSummary{}, err
		}
		days = append(days, d)
	}
	if days == nil {
		days = []platform.ModelUsageDay{}
	}
	return platform.ModelUsageSummary{Days: days}, rows.Err()
}

// GetSettings loads execution settings.
func (s *Store) GetSettings(ctx context.Context) (platform.ExecutionSettings, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'max_concurrent_tasks'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return platform.DefaultExecutionSettings(), nil
	}
	if err != nil {
		return platform.ExecutionSettings{}, err
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return platform.DefaultExecutionSettings(), nil
	}
	return platform.ExecutionSettings{MaxConcurrentTasks: n}, nil
}

// PutSettings stores execution settings.
func (s *Store) PutSettings(ctx context.Context, settings platform.ExecutionSettings) error {
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	now := platform.FormatTime(platform.NowUTC())
	_, err := s.db.ExecContext(ctx, `
INSERT INTO app_settings(key, value, updated_at) VALUES ('max_concurrent_tasks', ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		fmt.Sprintf("%d", settings.MaxConcurrentTasks), now,
	)
	return err
}

// DeleteTask removes a task and cascaded events/usage. Only non-active tasks.
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	task, err := s.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task.Status.IsActive() {
		return fmt.Errorf("%w: task is active", ErrConflict)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: task %s", ErrNotFound, id)
	}
	return nil
}

// CountTasks returns total task count.
func (s *Store) CountTasks(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&n)
	return n, err
}

// --- scanners ---

type scannable interface {
	Scan(dest ...any) error
}

func scanTask(row scannable) (platform.Task, error) {
	var (
		t                                            platform.Task
		cat, status, created, updated                string
		parent, handoff, title, desc, prompt, target string
		flagFmt, modelProfile, modelID, image        string
		runtime, containerID, lastError              string
		id                                           string
	)
	err := row.Scan(
		&id, &parent, &handoff, &title, &cat, &desc, &prompt, &target, &flagFmt, &modelProfile, &modelID, &status, &image,
		&runtime, &containerID, &lastError, &created, &updated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return platform.Task{}, fmt.Errorf("%w: task", ErrNotFound)
	}
	if err != nil {
		return platform.Task{}, err
	}
	t.ID = id
	t.ParentTaskID = parent
	t.HandoffID = handoff
	t.Title = title
	t.Category = platform.Category(cat)
	t.Description = desc
	t.Prompt = prompt
	t.Target = target
	t.FlagFormat = flagFmt
	t.ModelProfile = modelProfile
	t.ModelID = modelID
	t.Status = platform.TaskStatus(status)
	t.Image = image
	t.Runtime = runtime
	t.ContainerID = containerID
	t.LastError = lastError
	t.CreatedAt, err = platform.ParseTime(created)
	if err != nil {
		return platform.Task{}, err
	}
	t.UpdatedAt, err = platform.ParseTime(updated)
	if err != nil {
		return platform.Task{}, err
	}
	return t, nil
}

func scanTasks(rows *sql.Rows) ([]platform.Task, error) {
	var out []platform.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if out == nil {
		out = []platform.Task{}
	}
	return out, rows.Err()
}

func getTaskTx(tx *sql.Tx, id string) (platform.Task, error) {
	row := tx.QueryRow(`
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, model_profile, model_id, status, image,
 runtime, container_id, last_error, created_at, updated_at
FROM tasks WHERE id = ?`, id)
	return scanTask(row)
}
