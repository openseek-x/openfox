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

	"openfox/internal/daemon/workdir"
	"openfox/internal/runtime"
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

// reportRetention 是尚未上报成功的核对结果的最长保留期（超过后放弃，避免文件无界增长）。
const reportRetention = 30 * 24 * time.Hour

// maxPendingReports 是一次最多处理的待核对/待上报记录数（有界，沿用事件 outbox 的 1000 条量级）。
const maxPendingReports = 1000

// outboxPerRun 是同一 Run 在事件 outbox 中最多保留的条数；超出时丢弃序号最小（最旧）的事件，
// 保留最近的步骤终态。
const outboxPerRun = 2000

var ErrJournal = errors.New("supervisor: journal unavailable")

// Journal 是基于 SQLite 的本地 Run 日志，文件权限 0600，目录 0700。
type Journal struct {
	StoragePaths workdir.StoragePaths
	db           *sql.DB
	now          func() time.Time
	path         string
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
		-- 结果必须先落盘；每个代次只有一份固定 ID 的结果，成功确认后才删除。
		CREATE TABLE IF NOT EXISTS result_outbox (
			organization_id TEXT NOT NULL, run_id TEXT NOT NULL, generation INTEGER NOT NULL,
			body TEXT NOT NULL, PRIMARY KEY (run_id, generation)
		);
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
	// 阶段 7f：workspaces 增加 kind（task / conversation），供存储统计按类型细分；旧库按列缺失补上，已有行为空串（统计归「其他」）。
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

	// 阶段 11h：workspaces 增加 conversation_id（可空），供“此对话”存储预览和清理按会话限定。
	// 按 conversation_directory_facts 的路径回填；同一路径对应多个会话（有歧义）或匹配不上的保持 NULL。
	// 回填只填 NULL 行，重复升级不改变已有值。
	var hasConversation int
	if err := j.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('workspaces') WHERE name='conversation_id'`).Scan(&hasConversation); err != nil {
		return fmt.Errorf("%w: migrate: %v", ErrJournal, err)
	}
	if hasConversation == 0 {
		if _, err := j.db.ExecContext(ctx, `ALTER TABLE workspaces ADD COLUMN conversation_id TEXT`); err != nil {
			return fmt.Errorf("%w: migrate: %v", ErrJournal, err)
		}
	}
	if _, err := j.db.ExecContext(ctx, `UPDATE workspaces SET conversation_id=(
		SELECT MIN(f.conversation_id) FROM conversation_directory_facts f
		 WHERE f.path=workspaces.path AND f.organization_id=workspaces.organization_id
		HAVING COUNT(DISTINCT f.conversation_id)=1) WHERE conversation_id IS NULL`); err != nil {
		return fmt.Errorf("%w: migrate: %v", ErrJournal, err)
	}

	// 阶段 12a：run_journal 增加进程身份与核对上报列。旧库按列缺失补上；旧行 proc_known 为 NULL，
	// 重启核对时视为“不确定”（无法证明当时的进程身份，绝不据此终止进程或收尾 Run）。
	for _, column := range []struct{ name, ddl string }{
		{"proc_known", "INTEGER"}, {"pid", "INTEGER"}, {"start_token", "INTEGER"}, {"pgid", "INTEGER"},
		{"report_status", "TEXT NOT NULL DEFAULT ''"}, {"report_id", "TEXT NOT NULL DEFAULT ''"},
		{"report_kind", "TEXT NOT NULL DEFAULT ''"}, {"report_certain", "INTEGER NOT NULL DEFAULT 0"},
		{"report_detail", "TEXT NOT NULL DEFAULT ''"},
		{"claim_retry", "INTEGER NOT NULL DEFAULT 0"},
	} {
		var present int
		if err := j.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('run_journal') WHERE name = ?`, column.name).Scan(&present); err != nil {
			return fmt.Errorf("%w: migrate: %v", ErrJournal, err)
		}
		if present == 0 {
			if _, err := j.db.ExecContext(ctx, `ALTER TABLE run_journal ADD COLUMN `+column.name+` `+column.ddl); err != nil {
				return fmt.Errorf("%w: migrate: %v", ErrJournal, err)
			}
		}
	}

	return nil
}

