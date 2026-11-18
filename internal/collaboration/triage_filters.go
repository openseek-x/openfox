package collaboration

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/url"
	"slices"
	"strings"

	"openfox/internal/membership"
)

// Triage-only filters. The identical predicates are used for the keyset page and its count.
type triageFacets struct {
	role                                                 string
	subscribers, notSubscribers, relations, notRelations []string
	fields                                               []triageFieldMatch
}
type triageFieldMatch struct {
	id     string
	values []string
	negate bool
}

func (f *TaskFilter) SetTriageFacets(v url.Values, actor Actor) error {
	x := triageFacets{role: strings.ToUpper(string(actor.Role))}
	var err error
	if x.subscribers, err = uuidList(v["subscriber"]); err != nil {
		return err
	}
	if x.notSubscribers, err = uuidList(v["subscriber_not"]); err != nil {
		return err
	}
	relations := []string{"parent", "subtask", "blocked", "blocking", "duplicate"}
	for key, target := range map[string]*[]string{"relation": &x.relations, "relation_not": &x.notRelations} {
		if len(v[key]) > len(relations) {
			return errInvalidFilter
		}
		for _, value := range v[key] {
			if !slices.Contains(relations, value) {
				return errInvalidFilter
			}
			*target = append(*target, value)
		}
	}
	for key, values := range v {
		id, custom := strings.CutPrefix(key, "cf_")
		if !custom {
			continue
		}
		id, negate := strings.CutSuffix(id, "_not")
		if !membership.ValidateUUID(id) || len(values) == 0 || len(values) > 20 || len(x.fields) >= 20 {
			return errInvalidFilter
		}
		for _, value := range values {
			if value == "" || len([]rune(value)) > 100 {
				return errInvalidFilter
			}
		}
		x.fields = append(x.fields, triageFieldMatch{id, values, negate})
	}
	f.triageFacets = x
	return nil
}

func (x triageFacets) conditions(q *taskQuery) []string {
	var out []string
	add := func(part string, negate bool) {
		if negate {
			part = "NOT (" + part + ")"
		}
		out = append(out, part)
	}
	for i, values := range [][]string{x.subscribers, x.notSubscribers} {
		if len(values) > 0 {
			add(`EXISTS (SELECT 1 FROM openfox.task_followers f WHERE f.organization_id=t.organization_id AND f.task_id=t.id AND COALESCE(f.user_id,f.agent_id)=ANY(`+q.arg(values)+`::uuid[]))`, i == 1)
		}
	}
	for i, values := range [][]string{x.relations, x.notRelations} {
		if len(values) == 0 {
			continue
		}
		// Use the same visibility check as the relation list; a hidden peer must not affect counts.
		readable := strings.ReplaceAll(SpaceReadableSQL, "$1", q.arg(x.role)+"::text")
		// Keep the role parameter used even when filtering by parent/child only.
		peer := ` JOIN openfox.spaces s ON s.organization_id=o.organization_id AND s.id=o.space_id WHERE o.organization_id=t.organization_id AND o.deleted_at IS NULL AND ` + readable + ` AND `
		var parts []string
		for _, v := range values {
			var condition string
			switch v {
			case "parent":
				condition = "o.parent_task_id=t.id"
			case "subtask":
				condition = "o.id=t.parent_task_id"
			case "blocked", "blocking", "duplicate":
				src, dst, kind := "source_task_id", "target_task_id", "BLOCKS"
				if v == "blocked" {
					src, dst = dst, src
				}
				if v == "duplicate" {
					kind = "DUPLICATES"
				}
				condition = `EXISTS (SELECT 1 FROM openfox.task_relations r WHERE r.organization_id=t.organization_id AND r.` + src + `=t.id AND r.` + dst + `=o.id AND r.builtin_type='` + kind + `')`
				if v == "duplicate" {
					condition = "(o.id=t.duplicate_of_task_id OR " + condition + ")"
				}
			}
			parts = append(parts, `EXISTS (SELECT 1 FROM openfox.tasks o`+peer+condition+`)`)
		}
		add("("+strings.Join(parts, " OR ")+")", i == 1)
	}
	for _, field := range x.fields {
		id, vals := q.arg(field.id), q.arg(field.values)+"::text[]"
		add(`EXISTS (SELECT 1 FROM openfox.task_field_values v
     JOIN openfox.task_type_fields b ON b.organization_id=v.organization_id AND b.space_id=t.space_id AND b.task_type_id=t.type_id AND b.field_id=v.field_id
     JOIN openfox.custom_fields cf ON cf.organization_id=v.organization_id AND cf.id=v.field_id AND cf.archived_at IS NULL
     WHERE v.organization_id=t.organization_id AND v.task_id=t.id AND v.field_id=`+id+`::uuid
     AND ((v.value #>> '{}')=ANY(`+vals+`) OR (jsonb_typeof(v.value)='array' AND v.value ?| `+vals+`) OR (v.value->>'id')=ANY(`+vals+`)))`, field.negate)
	}
	return out
}

