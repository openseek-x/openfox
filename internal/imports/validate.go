package imports

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
)

const maxLabelRunes = 50

// recordWorkflowResource 记录本次导入加入工作流的 (状态, 任务类型) 行：resource_id 为状态 ID，task_type_id 区分类型。
func recordWorkflowResource(ctx context.Context, tx pgx.Tx, jobID, statusID, spaceID, typeID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.import_created_resources (organization_id, job_id, kind, resource_id, space_id, task_type_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, 'WORKFLOW_STATUS', $2::uuid, $3::uuid, $4::uuid)
		ON CONFLICT DO NOTHING`, jobID, statusID, spaceID, typeID)
	return err
}

func recordResource(ctx context.Context, tx pgx.Tx, jobID, kind, resourceID string, spaceID, typeID *string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.import_created_resources (organization_id, job_id, kind, resource_id, space_id, task_type_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3::uuid, $4::uuid, $5::uuid)
		ON CONFLICT DO NOTHING`, jobID, kind, resourceID, spaceID, typeID)
	return err
}

// validate 是 validating 阶段（只执行一次）：解析状态 / 标签 / 类型 / 成员映射，写入 preview.resolved。
func (w *Worker) validate(ctx context.Context, tx pgx.Tx, b *batchCtx) error {
	rows, err := loadItems(ctx, tx, b.job.ID)
	if err != nil {
		return err
	}
	infos := classify(b.plan, rows, b.job.Cfg.Options)
	l, err := loadLookups(ctx, tx, b.job.SpaceID)
	if err != nil {
		return err
	}
	res := Resolved{Statuses: map[string]string{}, Labels: map[string]string{}, Types: map[string]string{}, Users: map[string]ResolvedUser{}}

	res.Epics, err = resolveFileEpics(ctx, tx, b, infos)
	if err != nil {
		return err
	}
	cfgStatus := map[string]StatusMapping{}
	for _, s := range b.job.Cfg.Statuses {
		cfgStatus[strings.ToLower(s.Source)] = s
	}
	labelNames := map[string]string{} // 小写 → 首次写法
	var labelOrder []string
	typeNames := map[string]bool{}
	var usedStatus []string
	usedSeen := map[string]bool{}
	for _, info := range infos {
		if info.Invalid || info.Filtered || info.Epic {
			continue
		}
		d := info.Data
		if d.Status != "" {
			k := strings.ToLower(d.Status)
			if _, done := res.Statuses[k]; !done {
				id, err := w.resolveStatus(ctx, tx, b, l, cfgStatus[k], k)
				if err != nil {
					return err
				}
				if id == "" {
					return withDetail(ErrConfigIncomplete, "来源状态「%s」没有映射", d.Status)
				}
				res.Statuses[k] = id
				if !usedSeen[id] {
					usedSeen[id] = true
					usedStatus = append(usedStatus, id)
				}
			}
		}
		labels, _ := SplitLabels(d.LabelCells, maxLabelRunes)
		for _, name := range labels {
			k := strings.ToLower(name)
			if _, ok := labelNames[k]; !ok {
				labelNames[k] = name
				labelOrder = append(labelOrder, k)
			}
		}
		if d.TypeName != "" {
			typeNames[strings.ToLower(d.TypeName)] = true
		}
	}
	if err := w.ensureWorkflow(ctx, tx, b, usedStatus); err != nil {
		return err
	}
	for _, k := range labelOrder {
		id, err := w.resolveLabel(ctx, tx, b, labelNames[k])
		if err != nil {
			return err
		}
		res.Labels[k] = id
	}
	if b.job.Cfg.Options.MigratedLabel {
		if res.MigratedLabel, err = w.resolveLabel(ctx, tx, b, "Migrated"); err != nil {
			return err
		}
	}
	for k := range typeNames {
		if id, ok := l.typeByName[k]; ok {
			res.Types[k] = id
		}
	}
	if err := w.resolveKeepNumbers(ctx, tx, b, infos, &res); err != nil {
		return err
	}
	if err := w.resolveSprints(ctx, tx, b, infos, &res); err != nil {
		return err
	}
	if err := w.resolveUsers(ctx, tx, b, infos, &res); err != nil {
		return err
	}
	b.resolved = res
	b.job.Preview.Resolved = &res
	return nil
}

