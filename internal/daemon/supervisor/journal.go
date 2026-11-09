package supervisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（BSD-3-Clause），无 cgo 依赖
)

// 本地 Run 日志状态。日志只记录本机事实（是否已领取、是否已启动进程），
// 用于阻止重复唤醒或 Daemon 重启后对同一 (run, generation) 再次启动进程；
// Run 的业务状态始终以中心为准。
const (
	JournalClaiming  = "CLAIMING"
	JournalPreparing = "PREPARING"
	JournalRunning   = "RUNNING"
	JournalFinished  = "FINISHED"
	JournalAbandoned = "ABANDONED"
)

// journalRetention 是终态记录的保留期，启动时清理更旧的记录，保证文件有界。
const journalRetention = 7 * 24 * time.Hour

// outboxPerRun 是同一 Run 在事件 outbox 中最多保留的条数；超出时丢弃序号最小（最旧）的事件，
// 保留最近的步骤终态。
const outboxPerRun = 2000

var ErrJournal = errors.New("supervisor: journal unavailable")

// Journal 是基于 SQLite 的本地 Run 日志，文件权限 0600，目录 0700。
type Journal struct {
	db   *sql.DB
	now  func() time.Time
	path string
}

// Path 返回日志文件路径（存储统计用）。
func (j *Journal) Path() string {
	if j == nil {
		return ""
	}
	return j.path
}

// OpenJournal 打开（必要时创建）日志文件。path 必须是绝对路径。
func OpenJournal(ctx context.Context, path string) (*Journal, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("%w: journal path must be a clean absolute path", ErrJournal)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("%w: create journal directory: %v", ErrJournal, err)
	}
	// 先以 0600 创建文件，避免驱动按 umask 创建出组/其他可读的文件。
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: create journal file: %v", ErrJournal, err)
	}
	_ = file.Close()
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: journal file must be a user-private regular file", ErrJournal)
	}
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrJournal, err)
	}
	// 单连接即可：写入量很小，且避免 SQLite 多连接写锁竞争。
	db.SetMaxOpenConns(1)
	journal := &Journal{db: db, now: time.Now, path: path}
	if err := journal.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return journal, nil
}

