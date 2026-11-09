package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/spacebundle"
)

const maxCloneCommentBytes = 65000

func (b *cloneBatchCtx) state() *cloneState { return &b.job.Doc.State }

func (b *cloneBatchCtx) sourceKey(t spacebundle.Task) string {
	return fmt.Sprintf("%s-%d", b.job.Doc.TargetPrefix, t.Sequence)
}

// cloneTaskMap 把来源任务 ID 映射为目标任务 ID（import_mappings，仅限本次克隆创建的空间）。
func (w *Worker) cloneTaskMap(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT external_id, task_id::text FROM openfox.import_mappings
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		   AND source_system = $2 AND source_instance = '' AND released_at IS NULL AND external_id = ANY($3::text[])`,
		b.job.SpaceID, CloneSourceSystem, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ext, task string
		if err := rows.Scan(&ext, &task); err != nil {
			return nil, err
		}
		out[ext] = task
	}
	return out, rows.Err()
}

func (w *Worker) cloneActivity(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx, taskID, kind string, diff map[string]any, at time.Time, sourceID string) error {
	payload, err := json.Marshal(diff)
	if err != nil {
		return err
	}
	var created any
	if !at.IsZero() {
		created = at
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.task_activities (organization_id, space_id, task_id, actor_id, kind, safe_diff, source_id, created_at)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5::jsonb, $6, COALESCE($7::timestamptz, clock_timestamp()))
		ON CONFLICT DO NOTHING`, b.job.SpaceID, taskID, b.actor.UserID, kind, payload, sourceID, created)
	return err
}

// cloneImportTasks 导入一批任务（逐条 SAVEPOINT，单条失败只记失败项）。
func (w *Worker) cloneImportTasks(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx) error {
	st := b.state()
	raws, err := spacebundle.Select(w.bundleOpener(b.job.BundleKey), spacebundle.KindTask, st.Cursor, cloneTaskBatch)
	if err != nil {
		return err
	}
	tasks, err := spacebundle.Decode[spacebundle.Task](raws)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		b.nextPhase, st.Stage, st.Cursor = "linking", "parents", 0
		return nil
	}
	for i, t := range tasks {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		warnsBefore := cloneWarn(b.warns)
		err = w.cloneTask(ctx, sp, b, t)
		if err != nil {
			_ = sp.Rollback(ctx)
			b.warns = warnsBefore
			if ctx.Err() != nil {
				return ctx.Err()
			}
			b.delta.failed++
			if _, err := tx.Exec(ctx, `
				INSERT INTO openfox.import_items (organization_id, job_id, ordinal, external_id, external_key, raw, status, action, error, processed_at)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, '{}'::jsonb, 'FAILED', 'failed', left($5, 1000), clock_timestamp())`,
				b.job.ID, st.Cursor+i, truncateBytes(t.ID, 255), truncateBytes(b.sourceKey(t), 255), itemErrorText(err)); err != nil {
				return err
			}
			continue
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		b.delta.created++
		if t.Sequence > st.MaxSequence {
			st.MaxSequence = t.Sequence
		}
	}
	st.Cursor += len(tasks)
	return nil
}