// resolveStatus 返回来源状态对应的状态 ID：映射到已有状态 / 新建（同名复用）/ 同名已有状态。
func (w *Worker) resolveStatus(ctx context.Context, tx pgx.Tx, b *batchCtx, l lookups, m StatusMapping, key string) (string, error) {
	switch {
	case m.StatusID != "":
		return strings.ToLower(m.StatusID), nil
	case m.NewName != "":
		var id string
		var archived bool
		err := tx.QueryRow(ctx, `
			SELECT id::text, archived_at IS NOT NULL FROM openfox.task_statuses
			 WHERE organization_id = openfox_private.current_organization_id() AND lower(name) = lower($1)`, m.NewName).Scan(&id, &archived)
		switch {
		case err == nil && !archived:
			return id, nil
		case err == nil:
			if _, err := tx.Exec(ctx, `
				UPDATE openfox.task_statuses SET archived_at = NULL, version = version + 1, updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id); err != nil {
				return "", err
			}
			return id, recordResource(ctx, tx, b.job.ID, "STATUS", id, nil, nil)
		case err != pgx.ErrNoRows:
			return "", err
		}
		created, _, err := b.collab.CreateStatus(ctx, tx, b.orgID, b.actor, collaboration.TaskStatusInput{Name: m.NewName, Category: m.NewCategory})
		if err != nil {
			return "", err
		}
		if err := recordResource(ctx, tx, b.job.ID, "STATUS", created.ID, nil, nil); err != nil {
			return "", err
		}
		// CreateStatus 已把状态加入所有未归档类型的工作流：逐行记录，回滚时才能清掉。
		wr, err := tx.Query(ctx, `
			SELECT space_id::text, task_type_id::text FROM openfox.workflow_statuses
			 WHERE organization_id = openfox_private.current_organization_id() AND status_id = $1::uuid`, created.ID)
		if err != nil {
			return "", err
		}
		type pair struct{ space, typ string }
		var pairs []pair
		for wr.Next() {
			var p pair
			if err := wr.Scan(&p.space, &p.typ); err != nil {
				wr.Close()
				return "", err
			}
			pairs = append(pairs, p)
		}
		wr.Close()
		if err := wr.Err(); err != nil {
			return "", err
		}
		for _, p := range pairs {
			if err := recordWorkflowResource(ctx, tx, b.job.ID, created.ID, p.space, p.typ); err != nil {
				return "", err
			}
		}
		return created.ID, nil
	default:
		return l.statusByName[key], nil
	}
}

// ensureWorkflow 确保每个用到的状态都在空间各启用任务类型的工作流里，缺的追加到末尾并记录。
func (w *Worker) ensureWorkflow(ctx context.Context, tx pgx.Tx, b *batchCtx, statusIDs []string) error {
	for _, statusID := range statusIDs {
		rows, err := tx.Query(ctx, `
			INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default)
			SELECT tt.organization_id, tt.space_id, tt.id, $2::uuid,
			       COALESCE((SELECT max(ws.position) + 1 FROM openfox.workflow_statuses ws
			                  WHERE ws.organization_id = tt.organization_id AND ws.task_type_id = tt.id), 0), false
			  FROM openfox.task_types tt
			 WHERE tt.organization_id = openfox_private.current_organization_id() AND tt.space_id = $1::uuid
			   AND tt.enabled AND tt.archived_at IS NULL
			ON CONFLICT DO NOTHING
			RETURNING task_type_id::text`, b.job.SpaceID, statusID)
		if err != nil {
			return err
		}
		var types []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			types = append(types, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, typ := range types {
			if err := recordWorkflowResource(ctx, tx, b.job.ID, statusID, b.job.SpaceID, typ); err != nil {
				return err
			}
		}
		if len(types) > 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE openfox.task_types SET version = version + 1
				 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[])`, types); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveLabel 返回标签 ID：优先未归档的组织级 / 空间级同名标签；归档的同名标签恢复；都没有则创建空间级标签。
func (w *Worker) resolveLabel(ctx context.Context, tx pgx.Tx, b *batchCtx, name string) (string, error) {
	var id string
	var archived bool
	err := tx.QueryRow(ctx, `
		SELECT id::text, archived_at IS NOT NULL FROM openfox.labels
		 WHERE organization_id = openfox_private.current_organization_id() AND lower(name) = lower($1)
		   AND (space_id IS NULL OR space_id = $2::uuid)
		 ORDER BY (archived_at IS NOT NULL), (space_id IS NULL) DESC, created_at LIMIT 1`, name, b.job.SpaceID).Scan(&id, &archived)
	switch {
	case err == nil && !archived:
		return id, nil
	case err == nil:
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.labels SET archived_at = NULL, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id); err != nil {
			return "", err
		}
		return id, recordResource(ctx, tx, b.job.ID, "LABEL", id, nil, nil)
	case err != pgx.ErrNoRows:
		return "", err
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO openfox.labels (organization_id, space_id, name)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2) RETURNING id::text`, b.job.SpaceID, name).Scan(&id); err != nil {
		return "", err
	}
	return id, recordResource(ctx, tx, b.job.ID, "LABEL", id, nil, nil)
}

// resolveUsers 解析来源用户：成员 → user_id；外部账号（已关联成员的直接用成员）→ external_account_id；skip → 不导入。
func (w *Worker) resolveUsers(ctx context.Context, tx pgx.Tx, b *batchCtx, infos []rowInfo, res *Resolved) error {
	users := collectUsers(infos, b.settingsLocation())
	modes := map[string]UserMapping{}
	for _, u := range b.job.Cfg.Users {
		modes[u.SourceID] = u
	}
	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, strings.ToLower(u.SourceID))
	}
	type account struct{ id, linked string }
	accounts := map[string]account{}
	if len(ids) > 0 {
		rows, err := tx.Query(ctx, `
			SELECT lower(external_user_id), id::text, COALESCE(linked_user_id::text, '') FROM openfox.external_accounts
			 WHERE organization_id = openfox_private.current_organization_id() AND provider = $1 AND lower(external_user_id) = ANY($2::text[])`,
			sourceSystem(b.job.Source), ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k string
			var a account
			if err := rows.Scan(&k, &a.id, &a.linked); err != nil {
				rows.Close()
				return err
			}
			accounts[k] = a
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for _, u := range users {
		m, mapped := modes[u.SourceID]
		mode := "external"
		if mapped {
			mode = m.Mode
		}
		switch mode {
		case "skip":
			res.Users[u.SourceID] = ResolvedUser{Name: u.Name}
		case "member":
			res.Users[u.SourceID] = ResolvedUser{UserID: strings.ToLower(m.UserID), Name: u.Name}
		default:
			if a, ok := accounts[strings.ToLower(u.SourceID)]; ok {
				if a.linked != "" {
					res.Users[u.SourceID] = ResolvedUser{UserID: a.linked, Name: u.Name}
				} else {
					res.Users[u.SourceID] = ResolvedUser{ExternalAccountID: a.id, Name: u.Name}
				}
			}
		}
	}
	return nil
}

func (b *batchCtx) settingsLocation() *time.Location { return b.settings.location }

// resolveKeepNumbers 保留票号：确定前缀，把 next_task_number 抬到「最大来源票号 + 1」，之后的任务序号直接取来源票号。
// 序号映射不单独保存：条目的 external_key 就是来源票号，创建时再解析。
func (w *Worker) resolveKeepNumbers(ctx context.Context, tx pgx.Tx, b *batchCtx, infos []rowInfo, res *Resolved) error {
	if !b.job.Cfg.Options.KeepNumbers {
		return nil
	}
	if !b.job.SpaceCreated {
		b.warns.add("keep_numbers_unavailable", "目标空间不是本次导入新建的，未保留票号")
		return nil
	}
	var keys []string
	for _, info := range infos {
		if !info.Invalid && !info.Filtered {
			keys = append(keys, info.Data.SourceKey)
		}
	}
	prefix := dominantPrefix(keys)
	if b.job.Preview.API != nil {
		prefix = strings.ToUpper(b.job.Preview.API.ProjectKey)
	}
	if prefix == "" {
		return nil
	}
	var maxNumber int64
	for _, key := range keys {
		if p, n, ok := ParseSourceKey(key); ok && strings.EqualFold(p, prefix) && n > maxNumber {
			maxNumber = n
		}
	}
	if maxNumber == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.spaces SET next_task_number = GREATEST(next_task_number, $2), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, b.job.SpaceID, maxNumber+1); err != nil {
		return err
	}
	res.KeepPrefix = prefix
	return nil
}

// resolveSprints 把带起止日期的 Sprint 建成迭代（同名且起止日期相同的复用），记录 ITERATION 资源与 Sprint → 迭代的映射。
func (w *Worker) resolveSprints(ctx context.Context, tx pgx.Tx, b *batchCtx, infos []rowInfo, res *Resolved) error {
	if !b.job.Cfg.Options.ImportSprints {
		return nil
	}
	var enabled bool
	var mode string
	if err := tx.QueryRow(ctx, `
		SELECT iterations_enabled, iteration_mode FROM openfox.spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, b.job.SpaceID).Scan(&enabled, &mode); err != nil {
		return err
	}
	if !enabled || mode != "manual" {
		b.warns.add("sprints_unavailable", "目标空间未开启迭代或迭代是自动模式，未导入迭代")
		return nil
	}
	sprints := collectSprints(infos, b.settings.location)
	if len(sprints) == 0 {
		return nil
	}
	type existing struct {
		id, name     string
		starts, ends time.Time
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, name, starts_at, ends_at, status = 'active' FROM openfox.iterations
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status <> 'canceled'`, b.job.SpaceID)
	if err != nil {
		return err
	}
	var found []existing
	hasActive := false
	for rows.Next() {
		var e existing
		var active bool
		if err := rows.Scan(&e.id, &e.name, &e.starts, &e.ends, &active); err != nil {
			rows.Close()
			return err
		}
		hasActive = hasActive || active
		found = append(found, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	res.Iterations = map[string]string{}
	for _, sp := range sprints {
		name, _ := TruncateRunes(strings.TrimSpace(sp.Name), 80)
		if utf8.RuneCountInString(name) == 0 {
			name = "Sprint " + sp.ID
		}
		reused := false
		for _, e := range found {
			if e.name == name && e.starts.Equal(sp.Starts) && e.ends.Equal(sp.Ends) {
				res.Iterations[sp.ID] = e.id
				reused = true
				break
			}
		}
		if reused {
			continue
		}
		status := "planned"
		var startedAt, completedAt any
		switch sp.State {
		case "CLOSED":
			status, startedAt = "completed", sp.Starts
			completedAt = sp.Ends
			if t, ok := ParseDate(sp.Complete, b.settings.location); ok {
				completedAt = t
			}
		case "ACTIVE":
			if hasActive {
				b.warns.add("sprint_active_conflict", "空间已有进行中的迭代，Sprint「"+name+"」按计划中导入")
			} else {
				status, startedAt = "active", sp.Starts
				hasActive = true
			}
		}
		var id string
		var number int
		if err := tx.QueryRow(ctx, `
			UPDATE openfox.spaces SET next_iteration_number = next_iteration_number + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
			RETURNING gen_random_uuid()::text, next_iteration_number - 1`, b.job.SpaceID).Scan(&id, &number); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.iterations (organization_id, space_id, id, number, name, status, starts_at, ends_at,
			                                started_at, completed_at, created_by)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10::uuid)`,
			b.job.SpaceID, id, number, name, status, sp.Starts, sp.Ends, startedAt, completedAt, b.actor.UserID); err != nil {
			return err
		}
		spaceID := b.job.SpaceID
		if err := recordResource(ctx, tx, b.job.ID, "ITERATION", id, &spaceID, nil); err != nil {
			return err
		}
		res.Iterations[sp.ID] = id
		found = append(found, existing{id: id, name: name, starts: sp.Starts, ends: sp.Ends})
	}
	return nil
}
