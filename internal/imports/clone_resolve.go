package imports

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/spacebundle"
)

// cloneValidate 是 validating 阶段：读快照包头，确认它属于这个任务。
func (w *Worker) cloneValidate(ctx context.Context, b *cloneBatchCtx) error {
	src, err := w.Storage.Open(ctx, b.job.BundleKey)
	if err != nil {
		return err
	}
	defer src.Close()
	r, err := spacebundle.NewReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	h, err := r.ReadHeader()
	if err != nil {
		return err
	}
	if !strings.EqualFold(h.SourceSpaceID, b.job.SourceSpace) || !strings.EqualFold(h.SourceOrganization, b.job.SourceOrg) {
		return errBadBundle("快照包与克隆任务的来源不一致")
	}
	b.job.Doc.SourceOrganizationName = h.SourceOrgName
	b.nextPhase = "creating_space"
	return nil
}

func decodeAll[T any](m map[string][]json.RawMessage, kind string) ([]T, error) {
	return spacebundle.Decode[T](m[kind])
}

func newStringMap() map[string]string { return map[string]string{} }

// cloneCreateSpace 是 creating_space 阶段（一个事务）：建空间并解析全部组织级配置。
func (w *Worker) cloneCreateSpace(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx) error {
	h, recs, err := spacebundle.Collect(w.bundleOpener(b.job.BundleKey),
		spacebundle.KindStatus, spacebundle.KindType, spacebundle.KindLabel, spacebundle.KindField,
		spacebundle.KindProject, spacebundle.KindRelType, spacebundle.KindPerson)
	if err != nil {
		return err
	}
	statuses, err := decodeAll[spacebundle.Status](recs, spacebundle.KindStatus)
	if err != nil {
		return err
	}
	types, err := decodeAll[spacebundle.Type](recs, spacebundle.KindType)
	if err != nil {
		return err
	}
	labels, err := decodeAll[spacebundle.Label](recs, spacebundle.KindLabel)
	if err != nil {
		return err
	}
	fields, err := decodeAll[spacebundle.Field](recs, spacebundle.KindField)
	if err != nil {
		return err
	}
	projects, err := decodeAll[spacebundle.Project](recs, spacebundle.KindProject)
	if err != nil {
		return err
	}
	relTypes, err := decodeAll[spacebundle.RelType](recs, spacebundle.KindRelType)
	if err != nil {
		return err
	}
	persons, err := decodeAll[spacebundle.Person](recs, spacebundle.KindPerson)
	if err != nil {
		return err
	}

	// 1. 新空间。名称与标识符用预览时确认的值；估点等设置沿用源空间。
	collab := collaboration.Service{}
	doc := &b.job.Doc
	views, projectsOn, multi := h.Space.ViewsEnabled, h.Space.ProjectsOn, h.Space.MultiAssignee
	icon := h.Space.Icon
	if strings.HasPrefix(icon, "upload:") {
		icon = ""
	}
	detail, _, err := collab.CreateSpaceDetail(ctx, tx, b.orgID, b.actor, collaboration.SpaceCreateInput{
		Name: doc.TargetName, Prefix: doc.TargetPrefix, Visibility: h.Space.Visibility, Purpose: h.Space.Purpose, Icon: icon,
		Features:      &collaboration.SpaceFeaturesPatch{Views: &views, Projects: &projectsOn},
		MultiAssignee: &multi,
	})
	if err != nil {
		return err
	}
	spaceID := detail.ID
	b.job.SpaceID = spaceID
	doc.State.Created = true
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.spaces
		   SET estimation_scale = $2, estimation_extended = $3, estimation_allow_zero = $4, estimation_count_unestimated = $5,
		       updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
		spaceID, h.Space.EstimationScale, h.Space.EstimationExtended, h.Space.EstimationAllowZero, h.Space.EstimationCountUnscored); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.import_jobs SET space_id = $2::uuid, space_created = true
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, b.job.ID, spaceID); err != nil {
		return err
	}
	doc.State.NextNumber = h.Space.NextTaskNumber

	res := &cloneResolved{Statuses: newStringMap(), Types: newStringMap(), Labels: newStringMap(), Fields: map[string]cloneField{},
		Projects: newStringMap(), RelTypes: newStringMap(), People: map[string]clonePerson{}}

	// 2. 状态（按名称匹配，类别不同时沿用已有的并告警；没有就新建）。
	for _, s := range statuses {
		id, err := w.cloneStatus(ctx, tx, b, s)
		if err != nil {
			return err
		}
		res.Statuses[s.ID] = id
	}
	// 3. 任务类型与工作流。
	if err := w.cloneTypes(ctx, tx, b, spaceID, types, res); err != nil {
		return err
	}
	// 4. 标签（新空间里的平铺标签）。
	for _, l := range labels {
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO openfox.labels (organization_id, space_id, name, color, description)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2, NULLIF($3, ''), $4)
			ON CONFLICT (organization_id, space_id, lower(name)) WHERE space_id IS NOT NULL DO UPDATE SET description = EXCLUDED.description
			RETURNING id::text`, spaceID, truncateBytes(l.Name, 50), l.Color, l.Description).Scan(&id); err != nil {
			return err
		}
		res.Labels[l.ID] = id
	}
	// 5. 自定义字段、专项、关联类型。
	for _, f := range fields {
		cf, err := resolveImportField(ctx, tx, b.job.ID, b.actor.UserID, f)
		if err != nil {
			return err
		}
		res.Fields[f.ID] = cf
	}
	// 类型上的字段挂载在字段解析之后补。
	if err := w.cloneAttachFields(ctx, tx, b, spaceID, types, res); err != nil {
		return err
	}
	for _, p := range projects {
		id, err := w.cloneProject(ctx, tx, b, spaceID, p)
		if err != nil {
			return err
		}
		res.Projects[p.ID] = id
	}
	for _, rt := range relTypes {
		id, err := w.cloneRelType(ctx, tx, b, rt)
		if err != nil {
			return err
		}
		res.RelTypes[rt.ID] = id
	}
	// 6. 人员：按邮箱对应目标组织活跃成员，对应不上的保留为外部账号。
	if err := w.clonePeople(ctx, tx, b, spaceID, h.SourceOrganization, h.SourceOrgName, persons, res); err != nil {
		return err
	}
	doc.State.Resolved = res
	b.nextPhase = "importing"
	return nil
}

func (w *Worker) cloneStatus(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx, s spacebundle.Status) (string, error) {
	var id, category string
	var archived bool
	err := tx.QueryRow(ctx, `
		SELECT id::text, category, archived_at IS NOT NULL FROM openfox.task_statuses
		 WHERE organization_id = openfox_private.current_organization_id() AND lower(name) = lower($1)`, s.Name).Scan(&id, &category, &archived)
	switch {
	case err == nil:
		if archived {
			if _, err := tx.Exec(ctx, `
				UPDATE openfox.task_statuses SET archived_at = NULL, version = version + 1, updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id); err != nil {
				return "", err
			}
			if err := recordResource(ctx, tx, b.job.ID, "STATUS", id, nil, nil); err != nil {
				return "", err
			}
		}
		if category != s.Category {
			b.warns.add("status_category_differs", "目标组织已有同名任务状态「"+s.Name+"」但类别不同，已沿用目标组织的状态")
		}
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO openfox.task_statuses (organization_id, name, category, position, color, icon)
		SELECT openfox_private.current_organization_id(), $1, $2, COALESCE(max(position), 0) + 1, NULLIF($3, ''), NULLIF($4, '')
		  FROM openfox.task_statuses WHERE organization_id = openfox_private.current_organization_id()
		RETURNING id::text`, truncateBytes(s.Name, 64), s.Category, s.Color, s.Icon).Scan(&id); err != nil {
		return "", err
	}
	return id, recordResource(ctx, tx, b.job.ID, "STATUS", id, nil, nil)
}

// cloneTypes 创建任务类型与工作流。新空间自带一个默认类型：与来源同名就复用（工作流替换成来源的），
// 否则创建来源的类型、把来源的默认类型设为默认，并归档自带的类型。
func (w *Worker) cloneTypes(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx, spaceID string, types []spacebundle.Type, res *cloneResolved) error {
	// 目标组织没有任何任务状态时，新空间不会自带默认类型（seedDefaultTaskType 没有可用的工作流），此时 seededID 为空。
	var seededID, seededName string
	if err := tx.QueryRow(ctx, `
		SELECT id::text, name FROM openfox.task_types
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND is_default AND archived_at IS NULL`,
		spaceID).Scan(&seededID, &seededName); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	seededMatched := false
	var defaultTarget string
	for _, t := range types {
		var id string
		if seededID != "" && strings.EqualFold(t.Name, seededName) && !seededMatched {
			seededMatched = true
			id = seededID
			if _, err := tx.Exec(ctx, `
				UPDATE openfox.task_types SET description = $2, icon = NULLIF($3, ''), enabled = $4, version = version + 1
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
				id, t.Description, t.Icon, t.Enabled); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				DELETE FROM openfox.workflow_statuses
				 WHERE organization_id = openfox_private.current_organization_id() AND task_type_id = $1::uuid`, id); err != nil {
				return err
			}
		} else {
			if err := tx.QueryRow(ctx, `
				INSERT INTO openfox.task_types (organization_id, space_id, name, description, icon, enabled, is_default)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, NULLIF($4, ''), $5, false)
				RETURNING id::text`, spaceID, truncateBytes(t.Name, 64), t.Description, t.Icon, t.Enabled).Scan(&id); err != nil {
				return err
			}
		}
		res.Types[t.ID] = id
		if t.IsDefault {
			defaultTarget = id
		}
		if t.Archived {
			b.job.Doc.State.ArchiveTypes = append(b.job.Doc.State.ArchiveTypes, id)
		}
		hasDefault := false
		for _, ws := range t.Workflow {
			if ws.IsDefault {
				hasDefault = true
			}
		}
		for i, ws := range t.Workflow {
			statusID := res.Statuses[ws.StatusID]
			if statusID == "" {
				continue
			}
			isDefault := ws.IsDefault
			if !hasDefault {
				isDefault = false
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5)
				ON CONFLICT DO NOTHING`, spaceID, id, statusID, i, isDefault); err != nil {
				return err
			}
		}
	}
	if !seededMatched && seededID != "" {
		// 来源没有与自带类型同名的类型：自带类型退出（先取消默认再归档）。
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.task_types SET is_default = false, archived_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, seededID); err != nil {
			return err
		}
	} else if seededMatched && defaultTarget != "" && defaultTarget != seededID {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.task_types SET is_default = false, version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, seededID); err != nil {
			return err
		}
	}
	if defaultTarget != "" && defaultTarget != seededID {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.task_types SET is_default = true, version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, defaultTarget); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) cloneAttachFields(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx, spaceID string, types []spacebundle.Type, res *cloneResolved) error {
	for _, t := range types {
		typeID := res.Types[t.ID]
		for i, fieldID := range t.FieldIDs {
			f, ok := res.Fields[fieldID]
			if !ok || typeID == "" {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO openfox.task_type_fields (organization_id, space_id, task_type_id, field_id, position, created_by)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5::uuid)
				ON CONFLICT DO NOTHING`, spaceID, typeID, f.ID, i, b.actor.UserID); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveImportField 按名称 + 类型匹配目标组织已有的自定义字段；没有就新建（含选项）。
func resolveImportField(ctx context.Context, tx pgx.Tx, jobID, userID string, f spacebundle.Field) (cloneField, error) {
	out := cloneField{Kind: f.Kind}
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.custom_fields
		 WHERE organization_id = openfox_private.current_organization_id() AND lower(name) = lower($1) AND kind = $2 AND archived_at IS NULL
		 ORDER BY created_at LIMIT 1`, f.Name, f.Kind).Scan(&out.ID)
	hasOptions := f.Kind == "SELECT" || f.Kind == "MULTI_SELECT"
	if err == nil {
		if hasOptions {
			rows, err := tx.Query(ctx, `
				SELECT id::text, lower(label) FROM openfox.custom_field_options
				 WHERE organization_id = openfox_private.current_organization_id() AND field_id = $1::uuid AND archived_at IS NULL`, out.ID)
			if err != nil {
				return out, err
			}
			byLabel := map[string]string{}
			for rows.Next() {
				var id, label string
				if err := rows.Scan(&id, &label); err != nil {
					rows.Close()
					return out, err
				}
				byLabel[label] = id
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return out, err
			}
			out.Options = map[string]string{}
			for _, o := range f.Options {
				if id, ok := byLabel[strings.ToLower(o.Label)]; ok {
					out.Options[o.ID] = id
				}
			}
		}
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	// 默认值引用选项 / 成员，克隆时不带；其余类型沿用。
	var def any
	if !hasOptions && f.Kind != "MEMBER" && len(f.Default) > 0 {
		def = []byte(f.Default)
	}
	var scope any
	if f.Kind == "MEMBER" {
		scope = f.MemberScope
	}
	name, _ := TruncateRunes(f.Name, 64)
	if err := tx.QueryRow(ctx, `
		INSERT INTO openfox.custom_fields (organization_id, name, kind, description, default_value, member_scope, created_by)
		VALUES (openfox_private.current_organization_id(), $1, $2, $3, $4::jsonb, $5, $6::uuid)
		RETURNING id::text`, name, f.Kind, truncateBytes(f.Description, 1000), def, scope, userID).Scan(&out.ID); err != nil {
		return out, err
	}
	if err := recordResource(ctx, tx, jobID, "CUSTOM_FIELD", out.ID, nil, nil); err != nil {
		return out, err
	}
	if hasOptions {
		out.Options = map[string]string{}
		for i, o := range f.Options {
			var id string
			if err := tx.QueryRow(ctx, `
				INSERT INTO openfox.custom_field_options (organization_id, field_id, label, color, position)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4) RETURNING id::text`,
				out.ID, truncateBytes(o.Label, 64), o.Color, i).Scan(&id); err != nil {
				return out, err
			}
			out.Options[o.ID] = id
		}
	}
	return out, nil
}

// cloneProject 按名称匹配目标组织的专项，没有就新建；新空间加入专项的空间列表。
func (w *Worker) cloneProject(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx, spaceID string, p spacebundle.Project) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.projects
		 WHERE organization_id = openfox_private.current_organization_id() AND lower(name) = lower($1)
		 ORDER BY created_at LIMIT 1`, p.Name).Scan(&id)
	created := false
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `
			INSERT INTO openfox.projects (organization_id, name, icon, description, status, priority, created_by)
			VALUES (openfox_private.current_organization_id(), $1, NULLIF($2, ''), $3, $4, $5, $6::uuid) RETURNING id::text`,
			truncateBytes(p.Name, 80), p.Icon, p.Description, p.Status, p.Priority, b.actor.UserID).Scan(&id); err != nil {
			return "", err
		}
		created = true
		if err := recordResource(ctx, tx, b.job.ID, "PROJECT", id, nil, nil); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.project_spaces (organization_id, project_id, space_id, is_primary)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3)
		ON CONFLICT DO NOTHING`, id, spaceID, created)
	return id, err
}