// TriageCandidateCounts groups matching queue entries for a single open picker. Never
// infer availability from a partially loaded queue. If a very large directory
// exceeds the response bound, the client keeps all choices available.
type TriageCandidateCounts struct {
	Counts   map[string]int64 `json:"counts"`
	Complete bool             `json:"complete"`
}

func (s Service) TriageCandidates(ctx context.Context, tx pgx.Tx, org, space string, actor Actor, field string, f TaskFilter) (TriageCandidateCounts, error) {
	out := TriageCandidateCounts{Counts: map[string]int64{}, Complete: true}
	if err := s.CheckActor(ctx, tx, org, actor); err != nil {
		return out, err
	}
	q := &taskQuery{}
	conds := []string{"t.organization_id=openfox_private.current_organization_id()", "t.deleted_at IS NULL", "t.space_id=" + q.arg(space) + "::uuid"}
	conds = append(conds, f.conditions(q)...)
	var vals string
	switch field {
	case "priority":
		vals = "SELECT t.priority"
	case "type":
		vals = "SELECT t.type_id::text"
	case "project":
		vals = "SELECT coalesce(t.project_id::text,'none')"
	case "iteration":
		vals = `SELECT coalesce(t.iteration_id::text,'none') UNION ALL SELECT 'current' WHERE EXISTS (SELECT 1 FROM openfox.iterations i WHERE i.organization_id=t.organization_id AND i.id=t.iteration_id AND i.status='active')`
	case "creator":
		vals = "SELECT DISTINCT a.actor_id::text FROM openfox.task_activities a WHERE a.organization_id=t.organization_id AND a.task_id=t.id AND a.kind='task.created'"
	case "subscriber":
		vals = "SELECT coalesce(a.user_id,a.agent_id)::text FROM openfox.task_followers a WHERE a.organization_id=t.organization_id AND a.task_id=t.id"
	case "label":
		vals = "SELECT a.label_id::text FROM openfox.task_labels a WHERE a.organization_id=t.organization_id AND a.task_id=t.id"
	case "crew":
		vals = "SELECT a.crew_id::text FROM openfox.task_assignees a WHERE a.organization_id=t.organization_id AND a.task_id=t.id AND a.crew_id IS NOT NULL"
	case "assignee":
		vals = `SELECT coalesce('user:'||a.user_id, 'agent:'||a.agent_id) FROM openfox.task_assignees a WHERE a.organization_id=t.organization_id AND a.task_id=t.id
		UNION ALL SELECT coalesce('user:'||t.assignee_id,'agent:'||t.executor_agent_id,'none') WHERE NOT EXISTS (SELECT 1 FROM openfox.task_assignees a WHERE a.organization_id=t.organization_id AND a.task_id=t.id)`
	default:
		id, ok := strings.CutPrefix(field, "cf_")
		if !ok || !membership.ValidateUUID(id) {
			return out, errInvalidFilter
		}
		vals = `SELECT DISTINCT item FROM openfox.task_field_values v
		JOIN openfox.task_type_fields b ON b.organization_id=v.organization_id AND b.field_id=v.field_id AND b.space_id=t.space_id AND b.task_type_id=t.type_id
		JOIN openfox.custom_fields cf ON cf.organization_id=v.organization_id AND cf.id=v.field_id AND cf.archived_at IS NULL AND cf.kind IN ('BOOLEAN','SELECT','MULTI_SELECT','MEMBER')
		CROSS JOIN LATERAL jsonb_array_elements_text(CASE jsonb_typeof(v.value) WHEN 'array' THEN v.value WHEN 'object' THEN jsonb_build_array(v.value->>'id') ELSE jsonb_build_array(v.value) END) item
		WHERE v.organization_id=t.organization_id AND v.task_id=t.id AND v.field_id=` + q.arg(id) + `::uuid`
	}
	rows, err := tx.Query(ctx, `SELECT opt.value,count(*) `+taskSelectFrom+` CROSS JOIN LATERAL (`+vals+`) AS opt(value) WHERE opt.value IS NOT NULL AND `+strings.Join(conds, " AND ")+` GROUP BY opt.value LIMIT 4097`, q.args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var count int64
		if err := rows.Scan(&k, &count); err != nil {
			return out, err
		}
		if len(out.Counts) == 4096 {
			out.Complete = false
			break
		}
		out.Counts[k] = count
	}
	return out, rows.Err()
}