// SetWorkspaceConversation 记录创建/复用 worktree 的会话；会话 ID 为空时保持 NULL。
func (j *Journal) SetWorkspaceConversation(ctx context.Context, path, conversationID string) error {
	if _, err := j.db.ExecContext(ctx, `UPDATE workspaces SET conversation_id = NULLIF(?, '') WHERE path = ?`, conversationID, j.StoragePaths.Stored(path)); err != nil {
		return fmt.Errorf("%w: set workspace conversation: %v", ErrJournal, err)
	}
	return nil
}

// SetWorkspaceKind 记录 worktree 的用途（task / conversation）。
func (j *Journal) SetWorkspaceKind(ctx context.Context, path, kind string) error {
	if _, err := j.db.ExecContext(ctx, `UPDATE workspaces SET kind = ? WHERE path = ?`, kind, j.StoragePaths.Stored(path)); err != nil {
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
		result[j.StoragePaths.Current(path)] = kind
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

// RetryClaim 只放开尚未取得租约、也没有进程和待上报证据的领取尝试。
func (j *Journal) RetryClaim(ctx context.Context, runID string, generation int64) error {
	_, err := j.db.ExecContext(ctx, `UPDATE run_journal SET claim_retry = 1, updated_at = ?
	 WHERE run_id = ? AND generation = ? AND state = 'CLAIMING' AND proc_known = 1
	 AND pid IS NULL AND report_status = ''`, j.now().UnixMilli(), runID, generation)
	return err
}

// PendingClaims 包含响应丢失后不再出现在 LIVE offer 列表中的领取尝试。
func (j *Journal) PendingClaims(ctx context.Context) ([]Offer, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT organization_id, run_id, generation, offer_id
	 FROM run_journal WHERE state = 'CLAIMING' AND claim_retry = 1 AND report_status = '' ORDER BY updated_at LIMIT ?`, maxPendingReports)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var offers []Offer
	for rows.Next() {
		var o Offer
		if err := rows.Scan(&o.OrganizationID, &o.RunID, &o.Generation, &o.OfferID); err != nil {
			return nil, err
		}
		offers = append(offers, o)
	}
	return offers, rows.Err()
}

type PendingResult struct {
	OrganizationID string
	RunID          string
	Input          ResultInput
}

// SaveResult 与本地终态原子落盘，防止重启把已完成但尚未上传的运行按进程丢失收尾。
func (j *Journal) SaveResult(ctx context.Context, org, run string, input ResultInput) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO result_outbox (organization_id,run_id,generation,body) VALUES (?,?,?,?)
	 ON CONFLICT(run_id,generation) DO UPDATE SET body = excluded.body`, org, run, input.Generation, string(body)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE run_journal SET state = 'FINISHED', updated_at = ? WHERE run_id = ? AND generation = ?`, j.now().UnixMilli(), run, input.Generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (j *Journal) PendingResults(ctx context.Context) ([]PendingResult, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT organization_id,run_id,body FROM result_outbox ORDER BY run_id,generation LIMIT ?`, maxPendingReports)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PendingResult
	for rows.Next() {
		var r PendingResult
		var body string
		if err := rows.Scan(&r.OrganizationID, &r.RunID, &body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &r.Input); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// RejectFinalResult keeps the reason in the terminal journal until its normal retention expires.
func (j *Journal) RejectFinalResult(ctx context.Context, run string, input ResultInput) error {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE run_journal SET detail=?, updated_at=? WHERE run_id=? AND generation=?`, "RESULT_FINALIZED: "+input.ID, j.now().UnixMilli(), run, input.Generation); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM result_outbox WHERE run_id=? AND generation=?`, run, input.Generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (j *Journal) DeleteResult(ctx context.Context, run string, generation int64) error {
	_, err := j.db.ExecContext(ctx, `DELETE FROM result_outbox WHERE run_id = ? AND generation = ?`, run, generation)
	return err
}

// Begin 原子登记领取；仅允许明确标记为可重试且尚无进程/证据的 CLAIMING 记录重领。
// 其余重复唤醒返回 false，避免同一代次启动第二个进程。
func (j *Journal) Begin(ctx context.Context, organizationID, runID string, generation int64, offerID string) (bool, error) {
	now := j.now().UnixMilli()
	result, err := j.db.ExecContext(ctx, `
		INSERT INTO run_journal (run_id, generation, organization_id, offer_id, state, created_at, updated_at, proc_known)
		VALUES (?, ?, ?, ?, 'CLAIMING', ?, ?, 1)
		ON CONFLICT (run_id, generation) DO UPDATE SET offer_id = excluded.offer_id,
		 claim_retry = 0, updated_at = excluded.updated_at
		 WHERE run_journal.state = 'CLAIMING' AND run_journal.claim_retry = 1
		   AND run_journal.report_status = '' AND run_journal.proc_known = 1 AND run_journal.pid IS NULL`, runID, generation, organizationID, offerID, now, now)
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
		j.StoragePaths.Stored(path), j.StoragePaths.Stored(repositoryRoot), runID, generation, j.now().UnixMilli(), runID, generation)
	if err != nil {
		return fmt.Errorf("%w: record workspace: %v", ErrJournal, err)
	}
	return nil
}

// TouchWorkspace 在 Run 结束时刷新最近使用时间（保留期从最后一次使用结束算起）。
func (j *Journal) TouchWorkspace(ctx context.Context, path string) error {
	if _, err := j.db.ExecContext(ctx, `UPDATE workspaces SET last_used_at = ? WHERE path = ?`, j.now().UnixMilli(), j.StoragePaths.Stored(path)); err != nil {
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
		workspace.Path = j.StoragePaths.Current(workspace.Path)
		workspace.RepositoryRoot = j.StoragePaths.Current(workspace.RepositoryRoot)
		workspace.LastUsedAt = time.UnixMilli(usedAt)
		result = append(result, workspace)
	}
	return result, rows.Err()
}

// DeleteWorkspace 删除已清理 worktree 的登记。
func (j *Journal) DeleteWorkspace(ctx context.Context, path string) error {
	if _, err := j.db.ExecContext(ctx, `DELETE FROM workspaces WHERE path = ?`, j.StoragePaths.Stored(path)); err != nil {
		return fmt.Errorf("%w: delete workspace: %v", ErrJournal, err)
	}
	return nil
}

// Prune 删除超过保留期的终态记录与 outbox 事件，返回删除的行数。
func (j *Journal) Prune(ctx context.Context) (int64, error) {
	cutoff := j.now().Add(-journalRetention).UnixMilli()
	result, err := j.db.ExecContext(ctx, `
		DELETE FROM run_journal WHERE state IN ('FINISHED','ABANDONED') AND updated_at < ?
		   AND NOT EXISTS (SELECT 1 FROM result_outbox r WHERE r.run_id = run_journal.run_id AND r.generation = run_journal.generation)
		   AND (report_status NOT IN ('CHECK','PENDING') OR updated_at < ?)`, cutoff, j.now().Add(-reportRetention).UnixMilli())
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
// 本方法只处理本地记录；进程核对（含终止旧进程）与上报由 Supervisor.recoverProcesses 基于 AbandonUnfinished 完成。
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

// 核对上报状态：CHECK 表示已放弃但尚未完成进程核对；PENDING 表示已有核对结论、待上报；DONE 已送达。
const (
	ReportCheck   = "CHECK"
	ReportPending = "PENDING"
	ReportDone    = "DONE"
)

// MarkProcessLaunching 在调用 Runtime Start 之前调用：从此刻起进程身份“未知”，直到 RecordProcess 记下进程号。
// 这样在 Start 之后、记录之前崩溃，或适配器无法给出进程号时，重启核对都按“不确定”处理，不会误判为“从未启动”。
func (j *Journal) MarkProcessLaunching(ctx context.Context, runID string, generation int64) error {
	result, err := j.db.ExecContext(ctx, `UPDATE run_journal SET proc_known = 0 WHERE run_id = ? AND generation = ?`, runID, generation)
	if err != nil {
		return fmt.Errorf("%w: mark launching: %v", ErrJournal, err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("%w: mark launching affected %d rows: %v", ErrJournal, n, err)
	}
	return nil
}

// MarkNoProcess 记录“Runtime 启动失败，确定没有遗留进程”，重启核对据此给出确定的 PROCESS_NOT_FOUND。
func (j *Journal) MarkNoProcess(ctx context.Context, runID string, generation int64) error {
	if _, err := j.db.ExecContext(ctx, `UPDATE run_journal SET proc_known = 1, pid = NULL WHERE run_id = ? AND generation = ?`, runID, generation); err != nil {
		return fmt.Errorf("%w: mark no process: %v", ErrJournal, err)
	}
	return nil
}

// RecordProcess 记录 Run 的 Runtime 进程身份（启动进程后立即调用），供重启后核对。
func (j *Journal) RecordProcess(ctx context.Context, runID string, generation int64, identity runtime.ProcessIdentity) error {
	if identity.PID <= 0 {
		return nil
	}
	if _, err := j.db.ExecContext(ctx, `
		UPDATE run_journal SET pid = ?, start_token = ?, pgid = ?, proc_known = 1, updated_at = ?
		 WHERE run_id = ? AND generation = ?`,
		identity.PID, identity.StartToken, identity.PGID, j.now().UnixMilli(), runID, generation); err != nil {
		return fmt.Errorf("%w: record process: %v", ErrJournal, err)
	}
	return nil
}

// RecoveredRun 是重启时发现的、上次未结束的本地记录。
type RecoveredRun struct {
	OrganizationID string
	RunID          string
	Generation     int64
	// ProcKnown 为 false 表示旧格式记录（升级前写入），进程身份无从核对。
	ProcKnown bool
	Identity  runtime.ProcessIdentity
	// EvidenceID 在放弃时一次性生成并持久化，后续重发保持不变，作为中心的幂等键。
	EvidenceID string
}

// AbandonUnfinished 在启动时把上次未结束的记录标为 ABANDONED 并标记待核对（CHECK），返回全部待核对记录
// （包含上次启动已放弃但没来得及完成核对的）。单事务完成，崩溃后下次启动可从 CHECK 状态继续，且同一 Run 不会生成第二个证据 ID。
func (j *Journal) AbandonUnfinished(ctx context.Context) ([]RecoveredRun, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: recover: %v", ErrJournal, err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT run_id, generation FROM run_journal
		 WHERE state IN ('CLAIMING','PREPARING','RUNNING','ABANDONED') AND report_status = ''
		   AND claim_retry = 0
		   AND NOT EXISTS (SELECT 1 FROM result_outbox r WHERE r.run_id = run_journal.run_id AND r.generation = run_journal.generation)`)
	if err != nil {
		return nil, fmt.Errorf("%w: recover: %v", ErrJournal, err)
	}
	type key struct {
		run string
		gen int64
	}
	var fresh []key
	for rows.Next() {
		var item key
		if err := rows.Scan(&item.run, &item.gen); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("%w: recover: %v", ErrJournal, err)
		}
		fresh = append(fresh, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("%w: recover: %v", ErrJournal, err)
	}
	_ = rows.Close()
	now := j.now().UnixMilli()
	for _, item := range fresh {
		if _, err := tx.ExecContext(ctx, `
			UPDATE run_journal SET state = 'ABANDONED', detail = 'daemon restarted before the run finished',
			       updated_at = ?, report_status = 'CHECK', report_id = ?
			 WHERE run_id = ? AND generation = ?`, now, newUUID(), item.run, item.gen); err != nil {
			return nil, fmt.Errorf("%w: recover: %v", ErrJournal, err)
		}
	}
	result, err := tx.QueryContext(ctx, `
		SELECT organization_id, run_id, generation, COALESCE(proc_known, 0), COALESCE(pid, 0),
		       COALESCE(start_token, 0), COALESCE(pgid, 0), report_id
		  FROM run_journal WHERE report_status = 'CHECK' ORDER BY updated_at LIMIT ?`, maxPendingReports)
	if err != nil {
		return nil, fmt.Errorf("%w: recover: %v", ErrJournal, err)
	}
	var runs []RecoveredRun
	for result.Next() {
		var run RecoveredRun
		var known int
		if err := result.Scan(&run.OrganizationID, &run.RunID, &run.Generation, &known, &run.Identity.PID,
			&run.Identity.StartToken, &run.Identity.PGID, &run.EvidenceID); err != nil {
			_ = result.Close()
			return nil, fmt.Errorf("%w: recover: %v", ErrJournal, err)
		}
		run.ProcKnown = known == 1
		runs = append(runs, run)
	}
	if err := result.Err(); err != nil {
		_ = result.Close()
		return nil, fmt.Errorf("%w: recover: %v", ErrJournal, err)
	}
	_ = result.Close()
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("%w: recover: %v", ErrJournal, err)
	}
	return runs, nil
}

// SaveReport 保存核对结论并转入待上报（PENDING）。只有 CHECK 状态的记录可写，结论写入后不再改变。
func (j *Journal) SaveReport(ctx context.Context, runID string, generation int64, kind string, certain bool, detail string) error {
	if len(detail) > 512 {
		detail = detail[:512]
	}
	flag := 0
	if certain {
		flag = 1
	}
	if _, err := j.db.ExecContext(ctx, `
		UPDATE run_journal SET report_status = 'PENDING', report_kind = ?, report_certain = ?, report_detail = ?, updated_at = ?
		 WHERE run_id = ? AND generation = ? AND report_status = 'CHECK'`,
		kind, flag, detail, j.now().UnixMilli(), runID, generation); err != nil {
		return fmt.Errorf("%w: save report: %v", ErrJournal, err)
	}
	return nil
}

// QueueReport 为一条没有经过重启核对的记录直接登记待上报结论（PENDING），并生成证据 ID。
// 用于 daemon 自己因租约丢失停掉 Runtime 的情况：进程已确定退出，但结果不能再上传，
// 中心只有收到这条证据才能结束「待核对」并释放对话。已有核对状态的记录不覆盖。
func (j *Journal) QueueReport(ctx context.Context, runID string, generation int64, kind string, certain bool, detail string) error {
	if len(detail) > 512 {
		detail = detail[:512]
	}
	flag := 0
	if certain {
		flag = 1
	}
	if _, err := j.db.ExecContext(ctx, `
		UPDATE run_journal SET report_status = 'PENDING', report_id = ?, report_kind = ?, report_certain = ?, report_detail = ?, updated_at = ?
		 WHERE run_id = ? AND generation = ? AND report_status = ''`,
		newUUID(), kind, flag, detail, j.now().UnixMilli(), runID, generation); err != nil {
		return fmt.Errorf("%w: queue report: %v", ErrJournal, err)
	}
	return nil
}

// PendingReport 是待上报的核对结论。
type PendingReport struct {
	OrganizationID string
	RunID          string
	Generation     int64
	EvidenceID     string
	Kind           string
	Certain        bool
	Detail         string
	PID            int
	StartToken     int64
}

// PendingReports 返回待上报的核对结论（有界）。
func (j *Journal) PendingReports(ctx context.Context) ([]PendingReport, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT organization_id, run_id, generation, report_id, report_kind, report_certain, report_detail,
		       COALESCE(pid, 0), COALESCE(start_token, 0)
		  FROM run_journal WHERE report_status = 'PENDING' ORDER BY updated_at LIMIT ?`, maxPendingReports)
	if err != nil {
		return nil, fmt.Errorf("%w: pending reports: %v", ErrJournal, err)
	}
	defer rows.Close()
	var reports []PendingReport
	for rows.Next() {
		var report PendingReport
		var certain int
		if err := rows.Scan(&report.OrganizationID, &report.RunID, &report.Generation, &report.EvidenceID,
			&report.Kind, &certain, &report.Detail, &report.PID, &report.StartToken); err != nil {
			return nil, fmt.Errorf("%w: pending reports: %v", ErrJournal, err)
		}
		report.Certain = certain == 1
		reports = append(reports, report)
	}
	return reports, rows.Err()
}

// MarkReportDone 标记核对结论已送达中心（或被中心永久拒绝而无需再试）。
func (j *Journal) MarkReportDone(ctx context.Context, runID string, generation int64) error {
	if _, err := j.db.ExecContext(ctx, `
		UPDATE run_journal SET report_status = 'DONE', updated_at = ?
		 WHERE run_id = ? AND generation = ? AND report_status = 'PENDING'`, j.now().UnixMilli(), runID, generation); err != nil {
		return fmt.Errorf("%w: mark report: %v", ErrJournal, err)
	}
	return nil
}

// ReportStatus 返回记录的核对上报状态（测试与诊断用）。
func (j *Journal) ReportStatus(ctx context.Context, runID string, generation int64) (string, error) {
	var status string
	err := j.db.QueryRowContext(ctx, `SELECT report_status FROM run_journal WHERE run_id = ? AND generation = ?`, runID, generation).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: report status: %v", ErrJournal, err)
	}
	return status, nil
}
