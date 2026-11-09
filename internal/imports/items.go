package imports

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/imports/jiraapi"
)

const (
	maxTitleRunes       = 500
	maxDescriptionRunes = 10000
	maxCommentsPerTask  = 200
)

type pendingItem struct {
	Ordinal    int
	ExternalID string
	Cells      []string
	Record     *Record
}

func (it pendingItem) data(p plan) rowData {
	if it.Record != nil {
		return rowFromRecord(it.Record)
	}
	return p.row(it.Cells)
}

type itemResult struct {
	status, action    string
	taskID, projectID string
	version           int64
	errText           string
}

// importBatch 处理一批 PENDING 条目（逐条在 SAVEPOINT 内）；没有剩余条目时返回 done=true。
func (w *Worker) importBatch(ctx context.Context, tx pgx.Tx, b *batchCtx) (bool, error) {
	if b.plan.project >= 0 || len(b.plan.custom) > 0 {
		// Jobs for different spaces can resolve the same organization-wide field.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "import-columns:"+b.orgID); err != nil {
			return false, err
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT ordinal, external_id, raw FROM openfox.import_items
		 WHERE organization_id = openfox_private.current_organization_id() AND job_id = $1::uuid AND status = 'PENDING'
		 ORDER BY ordinal LIMIT $2 FOR UPDATE`, b.job.ID, batchSize)
	if err != nil {
		return false, err
	}
	var items []pendingItem
	for rows.Next() {
		var it pendingItem
		var raw []byte
		if err := rows.Scan(&it.Ordinal, &it.ExternalID, &raw); err != nil {
			rows.Close()
			return false, err
		}
		var doc struct {
			Cells  []string `json:"cells"`
			Record *Record  `json:"record"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			rows.Close()
			return false, err
		}
		it.Cells, it.Record = doc.Cells, doc.Record
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(items) == 0 {
		return true, nil
	}
	for _, it := range items {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return false, err
		}
		warnsBefore := cloneWarn(b.warns)
		res, err := w.processItem(ctx, sp, b, it)
		if err == nil && (res.status == "CREATED" || res.status == "UPDATED") && res.taskID != "" && it.Record == nil {
			err = writeFileColumns(ctx, sp, b, it.Cells, res.taskID)
		}
		if err == nil && (res.status == "CREATED" || res.status == "UPDATED") && res.taskID != "" && it.Record != nil {
			err = attachRecordProject(ctx, sp, b, it.data(b.plan), res.taskID)
		}
		if err == nil && b.job.Cfg.Options.ContinuousSync && res.taskID != "" {
			d := it.data(b.plan)
			err = recordSyncItem(ctx, sp, b.job.ID, it.ExternalID, res.taskID, d.Title, d.Description, d.Status, b.resolved.Statuses[strings.ToLower(d.Status)], nil)
			if err == nil && it.Record != nil {
				for index, id := range it.Record.CommentIDs {
					if id == "" || index >= len(it.Record.Comments) {
						continue
					}
					pc := ParseCommentCell(it.Record.Comments[index], b.settings.location)
					if strings.TrimSpace(pc.Body) == "" {
						continue
					}
					_, err = applySourceComment(ctx, sp, &syncJob{batchCtx: *b}, it.ExternalID, res.taskID, sourceComment{id: id, author: pc.Author, body: pc.Body, created: pc.At.Format(time.RFC3339Nano)})
					if err != nil {
						break
					}
				}
			}
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			b.warns = warnsBefore
			res = itemResult{status: "FAILED", action: "failed", errText: itemErrorText(err)}
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
		} else if err := sp.Commit(ctx); err != nil {
			return false, err
		}
		switch res.status {
		case "CREATED":
			b.delta.created++
		case "UPDATED":
			b.delta.updated++
		case "SKIPPED":
			b.delta.skipped++
		default:
			b.delta.failed++
		}
		var versionArg any
		var taskArg any
		if res.taskID != "" {
			taskArg, versionArg = res.taskID, res.version
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.import_items SET status = $3, action = $4, task_id = $5::uuid, task_version = $6, project_id=nullif($8,'')::uuid, error = left($7, 1000),
			       processed_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND job_id = $1::uuid AND ordinal = $2`,
			b.job.ID, it.Ordinal, res.status, res.action, taskArg, versionArg, res.errText, res.projectID); err != nil {
			return false, err
		}
	}
	return false, nil
}

func cloneWarn(a *warnAcc) *warnAcc {
	c := &warnAcc{idx: map[string]int{}}
	c.list = append(c.list, a.list...)
	for k, v := range a.idx {
		c.idx[k] = v
	}
	return c
}

func dateOnly(t time.Time, loc *time.Location) string { return t.In(loc).Format("2006-01-02") }

// buildInput 把一行转成写入任务的输入，并记录降级告警。
func (w *Worker) buildInput(ctx context.Context, tx pgx.Tx, b *batchCtx, it pendingItem) (collaboration.ImportTaskInput, error) {
	d := it.data(b.plan)
	loc := b.settings.location
	in := collaboration.ImportTaskInput{SourceKey: d.SourceKey, Source: sourceSystem(b.job.Source)}
	// 保留票号：前缀与票号前缀一致时沿用来源序号。
	if b.job.Cfg.Options.KeepNumbers && b.resolved.KeepPrefix != "" {
		if prefix, number, ok := ParseSourceKey(d.SourceKey); ok && strings.EqualFold(prefix, b.resolved.KeepPrefix) {
			in.Sequence = number
		}
	}
	if b.job.Cfg.Options.ImportSprints {
		in.IterationID = pickIteration(d.Sprints, b.resolved.Iterations, loc)
	}

	title, cut := TruncateRunes(d.Title, maxTitleRunes)
	if cut {
		b.warns.add("title_truncated", "标题超过 500 字，已截断")
	}
	in.Title = title
	desc, cut := TruncateRunes(d.Description, maxDescriptionRunes)
	if cut {
		b.warns.add("description_truncated", "描述超过 10000 字，已截断")
	}
	in.Description = desc

	priority, ok := MapPriority(d.Priority, b.settings.priorityLevels)
	if !ok {
		b.warns.add("priority_unmapped", "优先级「"+d.Priority+"」无法识别，已设为无优先级")
	}
	in.Priority = priority

	if d.Status != "" {
		id, ok := b.resolved.Statuses[strings.ToLower(d.Status)]
		if !ok {
			return in, errors.New("来源状态没有解析结果")
		}
		in.StatusID = id
	}
	if d.TypeName != "" {
		in.TypeID = b.resolved.Types[strings.ToLower(d.TypeName)]
	}

	if d.Created != "" {
		if t, ok := ParseDate(d.Created, loc); ok {
			in.CreatedAt = t
		} else {
			b.warns.add("date_unparsed", "无法解析日期「"+d.Created+"」，已留空或使用当前时间")
		}
	}
	if d.Start != "" {
		if t, ok := ParseDate(d.Start, loc); ok {
			in.StartDate = dateOnly(t, loc)
		} else {
			b.warns.add("date_unparsed", "无法解析日期「"+d.Start+"」，已留空或使用当前时间")
		}
	}
	if d.Due != "" {
		if t, ok := ParseDate(d.Due, loc); ok {
			in.DueDate = dateOnly(t, loc)
		} else {
			b.warns.add("date_unparsed", "无法解析日期「"+d.Due+"」，已留空或使用当前时间")
		}
	}
	if in.StartDate != "" && in.DueDate != "" && in.StartDate > in.DueDate {
		in.StartDate = ""
		b.warns.add("date_range_invalid", "开始日期晚于到期日，已清空开始日期")
	}

	if d.Points != "" {
		if v, ok := ParseStoryPoints(d.Points); !ok {
			b.warns.add("estimate_invalid", "故事点「"+d.Points+"」不是合法数值，已忽略")
		} else if err := b.collab.CheckEstimate(ctx, tx, b.job.SpaceID, v); err != nil {
			if errors.Is(err, collaboration.ErrEstimationDisabled) {
				b.warns.add("estimate_unavailable", "目标空间未启用估点，故事点已忽略")
			} else {
				b.warns.add("estimate_invalid", "故事点「"+d.Points+"」不符合空间的估点刻度，已忽略")
			}
		} else {
			in.Estimate = &v
		}
	}

	if d.Assignee.ID != "" {
		if r, ok := b.resolved.Users[d.Assignee.ID]; ok {
			switch {
			case r.UserID != "":
				if b.collab.ImportAssignable(ctx, tx, b.job.SpaceID, b.actor, r.UserID) {
					in.AssigneeUserID = r.UserID
				} else {
					b.warns.add("assignee_unavailable", "映射的成员无法访问目标空间，已不设处理人")
				}
			case r.ExternalAccountID != "":
				in.ExternalAssigneeID = r.ExternalAccountID
			}
		}
	}

	labels, tooLong := SplitLabels(d.LabelCells, maxLabelRunes)
	if tooLong > 0 {
		for i := 0; i < tooLong; i++ {
			b.warns.add("label_too_long", "标签超过 50 字，已忽略")
		}
	}
	for _, name := range labels {
		if id := b.resolved.Labels[strings.ToLower(name)]; id != "" {
			in.LabelIDs = append(in.LabelIDs, id)
		}
	}
	if b.resolved.MigratedLabel != "" {
		in.LabelIDs = append(in.LabelIDs, b.resolved.MigratedLabel)
	}

	for commentIndex, cell := range d.CommentCells {
		if len(in.Comments) >= maxCommentsPerTask {
			b.warns.add("comments_truncated", "单个任务的评论超过 200 条，多出的已忽略")
			break
		}
		pc := ParseCommentCell(cell, loc)
		if strings.TrimSpace(pc.Body) == "" {
			continue
		}
		author, name := "", pc.Author
		mapped := false
		if r, ok := b.resolved.Users[pc.Author]; ok && pc.Author != "" {
			if r.UserID != "" {
				author, mapped = r.UserID, true
			}
			if r.Name != "" {
				name = r.Name
			}
		}
		clientID := collaboration.ImportCommentClientID("jira:"+it.ExternalID, pc.At, pc.Author, pc.Body)
		if it.Record != nil && commentIndex < len(it.Record.CommentIDs) && it.Record.CommentIDs[commentIndex] != "" {
			clientID = sourceCommentClientID(b.job.Preview.Origin.Instance, it.ExternalID, it.Record.CommentIDs[commentIndex])
		}
		in.Comments = append(in.Comments, collaboration.ImportComment{
			AuthorUserID: author,
			Body:         collaboration.ImportCommentBody(mapped, name, pc.At, pc.HasAt, pc.Body),
			CreatedAt:    pc.At,
			ClientID:     clientID,
		})
	}
	return in, nil
}

// processItem 处理一条：按映射决定跳过 / 覆盖 / 创建。在调用方给的 SAVEPOINT 事务内执行。
func (w *Worker) processItem(ctx context.Context, tx pgx.Tx, b *batchCtx, it pendingItem) (itemResult, error) {
	if (b.plan.jiraFile || b.plan.apiProjects) && b.job.Cfg.Options.FileProjects != nil && strings.EqualFold(it.data(b.plan).TypeName, "Epic") {
		epic := b.resolved.Epics[it.ExternalID]
		if epic.ID == "" {
			return itemResult{}, ErrConfigIncomplete
		}
		if epic.Created {
			if b.job.Cfg.Options.ContinuousSync && b.plan.apiProjects {
				if err := recordSyncProject(ctx, tx, b.job.ID, it.data(b.plan), epic.ID); err != nil {
					return itemResult{}, err
				}
			}
			return itemResult{status: "CREATED", action: "created", projectID: epic.ID}, nil
		}
		return itemResult{status: "SKIPPED", action: "skipped_existing", projectID: epic.ID}, nil
	}
	var mappingID, mappedTask, instance string
	var deleted bool
	err := tx.QueryRow(ctx, `
		SELECT m.id::text, m.task_id::text, m.source_instance, t.deleted_at IS NOT NULL OR t.space_id <> m.space_id
		  FROM openfox.import_mappings m
		  JOIN openfox.tasks t ON t.organization_id = m.organization_id AND t.id = m.task_id
		 WHERE m.organization_id = openfox_private.current_organization_id() AND m.space_id = $1::uuid AND m.source_system = $2
		   AND (lower(m.source_instance) = $4 OR m.source_instance = '') AND m.external_id = $3 AND m.released_at IS NULL
         ORDER BY (lower(m.source_instance) = $4) DESC LIMIT 1`,
		b.job.SpaceID, sourceSystem(b.job.Source), it.ExternalID, b.job.Preview.Origin.Instance).Scan(&mappingID, &mappedTask, &instance, &deleted)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return itemResult{}, err
	}
	if found && b.job.Cfg.Options.ContinuousSync && instance == "" {
		// Historical and file imports do not prove which project owns this ID.
		return itemResult{status: "SKIPPED", action: "skipped_existing"}, nil
	}
	if found && deleted {
		return itemResult{status: "SKIPPED", action: "skipped_deleted"}, nil
	}
	if found && !b.job.Cfg.Options.Overwrite {
		return itemResult{status: "SKIPPED", action: "skipped_existing"}, nil
	}
	in, err := w.buildInput(ctx, tx, b, it)
	if err != nil {
		return itemResult{}, err
	}
	if found {
		version, err := b.collab.ImportUpdateTask(ctx, tx, b.orgID, b.job.SpaceID, b.actor, mappedTask, in)
		if err != nil {
			return itemResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.import_mappings SET last_job_id = $2::uuid, external_key = $3, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			mappingID, b.job.ID, truncateBytes(in.SourceKey, 255)); err != nil {
			return itemResult{}, err
		}
		return itemResult{status: "UPDATED", action: "updated", taskID: mappedTask, version: version}, nil
	}
	taskID, version, conflict, err := b.collab.ImportCreateTaskEx(ctx, tx, b.orgID, b.job.SpaceID, b.actor, in)
	if err != nil {
		return itemResult{}, err
	}
	if conflict {
		b.warns.add("keep_number_conflict", "来源票号已被占用，已改用自动编号")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.import_mappings (organization_id, space_id, source_system, source_instance, external_id, external_key,
		                                     task_id, created_job_id, last_job_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $7, $3, $4, $5::uuid, $6::uuid, $6::uuid)`,
		b.job.SpaceID, sourceSystem(b.job.Source), it.ExternalID, truncateBytes(in.SourceKey, 255), taskID, b.job.ID, b.job.Preview.Origin.Instance); err != nil {
		return itemResult{}, err
	}
	return itemResult{status: "CREATED", action: "created", taskID: taskID, version: version}, nil
}

