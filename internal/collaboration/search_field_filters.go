package collaboration

import (
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"openfox/internal/membership"
)

type searchFieldFilter struct {
	id, op string
	values []string
}

// Consume the search-specific operators before passing the remaining filters to
// the shared triage parser. Each field binds independently, regardless of URL order.
func parseSearchFields(v url.Values) ([]searchFieldFilter, error) {
	fields := make(map[string]searchFieldFilter)
	for key, raw := range v {
		id, ok := strings.CutPrefix(key, "cf_")
		if !ok {
			continue
		}
		id, hasOp := strings.CutSuffix(id, "_op")
		id, negate := strings.CutSuffix(id, "_not")
		if !membership.ValidateUUID(id) || len(raw) == 0 || len(raw) > 20 || (hasOp && negate) {
			return nil, errInvalidFilter
		}
		f := fields[id]
		f.id = id
		if hasOp {
			if f.op != "" || len(raw) != 1 || raw[0] == "" {
				return nil, errInvalidFilter
			}
			f.op = raw[0]
		} else {
			if f.values != nil || (negate && f.op != "") {
				return nil, errInvalidFilter
			}
			if negate {
				f.op = "is_not"
			}
			for _, value := range raw {
				if value == "" || len([]rune(value)) > 100 {
					return nil, errInvalidFilter
				}
			}
			f.values = raw
		}
		fields[id] = f
		delete(v, key)
	}
	if len(fields) > 20 {
		return nil, errInvalidFilter
	}
	out := make([]searchFieldFilter, 0, len(fields))
	for _, f := range fields {
		if f.op == "" {
			f.op = "is"
		}
		op := strings.TrimPrefix(f.op, "not_")
		switch op {
		case "is", "is_not":
			if f.op != op || len(f.values) == 0 {
				return nil, errInvalidFilter
			}
		case "is_empty", "is_true", "is_false":
			if len(f.values) != 0 || f.op != op {
				return nil, errInvalidFilter
			}
		case "contains":
			if len(f.values) != 1 {
				return nil, errInvalidFilter
			}
		case "lt", "lte", "gt", "gte":
			if len(f.values) != 1 || !searchNumber(f.values[0]) {
				return nil, errInvalidFilter
			}
		case "before", "before_or_on", "after", "after_or_on":
			if len(f.values) != 1 || !searchDate(f.values[0]) {
				return nil, errInvalidFilter
			}
		case "between":
			if len(f.values) != 2 || !(searchNumber(f.values[0]) && searchNumber(f.values[1]) || searchDate(f.values[0]) && searchDate(f.values[1])) {
				return nil, errInvalidFilter
			}
		default:
			return nil, errInvalidFilter
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b searchFieldFilter) int { return strings.Compare(a.id, b.id) })
	return out, nil
}

func searchNumber(s string) bool {
	n, err := strconv.ParseFloat(s, 64)
	// PostgreSQL numeric accepts decimal input only, unlike Go's hexadecimal floats.
	return err == nil && strings.Trim(s, "0123456789eE+-.") == "" && !math.IsNaN(n) && !math.IsInf(n, 0)
}
func searchDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

func (f searchFieldFilter) condition(q *taskQuery) string {
	const value = "(v.value #>> '{}')"
	// CASE prevents casts of unrelated fields or historic values of other kinds.
	const number = "(CASE WHEN cf.kind='NUMBER' AND jsonb_typeof(v.value)='number' THEN " + value + "::numeric END)"
	op := strings.TrimPrefix(f.op, "not_")
	negate := op != f.op || op == "is_not" || op == "is_empty"
	var test string
	switch op {
	case "is", "is_not":
		set := q.arg(f.values) + "::text[]"
		test = "(" + value + "=ANY(" + set + ") OR (jsonb_typeof(v.value)='array' AND v.value ?| " + set + ") OR v.value->>'id'=ANY(" + set + "))"
		if len(f.values) == 1 && searchNumber(f.values[0]) {
			test = "(" + test + " OR " + number + "=" + q.arg(f.values[0]) + "::numeric)"
		}
	case "is_empty":
		test = `v.value NOT IN ('null'::jsonb,'""'::jsonb,'[]'::jsonb)`
	case "is_true", "is_false":
		test = "cf.kind='BOOLEAN' AND v.value = " + q.arg(strconv.FormatBool(op == "is_true")) + "::jsonb"
	case "contains":
		test = "cf.kind IN ('TEXT','RICH_TEXT','URL') AND " + value + " ILIKE " + q.arg(SearchPattern(f.values[0]))
	case "between":
		if searchDate(f.values[0]) {
			test = "cf.kind='DATE' AND " + value + " BETWEEN " + q.arg(f.values[0]) + "::text AND " + q.arg(f.values[1]) + "::text"
		} else {
			test = number + " BETWEEN " + q.arg(f.values[0]) + "::numeric AND " + q.arg(f.values[1]) + "::numeric"
		}
	default:
		comparison := map[string]string{"lt": "<", "lte": "<=", "gt": ">", "gte": ">=", "after": ">", "after_or_on": ">=", "before": "<", "before_or_on": "<="}[op]
		if searchDate(f.values[0]) {
			test = "cf.kind='DATE' AND " + value + comparison + q.arg(f.values[0]) + "::text"
		} else {
			test = number + comparison + q.arg(f.values[0]) + "::numeric"
		}
	}
	match := `EXISTS (SELECT 1 FROM openfox.task_field_values v
		JOIN openfox.task_type_fields b ON b.organization_id=v.organization_id AND b.field_id=v.field_id AND b.task_type_id=t.type_id AND b.space_id=t.space_id
		JOIN openfox.custom_fields cf ON cf.organization_id=v.organization_id AND cf.id=v.field_id AND cf.archived_at IS NULL
		WHERE v.organization_id=t.organization_id AND v.task_id=t.id AND v.field_id=` + q.arg(f.id) + "::uuid AND (" + test + "))"
	if negate {
		return "NOT " + match
	}
	return match
}