func (w *Worker) cloneRelType(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx, rt spacebundle.RelType) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.task_relation_types
		 WHERE organization_id = openfox_private.current_organization_id() AND lower(name) = lower($1)`, rt.Name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO openfox.task_relation_types (organization_id, name, outward_label, inward_label, created_by)
		VALUES (openfox_private.current_organization_id(), $1, $2, $3, $4::uuid) RETURNING id::text`,
		truncateBytes(rt.Name, 64), truncateBytes(rt.Outward, 64), truncateBytes(rt.Inward, 64), b.actor.UserID).Scan(&id); err != nil {
		return "", err
	}
	return id, recordResource(ctx, tx, b.job.ID, "RELATION_TYPE", id, nil, nil)
}

// clonePeople 解析来源人员：邮箱对应到目标组织活跃成员（且能访问新空间）→ 成员；否则 → 外部账号（不带邮箱）。
func (w *Worker) clonePeople(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx, spaceID, sourceOrgID, sourceOrgName string, persons []spacebundle.Person, res *cloneResolved) error {
	collab := collaboration.Service{}
	label := truncateBytes(sourceOrgName, 255)
	for _, p := range persons {
		name := truncateBytes(strings.TrimSpace(p.Name), 200)
		if name == "" {
			name = "未知用户"
		}
		person := clonePerson{Name: name}
		if p.Kind == "user" && strings.TrimSpace(p.Email) != "" {
			var userID string
			err := tx.QueryRow(ctx, `
				SELECT m.user_id::text FROM openfox.organization_members m JOIN openfox.users u ON u.id = m.user_id
				 WHERE m.organization_id = openfox_private.current_organization_id() AND m.status = 'ACTIVE'
				   AND u.normalized_email = lower(btrim($1))`, p.Email).Scan(&userID)
			switch {
			case err == nil:
				if collab.ImportAssignable(ctx, tx, spaceID, b.actor, userID) {
					person.UserID = userID
				}
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
		}
		if person.UserID == "" {
			var accountID string
			if err := tx.QueryRow(ctx, `
				INSERT INTO openfox.external_accounts (organization_id, provider, external_user_id, display_name, email, created_by, source_label)
				VALUES (openfox_private.current_organization_id(), $1, $2, $3, '', $4::uuid, $5)
				ON CONFLICT (organization_id, provider, lower(external_user_id)) DO UPDATE
				   SET display_name = EXCLUDED.display_name, updated_at = clock_timestamp(), version = openfox.external_accounts.version + 1
				RETURNING id::text`,
				CloneSourceSystem, truncateBytes(sourceOrgID+":"+p.Key, 255), name, b.actor.UserID, label).Scan(&accountID); err != nil {
				return err
			}
			person.ExternalID = accountID
		}
		res.People[p.Key] = person
	}
	return nil
}

var _ = membership.NewID