func (j *Journal) migrate(ctx context.Context) error {
	_, err := j.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS storage_policies (organization_id TEXT PRIMARY KEY, version INTEGER NOT NULL, policy TEXT NOT NULL, next_check INTEGER NOT NULL);
		CREATE TABLE IF NOT EXISTS device_preferences (id INTEGER PRIMARY KEY CHECK(id=1), version INTEGER NOT NULL, settings TEXT NOT NULL);
		CREATE TABLE IF NOT EXISTS run_journal (
			run_id          TEXT    NOT NULL,
			generation      INTEGER NOT NULL CHECK (generation > 0),
			organization_id TEXT    NOT NULL,
			offer_id        TEXT    NOT NULL,
			state           TEXT    NOT NULL CHECK (state IN ('CLAIMING','PREPARING','RUNNING','FINISHED','ABANDONED')),
			detail          TEXT    NOT NULL DEFAULT '',
			created_at      INTEGER NOT NULL,
			updated_at      INTEGER NOT NULL,
			PRIMARY KEY (run_id, generation)
		);
		-- 上传失败的事件，待下次 flush 重试；body 是 EventInput 的 JSON。
		CREATE TABLE IF NOT EXISTS event_outbox (
			event_id        TEXT    NOT NULL PRIMARY KEY,
			organization_id TEXT    NOT NULL,
			run_id          TEXT    NOT NULL,
			sequence        INTEGER NOT NULL,
			body            TEXT    NOT NULL,
			created_at      INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS event_outbox_run_idx ON event_outbox (run_id, sequence);
		-- Only this device sees physical paths. The verified task/agent on each row
		-- disambiguates crew members and prevents reuse across tenants or tasks.
		CREATE TABLE IF NOT EXISTS conversation_workdirs (
			device_id TEXT NOT NULL, organization_id TEXT NOT NULL, space_id TEXT NOT NULL,
			conversation_id TEXT NOT NULL, agent_id TEXT NOT NULL, task_id TEXT NOT NULL,
			logical_directory TEXT NOT NULL, selection TEXT NOT NULL, binding TEXT NOT NULL,
			PRIMARY KEY(device_id,organization_id,space_id,conversation_id,agent_id)
		);
		CREATE TABLE IF NOT EXISTS conversation_directory_facts (
			device_id TEXT NOT NULL, organization_id TEXT NOT NULL, space_id TEXT NOT NULL,
			conversation_id TEXT NOT NULL, agent_id TEXT NOT NULL, run_id TEXT NOT NULL,
			generation INTEGER NOT NULL, path TEXT NOT NULL,
			PRIMARY KEY(device_id,organization_id,space_id,conversation_id,agent_id)
		);
		-- Daemon 创建的 Git worktree 与最近使用它的 Run，供存储清理判断“已结束且不在使用”。
		-- 行在 worktree 被清理时删除；不参与 run_journal 的保留期清理。
		CREATE TABLE IF NOT EXISTS workspaces (
			path            TEXT    NOT NULL PRIMARY KEY,
			repository_root TEXT    NOT NULL,
			last_run_id     TEXT    NOT NULL,
			last_generation INTEGER NOT NULL,
			last_used_at    INTEGER NOT NULL
		);`)
	if err != nil {
		return fmt.Errorf("%w: migrate: %v", ErrJournal, err)
	}
	// workspaces 增加 kind（task / conversation），供存储统计按类型细分；旧库按列缺失补上，已有行为空串（统计归「其他」）。
	var hasKind int
	if err := j.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('workspaces') WHERE name = 'kind'`).Scan(&hasKind); err != nil {
		return fmt.Errorf("%w: migrate: %v", ErrJournal, err)
	}
	if hasKind == 0 {
		if _, err := j.db.ExecContext(ctx, `ALTER TABLE workspaces ADD COLUMN kind TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("%w: migrate: %v", ErrJournal, err)
		}
	}
	var hasOrg int
	if err := j.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('workspaces') WHERE name='organization_id'`).Scan(&hasOrg); err != nil {
		return err
	}
	if hasOrg == 0 {
		tx, err := j.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `ALTER TABLE workspaces ADD COLUMN organization_id TEXT NOT NULL DEFAULT '';
		  UPDATE workspaces SET organization_id=COALESCE((SELECT organization_id FROM run_journal WHERE run_id=workspaces.last_run_id AND generation=workspaces.last_generation),'');`); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}

	return nil
}

// SetWorkspaceKind 记录 worktree 的用途（task / conversation）。
func (j *Journal) SetWorkspaceKind(ctx context.Context, path, kind string) error {
	if _, err := j.db.ExecContext(ctx, `UPDATE workspaces SET kind = ? WHERE path = ?`, kind, path); err != nil {
		return fmt.Errorf("%w: set workspace kind: %v", ErrJournal, err)
	}
	return nil
}

// WorkspaceKinds 返回最多 limit 个已登记 worktree 的路径到用途的映射（用途为空的不含）。
func (j *Journal) WorkspaceKinds(ctx context.Context, limit int) (map[string]string, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT path, kind FROM workspaces WHERE kind <> '' LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: workspace kinds: %v", ErrJournal, err)
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var path, kind string
		if err := rows.Scan(&path, &kind); err != nil {
			return nil, fmt.Errorf("%w: workspace kinds: %v", ErrJournal, err)
		}
		result[path] = kind
	}
	return result, rows.Err()
}

// OutboxRun 标识 outbox 中有待上传事件的 Run。
type OutboxRun struct {
	OrganizationID string
	RunID          string
}