// linkBatch 关联父子：对本 job 中 CREATED / UPDATED 且有父编号的条目，用映射找父任务并设置 parent_task_id。
func (w *Worker) linkBatch(ctx context.Context, tx pgx.Tx, b *batchCtx) (bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT ordinal, task_id::text, parent_external_id FROM openfox.import_items
		 WHERE organization_id = openfox_private.current_organization_id() AND job_id = $1::uuid
		   AND task_id IS NOT NULL AND status IN ('CREATED', 'UPDATED') AND parent_external_id <> '' AND NOT (raw ? 'linked')
		 ORDER BY ordinal LIMIT $2 FOR UPDATE`, b.job.ID, linkBatchSize)
	if err != nil {
		return false, err
	}
	type linkItem struct {
		ordinal  int
		task     string
		parentID string
	}
	var items []linkItem
	for rows.Next() {
		var it linkItem
		if err := rows.Scan(&it.ordinal, &it.task, &it.parentID); err != nil {
			rows.Close()
			return false, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(items) == 0 {
		return true, nil
	}
	for _, it := range items {
		if _, ok := b.resolved.Epics[it.parentID]; ok {
			if _, err := tx.Exec(ctx, `UPDATE openfox.import_items SET raw=raw || '{"linked":true}'::jsonb WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND ordinal=$2`, b.job.ID, it.ordinal); err != nil {
				return false, err
			}
			continue
		}
		var parentTask, currentParent string
		err := tx.QueryRow(ctx, `
			SELECT m.task_id::text, COALESCE(c.parent_task_id::text, '')
			  FROM openfox.import_mappings m
			  JOIN openfox.tasks p ON p.organization_id = m.organization_id AND p.id = m.task_id AND p.deleted_at IS NULL
			  JOIN openfox.tasks c ON c.organization_id = m.organization_id AND c.id = $4::uuid
			 WHERE m.organization_id = openfox_private.current_organization_id() AND m.space_id = $1::uuid AND m.source_system = $2
			   AND (lower(m.source_instance) = $5 OR (NOT $6 AND m.source_instance='')) AND m.external_id = $3 AND m.released_at IS NULL AND p.space_id = $1::uuid
             ORDER BY (lower(m.source_instance)=$5) DESC LIMIT 1`,
			b.job.SpaceID, sourceSystem(b.job.Source), it.parentID, it.task, b.job.Preview.Origin.Instance, b.job.Cfg.Options.ContinuousSync).Scan(&parentTask, &currentParent)
		if errors.Is(err, pgx.ErrNoRows) {
			b.warns.add("parent_unresolved", "找不到父任务「"+it.parentID+"」，已保留为顶层任务")
		} else if err != nil {
			return false, err
		} else if currentParent != parentTask {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return false, err
			}
			version, err := b.collab.ImportSetParent(ctx, sp, b.orgID, b.job.SpaceID, it.task, parentTask, b.actor)
			if err != nil {
				_ = sp.Rollback(ctx)
				b.warns.add("parent_unresolved", "无法设置父任务「"+it.parentID+"」（层级过深或成环），已保留为顶层任务")
			} else {
				if err := sp.Commit(ctx); err != nil {
					return false, err
				}
				if _, err := tx.Exec(ctx, `
					UPDATE openfox.import_items SET task_version = $3
					 WHERE organization_id = openfox_private.current_organization_id() AND job_id = $1::uuid AND ordinal = $2`,
					b.job.ID, it.ordinal, version); err != nil {
					return false, err
				}
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.import_items SET raw = raw || '{"linked":true}'::jsonb
			 WHERE organization_id = openfox_private.current_organization_id() AND job_id = $1::uuid AND ordinal = $2`,
			b.job.ID, it.ordinal); err != nil {
			return false, err
		}
	}
	return false, nil
}

// pickIteration 选任务归属的迭代：优先进行中的 Sprint，其次开始时间最晚的；没有映射到迭代的返回空串。
func pickIteration(sprints []jiraapi.Sprint, mapped map[string]string, loc *time.Location) string {
	best, bestActive := "", false
	var bestStart time.Time
	for _, sp := range sprints {
		id := mapped[sp.ID]
		if id == "" {
			continue
		}
		active := sp.State == "ACTIVE"
		start, _ := ParseDate(sp.Start, loc)
		if best == "" || (active && !bestActive) || (active == bestActive && start.After(bestStart)) {
			best, bestActive, bestStart = id, active, start
		}
	}
	return best
}