func (w *Worker) cloneTask(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx, t spacebundle.Task) error {
	collab := collaboration.Service{}
	res := b.state().Resolved
	if res == nil {
		return errBadBundle("克隆缺少解析结果")
	}
	in := collaboration.ImportTaskInput{
		Title: t.Title, Description: t.Description, Priority: t.Priority, TypeID: res.Types[t.TypeID], StatusID: res.Statuses[t.StatusID],
		CreatedAt: t.CreatedAt, StartDate: t.StartDate, DueDate: t.DueDate, SourceKey: b.sourceKey(t), Sequence: t.Sequence, Source: "space_clone",
	}
	if in.TypeID == "" || in.StatusID == "" {
		return errors.New("任务的类型或状态没有对应的目标配置")
	}
	if in.StartDate != "" && in.DueDate != "" && in.StartDate > in.DueDate {
		in.StartDate = ""
		b.warns.add("date_range_invalid", "开始日期晚于到期日，已清空开始日期")
	}
	if t.Estimate != nil {
		if err := collab.CheckEstimate(ctx, tx, b.job.SpaceID, *t.Estimate); err != nil {
			b.warns.add("estimate_dropped", "任务的估点不符合空间的估点刻度，已忽略")
		} else {
			in.Estimate = t.Estimate
		}
	}
	for _, id := range t.LabelIDs {
		if target := res.Labels[id]; target != "" {
			in.LabelIDs = append(in.LabelIDs, target)
		}
	}
	var extraUsers, agentNames []string
	externalID := ""
	for _, a := range t.Assignees {
		if a.Kind != "HUMAN" {
			name := a.Name
			if name == "" {
				name = a.Kind
			}
			agentNames = append(agentNames, name)
			continue
		}
		p := res.People[a.PersonKey]
		switch {
		case p.UserID != "" && in.AssigneeUserID == "":
			in.AssigneeUserID = p.UserID
		case p.UserID != "":
			extraUsers = append(extraUsers, p.UserID)
		case p.ExternalID != "" && externalID == "":
			externalID = p.ExternalID
		}
	}
	if in.AssigneeUserID == "" {
		if externalID == "" && t.External != "" {
			externalID = res.People[t.External].ExternalID
		}
		in.ExternalAssigneeID = externalID
	}
	taskID, _, conflict, err := collab.ImportCreateTaskEx(ctx, tx, b.orgID, b.job.SpaceID, b.actor, in)
	if err != nil {
		return err
	}
	if conflict {
		b.warns.add("keep_number_conflict", "来源票号已被占用，已改用自动编号")
	}
	var project any
	if t.ProjectID != "" {
		if id := res.Projects[t.ProjectID]; id != "" {
			project = id
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.tasks SET acceptance = $3, project_id = $4::uuid, updated_at = $5
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid`,
		b.job.SpaceID, taskID, t.Acceptance, project, t.UpdatedAt); err != nil {
		return err
	}
	for _, userID := range extraUsers {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_assignees (organization_id, space_id, task_id, kind, user_id, created_by)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, 'HUMAN', $3::uuid, $4::uuid)
			ON CONFLICT DO NOTHING`, b.job.SpaceID, taskID, userID, b.actor.UserID); err != nil {
			return err
		}
	}
	for _, key := range t.Followers {
		if userID := res.People[key].UserID; userID != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO openfox.task_followers (organization_id, space_id, task_id, kind, user_id, created_by)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, 'HUMAN', $3::uuid, $4::uuid)
				ON CONFLICT DO NOTHING`, b.job.SpaceID, taskID, userID, b.actor.UserID); err != nil {
				return err
			}
		}
	}
	for fieldID, raw := range t.Fields {
		value, ok := cloneFieldValue(res.Fields[fieldID], raw)
		if !ok {
			b.warns.add("field_value_dropped", "部分自定义字段的取值（成员字段或目标组织没有对应选项）未能保留")
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_field_values (organization_id, space_id, task_id, field_id, value, updated_by)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4::jsonb, $5::uuid)
			ON CONFLICT DO NOTHING`, b.job.SpaceID, taskID, res.Fields[fieldID].ID, value, b.actor.UserID); err != nil {
			return err
		}
	}
	if len(agentNames) > 0 {
		if err := w.cloneActivity(ctx, tx, b, taskID, "task.clone_agent_removed",
			map[string]any{"agents": agentNames}, time.Time{}, "clone:"+b.job.ID+":agent:"+t.ID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.import_mappings (organization_id, space_id, source_system, source_instance, external_id, external_key,
		                                     task_id, created_job_id, last_job_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, '', $3, $4, $5::uuid, $6::uuid, $6::uuid)`,
		b.job.SpaceID, CloneSourceSystem, t.ID, truncateBytes(in.SourceKey, 255), taskID, b.job.ID)
	return err
}

// cloneFieldValue 把来源取值转成目标字段的取值；成员字段与找不到对应选项的取值不保留。
func cloneFieldValue(f cloneField, raw json.RawMessage) ([]byte, bool) {
	if f.ID == "" {
		return nil, false
	}
	switch f.Kind {
	case "MEMBER":
		return nil, false
	case "SELECT":
		var id string
		if json.Unmarshal(raw, &id) != nil || f.Options[id] == "" {
			return nil, false
		}
		out, _ := json.Marshal(f.Options[id])
		return out, true
	case "MULTI_SELECT":
		var ids []string
		if json.Unmarshal(raw, &ids) != nil {
			return nil, false
		}
		mapped := make([]string, 0, len(ids))
		for _, id := range ids {
			if target := f.Options[id]; target != "" {
				mapped = append(mapped, target)
			}
		}
		if len(mapped) == 0 {
			return nil, false
		}
		out, _ := json.Marshal(mapped)
		return out, true
	default:
		return []byte(raw), true
	}
}

// cloneLink 先设置父子关系（parents），再写任务关联（relations）。
func (w *Worker) cloneLink(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx) error {
	st := b.state()
	collab := collaboration.Service{}
	if st.Stage == "relations" {
		raws, err := spacebundle.Select(w.bundleOpener(b.job.BundleKey), spacebundle.KindRelation, st.Cursor, cloneLinkBatch)
		if err != nil {
			return err
		}
		rels, err := spacebundle.Decode[spacebundle.Relation](raws)
		if err != nil {
			return err
		}
		if len(rels) == 0 {
			b.nextPhase, st.Stage, st.Cursor = "comments", "comments", 0
			return nil
		}
		var ids []string
		for _, r := range rels {
			ids = append(ids, r.SourceTaskID, r.TargetTaskID)
		}
		m, err := w.cloneTaskMap(ctx, tx, b, ids)
		if err != nil {
			return err
		}
		for _, r := range rels {
			src, dst := m[r.SourceTaskID], m[r.TargetTaskID]
			relType := ""
			if r.TypeID != "" {
				relType = st.Resolved.RelTypes[r.TypeID]
			}
			if src == "" || dst == "" || (r.Builtin == "" && relType == "") {
				b.warns.add("relation_dropped", "部分任务关联因任务未能迁移而未保留")
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO openfox.task_relations (organization_id, source_space_id, source_task_id, target_space_id, target_task_id,
				                                    builtin_type, relation_type_id, created_by)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $1::uuid, $3::uuid, NULLIF($4, ''), NULLIF($5, '')::uuid, $6::uuid)
				ON CONFLICT DO NOTHING`, b.job.SpaceID, src, dst, r.Builtin, relType, b.actor.UserID); err != nil {
				return err
			}
		}
		st.Cursor += len(rels)
		return nil
	}
	raws, err := spacebundle.Select(w.bundleOpener(b.job.BundleKey), spacebundle.KindTask, st.Cursor, cloneLinkBatch)
	if err != nil {
		return err
	}
	tasks, err := spacebundle.Decode[spacebundle.Task](raws)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		st.Stage, st.Cursor = "relations", 0
		return nil
	}
	var ids []string
	for _, t := range tasks {
		if t.ParentID != "" {
			ids = append(ids, t.ID, t.ParentID)
		}
	}
	m, err := w.cloneTaskMap(ctx, tx, b, ids)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.ParentID == "" {
			continue
		}
		child, parent := m[t.ID], m[t.ParentID]
		if child == "" || parent == "" {
			b.warns.add("parent_unresolved", "部分子任务的父任务未能迁移，已保留为顶层任务")
			continue
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := collab.ImportSetParent(ctx, sp, b.orgID, b.job.SpaceID, child, parent, b.actor); err != nil {
			_ = sp.Rollback(ctx)
			b.warns.add("parent_unresolved", "部分子任务无法设置父任务（层级过深或成环），已保留为顶层任务")
			continue
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
	}
	st.Cursor += len(tasks)
	return nil
}

