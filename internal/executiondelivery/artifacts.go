package executiondelivery

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/conversations"
	"openfox/internal/deviceauth"
	"openfox/internal/execution"
	"openfox/internal/membership"
)

// 会话产物：电脑端发布工具（openfox_publish_artifact）上传的文件。
// 设备端点只接受该 Run 的执行电脑、未结束的 Run 与当前代次；用户端点按会话可见性读取。

const (
	// MaxRunArtifacts 是每次 Run 最多登记的产物数；MaxConversationArtifacts 是会话产物列表的上限。
	MaxRunArtifacts          = 10
	MaxConversationArtifacts = 200
	// maxArtifactName 是文件名的字符上限（与 artifacts_name_valid 约束一致）。
	maxArtifactName = 255
)

// CheckArtifactUpload 在写入文件字节之前校验上传目标：只有该 Run 的执行电脑可上传，
// Run 必须未结束、generation 必须等于当前代次，artifactID 不能已被占用（避免覆盖已有对象）。
// AddArtifact 在行锁下再校验一次；这里的目的是不让过期电脑端白写一次存储。
func (s Service) CheckArtifactUpload(ctx context.Context, tx pgx.Tx, principal deviceauth.Principal, runID string, generation int64, artifactID string) error {
	if !membership.ValidateUUID(runID) || !membership.ValidateUUID(principal.DeviceID) || (artifactID != "" && !membership.ValidateUUID(artifactID)) {
		return ErrInvalidRequest
	}
	if _, err := lockArtifactRun(ctx, tx, runID, principal.DeviceID, generation); err != nil {
		return err
	}
	if artifactID == "" {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.artifacts
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid)`, artifactID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrResultConflict
	}
	return nil
}

// AddArtifact 登记一个已写入存储的产物。created 为 false 表示同一 Run 已有 sha256 相同的可用产物，
// 返回的是已有产物（调用方应删除这次新写入的对象）。校验：
//   - Run 未结束、generation 等于当前代次（防止过期电脑端写入）；
//   - 文件名只取最后一个路径成分并清理控制字符；MIME 不在可预览白名单内一律记为 application/octet-stream；
//   - 单个文件 ≤ 组织策略、每次 Run 合计 ≤ 组织策略、最多 MaxRunArtifacts 个。
//
// 新登记的产物在同一事务内写组织事件 run.artifact，前端据此实时刷新产物列表。
func (s Service) AddArtifact(ctx context.Context, tx pgx.Tx, organizationID string, principal deviceauth.Principal, runID string, generation int64, artifactID, name, storageKey, mimeType string, stored artifacts.Stored, retentionUntil *time.Time) (Artifact, bool, error) {
	if !membership.ValidateUUID(runID) || !membership.ValidateUUID(artifactID) || !membership.ValidateUUID(principal.DeviceID) {
		return Artifact{}, false, ErrInvalidRequest
	}
	name = CleanArtifactName(name)
	if utf8.RuneCountInString(name) > maxArtifactName || stored.Size < 0 || stored.Size > artifacts.MaxArtifactBytes || len(stored.SHA256) != 64 {
		return Artifact{}, false, ErrInvalidRequest
	}
	mimeType = NormalizeArtifactMIME(mimeType)
	run, err := lockArtifactRun(ctx, tx, runID, principal.DeviceID, generation)
	if err != nil {
		return Artifact{}, false, err
	}
	sha := strings.ToLower(stored.SHA256)
	// 同一 Run 内容相同的产物只登记一次（上传重试或 Agent 重复发布）。
	existing, err := scanArtifact(tx.QueryRow(ctx, `
		SELECT `+artifactColumns+`
		  FROM openfox.artifacts
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND run_id = $1::uuid AND sha256 = $2 AND state = 'AVAILABLE'
		 ORDER BY created_at, id
		 LIMIT 1`, runID, sha))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Artifact{}, false, err
	}
	config, err := s.Resources.GetConfig(ctx, tx, organizationID)
	if err != nil {
		return Artifact{}, false, err
	}
	if stored.Size > config.Policy.ArtifactFileMaxBytes {
		return Artifact{}, false, ErrResourceLimit
	}
	var currentBytes int64
	var currentCount int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(sum(size_bytes), 0), count(*)
		  FROM openfox.artifacts
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND run_id = $1::uuid AND state <> 'DELETED'`, runID).Scan(&currentBytes, &currentCount); err != nil {
		return Artifact{}, false, err
	}
	if currentBytes > config.Policy.ArtifactRunMaxBytes-stored.Size || currentCount >= MaxRunArtifacts {
		return Artifact{}, false, ErrResourceLimit
	}
	result, err := scanArtifact(tx.QueryRow(ctx, `
		INSERT INTO openfox.artifacts (
			organization_id, id, run_id, name, storage_key, size_bytes, sha256, mime_type, retention_until
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8)
		RETURNING `+artifactColumns, artifactID,
		runID, name, storageKey, stored.Size, sha, mimeType, retentionUntil))
	if err != nil {
		return Artifact{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.organization_events (organization_id, type, entity, entity_id, space_id, conversation_id, payload)
		VALUES (openfox_private.current_organization_id(), 'run.artifact', 'artifact', $1::uuid, $2::uuid, $3::uuid,
		        jsonb_build_object('run_id', $4::text))`, result.ID, run.spaceID, run.conversationID, runID); err != nil {
		return Artifact{}, false, err
	}
	return result, true, nil
}

// ListConversationArtifacts 返回会话内所有 Run 的产物：能读会话即可读，读不到（含跨组织）一律 ErrNotFound；
// 一次查询取齐（产物经 run 关联到会话），created_at 倒序，最多 MaxConversationArtifacts 个。
func (s Service) ListConversationArtifacts(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor conversations.Actor) ([]Artifact, error) {
	if !membership.ValidateUUID(conversationID) {
		return nil, ErrNotFound
	}
	if _, err := (conversations.Service{}).Get(ctx, tx, organizationID, conversationID, actor); err != nil {
		if errors.Is(err, conversations.ErrForbidden) || errors.Is(err, conversations.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT a.organization_id::text, a.id::text, a.run_id::text, COALESCE(a.result_id::text, ''),
		       a.name, a.size_bytes, a.sha256, a.mime_type, a.state, a.retention_until, a.pinned, a.version, a.created_at
		  FROM openfox.artifacts a
		  JOIN openfox.runs r ON r.organization_id = a.organization_id AND r.id = a.run_id
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND r.conversation_id = $1::uuid
		 ORDER BY a.created_at DESC, a.id DESC
		 LIMIT $2`, conversationID, MaxConversationArtifacts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Artifact, 0)
	for rows.Next() {
		item, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const artifactColumns = `organization_id::text, id::text, run_id::text, COALESCE(result_id::text, ''),
		       name, size_bytes, sha256, mime_type, state, retention_until, pinned, version, created_at`

func scanArtifact(row pgx.Row) (Artifact, error) {
	var item Artifact
	err := row.Scan(&item.OrganizationID, &item.ID, &item.RunID, &item.ResultID,
		&item.Name, &item.SizeBytes, &item.SHA256, &item.MIMEType, &item.State,
		&item.RetentionUntil, &item.Pinned, &item.Version, &item.CreatedAt)
	return item, err
}

type artifactRun struct {
	spaceID        string
	conversationID string
}

// lockArtifactRun 锁定 Run 行并校验产物上传的前提：执行电脑、代次一致、未结束。
func lockArtifactRun(ctx context.Context, tx pgx.Tx, runID, deviceID string, generation int64) (artifactRun, error) {
	var computerID, state string
	var current int64
	var run artifactRun
	err := tx.QueryRow(ctx, `
		SELECT computer_id::text, generation, state, space_id::text, conversation_id::text
		  FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		 FOR UPDATE`, runID).Scan(&computerID, &current, &state, &run.spaceID, &run.conversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactRun{}, ErrNotFound
	}
	if err != nil {
		return artifactRun{}, err
	}
	if computerID != deviceID {
		return artifactRun{}, ErrForbidden
	}
	if generation != current {
		return artifactRun{}, ErrGenerationConflict
	}
	if execution.State(state).Terminal() {
		return artifactRun{}, ErrResultConflict
	}
	return run, nil
}

// previewMIMETypes 是产物的可预览白名单；其余类型只能下载，一律记为 application/octet-stream。
var previewMIMETypes = map[string]struct{}{
	"text/plain": {}, "text/markdown": {}, "text/csv": {},
	"image/png": {}, "image/jpeg": {}, "image/gif": {}, "image/webp": {},
}

// NormalizeArtifactMIME 去掉参数、转小写；不在可预览白名单内的类型（包括 SVG、HTML）返回 application/octet-stream。
func NormalizeArtifactMIME(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if i := strings.IndexByte(value, ';'); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	if _, ok := previewMIMETypes[value]; ok {
		return value
	}
	return "application/octet-stream"
}

// CleanArtifactName 只保留最后一个路径成分（/ 与 \ 都算分隔符），控制字符、格式字符（如双向覆盖符）与非法 UTF-8 替换为下划线；
// 结果为空、"." 或 ".." 时返回 artifact.bin。
func CleanArtifactName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			return '_'
		}
		return r
	}, strings.ToValidUTF8(name, "_")))
	if name == "" || name == "." || name == ".." {
		return "artifact.bin"
	}
	return name
}