// SaveEvents 把上传失败的事件写入 outbox（同一事件 ID 重复写入时忽略），
// 并把该 Run 的条数裁剪到 outboxPerRun。
func (j *Journal) SaveEvents(ctx context.Context, organizationID, runID string, events []EventInput) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: save events: %v", ErrJournal, err)
	}
	defer func() { _ = tx.Rollback() }()
	now := j.now().UnixMilli()
	for _, event := range events {
		body, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("%w: save events: %v", ErrJournal, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO event_outbox (event_id, organization_id, run_id, sequence, body, created_at)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (event_id) DO NOTHING`,
			event.ID, organizationID, runID, event.Sequence, string(body), now); err != nil {
			return fmt.Errorf("%w: save events: %v", ErrJournal, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM event_outbox WHERE run_id = ? AND event_id NOT IN (
			SELECT event_id FROM event_outbox WHERE run_id = ? ORDER BY sequence DESC LIMIT ?)`,
		runID, runID, outboxPerRun); err != nil {
		return fmt.Errorf("%w: prune events: %v", ErrJournal, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: save events: %v", ErrJournal, err)
	}
	return nil
}

// PendingEvents 按序号返回某个 Run 最早的 limit 条待上传事件。
func (j *Journal) PendingEvents(ctx context.Context, runID string, limit int) ([]EventInput, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT body FROM event_outbox WHERE run_id = ? ORDER BY sequence LIMIT ?`, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: pending events: %v", ErrJournal, err)
	}
	defer rows.Close()
	var events []EventInput
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("%w: pending events: %v", ErrJournal, err)
		}
		var event EventInput
		if err := json.Unmarshal([]byte(body), &event); err != nil {
			// 无法解析的记录不会再变好：跳过，由 DeleteEvents / 保留期清理。
			continue
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// DeleteEvents 删除已上传（或被中心永久拒绝）的事件。
func (j *Journal) DeleteEvents(ctx context.Context, eventIDs []string) error {
	if len(eventIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(eventIDs)), ",")
	args := make([]any, len(eventIDs))
	for index, id := range eventIDs {
		args[index] = id
	}
	if _, err := j.db.ExecContext(ctx, `DELETE FROM event_outbox WHERE event_id IN (`+placeholders+`)`, args...); err != nil {
		return fmt.Errorf("%w: delete events: %v", ErrJournal, err)
	}
	return nil
}

// OutboxRuns 返回有待上传事件的 Run。
func (j *Journal) OutboxRuns(ctx context.Context) ([]OutboxRun, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT DISTINCT organization_id, run_id FROM event_outbox ORDER BY run_id`)
	if err != nil {
		return nil, fmt.Errorf("%w: outbox runs: %v", ErrJournal, err)
	}
	defer rows.Close()
	var runs []OutboxRun
	for rows.Next() {
		var run OutboxRun
		if err := rows.Scan(&run.OrganizationID, &run.RunID); err != nil {
			return nil, fmt.Errorf("%w: outbox runs: %v", ErrJournal, err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (j *Journal) Close() error {
	if j == nil || j.db == nil {
		return nil
	}
	return j.db.Close()
}

// Begin 原子登记一次领取尝试；同一 (run, generation) 已存在时返回 false，
// 调用方必须放弃，从而保证重复唤醒不会产生第二个进程。
func (j *Journal) Begin(ctx context.Context, organizationID, runID string, generation int64, offerID string) (bool, error) {
	now := j.now().UnixMilli()
	result, err := j.db.ExecContext(ctx, `
		INSERT INTO run_journal (run_id, generation, organization_id, offer_id, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'CLAIMING', ?, ?)
		ON CONFLICT (run_id, generation) DO NOTHING`, runID, generation, organizationID, offerID, now, now)
	if err != nil {
		return false, fmt.Errorf("%w: begin: %v", ErrJournal, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%w: begin: %v", ErrJournal, err)
	}
	return affected == 1, nil
}

// Mark 更新本地状态；终态之后不再变化。
func (j *Journal) Mark(ctx context.Context, runID string, generation int64, state, detail string) error {
	if len(detail) > 512 {
		detail = detail[:512]
	}
	_, err := j.db.ExecContext(ctx, `
		UPDATE run_journal SET state = ?, detail = ?, updated_at = ?
		 WHERE run_id = ? AND generation = ? AND state NOT IN ('FINISHED','ABANDONED')`,
		state, detail, j.now().UnixMilli(), runID, generation)
	if err != nil {
		return fmt.Errorf("%w: mark: %v", ErrJournal, err)
	}
	return nil
}

// State 返回本地记录的状态；不存在时返回空串。
func (j *Journal) State(ctx context.Context, runID string, generation int64) (string, error) {
	var state string
	err := j.db.QueryRowContext(ctx, `SELECT state FROM run_journal WHERE run_id = ? AND generation = ?`, runID, generation).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: state: %v", ErrJournal, err)
	}
	return state, nil
}

// Workspace 是 journal 记录的一个 Daemon 管理的 worktree。
type Workspace struct {
	Path           string
	RepositoryRoot string
	LastRunID      string
	LastGeneration int64
	LastUsedAt     time.Time
}

// RecordWorkspace 登记（或刷新）某个 Run 使用的 worktree。
func (j *Journal) RecordWorkspace(ctx context.Context, path, repositoryRoot, runID string, generation int64) error {
	_, err := j.db.ExecContext(ctx, `
		INSERT INTO workspaces (path, repository_root, last_run_id, last_generation, last_used_at, organization_id)
		VALUES (?, ?, ?, ?, ?, COALESCE((SELECT organization_id FROM run_journal WHERE run_id=? AND generation=?),''))
		ON CONFLICT (path) DO UPDATE SET repository_root = excluded.repository_root, last_run_id = excluded.last_run_id,
		       last_generation = excluded.last_generation, last_used_at = excluded.last_used_at, organization_id=excluded.organization_id, kind=''`,
		path, repositoryRoot, runID, generation, j.now().UnixMilli(), runID, generation)
	if err != nil {
		return fmt.Errorf("%w: record workspace: %v", ErrJournal, err)
	}
	return nil
}

// TouchWorkspace 在 Run 结束时刷新最近使用时间（保留期从最后一次使用结束算起）。
func (j *Journal) TouchWorkspace(ctx context.Context, path string) error {
	if _, err := j.db.ExecContext(ctx, `UPDATE workspaces SET last_used_at = ? WHERE path = ?`, j.now().UnixMilli(), path); err != nil {
		return fmt.Errorf("%w: touch workspace: %v", ErrJournal, err)
	}
	return nil
}

// Workspaces 返回最多 limit 个已登记的 worktree，最久未用的在前。
func (j *Journal) Workspaces(ctx context.Context, limit int) ([]Workspace, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT path, repository_root, last_run_id, last_generation, last_used_at
		  FROM workspaces ORDER BY last_used_at LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: workspaces: %v", ErrJournal, err)
	}
	defer rows.Close()
	var result []Workspace
	for rows.Next() {
		var workspace Workspace
		var usedAt int64
		if err := rows.Scan(&workspace.Path, &workspace.RepositoryRoot, &workspace.LastRunID, &workspace.LastGeneration, &usedAt); err != nil {
			return nil, fmt.Errorf("%w: workspaces: %v", ErrJournal, err)
		}
		workspace.LastUsedAt = time.UnixMilli(usedAt)
		result = append(result, workspace)
	}
	return result, rows.Err()
}

// DeleteWorkspace 删除已清理 worktree 的登记。
func (j *Journal) DeleteWorkspace(ctx context.Context, path string) error {
	if _, err := j.db.ExecContext(ctx, `DELETE FROM workspaces WHERE path = ?`, path); err != nil {
		return fmt.Errorf("%w: delete workspace: %v", ErrJournal, err)
	}
	return nil
}

// Prune 删除超过保留期的终态记录与 outbox 事件，返回删除的行数。
func (j *Journal) Prune(ctx context.Context) (int64, error) {
	cutoff := j.now().Add(-journalRetention).UnixMilli()
	result, err := j.db.ExecContext(ctx, `
		DELETE FROM run_journal WHERE state IN ('FINISHED','ABANDONED') AND updated_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("%w: prune: %v", ErrJournal, err)
	}
	runs, _ := result.RowsAffected()
	// 超过保留期仍未上传的事件不再有意义（中心的 Run 早已结束或被核对）。
	result, err = j.db.ExecContext(ctx, `DELETE FROM event_outbox WHERE created_at < ?`, cutoff)
	if err != nil {
		return runs, fmt.Errorf("%w: prune events: %v", ErrJournal, err)
	}
	events, _ := result.RowsAffected()
	return runs + events, nil
}

// Recover 在启动时把上次未结束的记录标为 ABANDONED 并返回数量。
// 不尝试接管或杀死旧进程（PID 可能已被复用）；中心会在租约过期后要求核对。
func (j *Journal) Recover(ctx context.Context) (int, error) {
	now := j.now()
	result, err := j.db.ExecContext(ctx, `
		UPDATE run_journal SET state = 'ABANDONED', detail = 'daemon restarted before the run finished', updated_at = ?
		 WHERE state IN ('CLAIMING','PREPARING','RUNNING')`, now.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("%w: recover: %v", ErrJournal, err)
	}
	abandoned, _ := result.RowsAffected()
	if _, err := j.Prune(ctx); err != nil {
		return int(abandoned), err
	}
	return int(abandoned), nil
}