func cloneCommentBody(name, orgName string, at time.Time, body string) string {
	return fmt.Sprintf("**%s**（来自 %s · %s UTC）\n\n%s", name, orgName, at.UTC().Format("2006-01-02 15:04"), body)
}

func cloneClientID(jobID, sourceID string) string {
	sum := sha256.Sum256([]byte(jobID + "\x00" + sourceID))
	return "clone:" + hex.EncodeToString(sum[:])[:40]
}

// cloneComments 先写评论（comments），再写只读动态历史（activities），最后收尾。finished=true 表示全部完成。
func (w *Worker) cloneComments(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx) (bool, error) {
	st := b.state()
	res := st.Resolved
	if st.Stage == "activities" {
		raws, err := spacebundle.Select(w.bundleOpener(b.job.BundleKey), spacebundle.KindActivity, st.Cursor, cloneActivityBatch)
		if err != nil {
			return false, err
		}
		acts, err := spacebundle.Decode[spacebundle.Activity](raws)
		if err != nil {
			return false, err
		}
		if len(acts) == 0 {
			return true, w.cloneFinish(ctx, tx, b)
		}
		ids := make([]string, 0, len(acts))
		for _, a := range acts {
			ids = append(ids, a.TaskID)
		}
		m, err := w.cloneTaskMap(ctx, tx, b, ids)
		if err != nil {
			return false, err
		}
		for i, a := range acts {
			taskID := m[a.TaskID]
			if taskID == "" {
				continue
			}
			name := res.People[a.ActorKey].Name
			if name == "" {
				name = "未知用户"
			}
			if err := w.cloneActivity(ctx, tx, b, taskID, "task.cloned_history",
				map[string]any{"source_kind": truncateBytes(a.Kind, 64), "actor_name": name},
				a.CreatedAt, fmt.Sprintf("clone:%s:%d", b.job.ID, st.Cursor+i)); err != nil {
				return false, err
			}
		}
		st.Cursor += len(acts)
		return false, nil
	}
	raws, err := spacebundle.Select(w.bundleOpener(b.job.BundleKey), spacebundle.KindComment, st.Cursor, cloneCommentBatch)
	if err != nil {
		return false, err
	}
	comments, err := spacebundle.Decode[spacebundle.Comment](raws)
	if err != nil {
		return false, err
	}
	if len(comments) == 0 {
		st.Stage, st.Cursor = "activities", 0
		return false, nil
	}
	ids := make([]string, 0, len(comments))
	for _, c := range comments {
		ids = append(ids, c.TaskID)
	}
	m, err := w.cloneTaskMap(ctx, tx, b, ids)
	if err != nil {
		return false, err
	}
	for _, c := range comments {
		taskID := m[c.TaskID]
		body := strings.TrimSpace(c.Body)
		if taskID == "" || body == "" {
			continue
		}
		author := b.actor.UserID
		p := res.People[c.AuthorKey]
		switch {
		case c.AgentName != "":
			body = cloneCommentBody(c.AgentName+"（Agent）", b.job.Doc.SourceOrganizationName, c.CreatedAt, body)
		case p.UserID != "":
			author = p.UserID
		default:
			name := p.Name
			if name == "" {
				name = "未知用户"
			}
			body = cloneCommentBody(name, b.job.Doc.SourceOrganizationName, c.CreatedAt, body)
		}
		if len(body) > maxCloneCommentBytes {
			b.warns.add("comment_too_long", "部分评论加上来源前缀后过长，未能保留")
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_comments (organization_id, space_id, task_id, author_user_id, body, client_comment_id, created_at)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, $6)
			ON CONFLICT DO NOTHING`, b.job.SpaceID, taskID, author, body, cloneClientID(b.job.ID, c.ID), c.CreatedAt); err != nil {
			return false, err
		}
	}
	st.Cursor += len(comments)
	return false, nil
}

// cloneFinish 收尾：推进空间的下一个票号、归档来源里已归档的任务类型。
func (w *Worker) cloneFinish(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx) error {
	st := b.state()
	next := st.NextNumber
	if st.MaxSequence+1 > next {
		next = st.MaxSequence + 1
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.spaces SET next_task_number = GREATEST(next_task_number, $2), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, b.job.SpaceID, next); err != nil {
		return err
	}
	if len(st.ArchiveTypes) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.task_types SET archived_at = clock_timestamp(), is_default = false, version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = ANY($2::uuid[])
			   AND NOT is_default`, b.job.SpaceID, st.ArchiveTypes); err != nil {
			return err
		}
	}
	return nil
}
