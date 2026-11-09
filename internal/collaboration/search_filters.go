package collaboration

import (
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"
)

// SearchTaskFilter keeps the global keyword independent from the title/description
// facet. All facets constrain the same visible task rows used to compute totals.
type SearchTaskFilter struct {
	TaskFilter
	iterations, excludedIterations       []string
	crews, excludedCrews                 []string
	creatorAgents, excludedCreatorAgents []string
	ranges                               []searchDateFacet
	fields                               []searchFieldFilter
}

type searchDateFacet struct {
	column, cast string
	negate       bool
	values       [][2]string // open bounds allowed; both empty represents a missing due date
}

func ParseSearchTaskFilter(values url.Values, actor Actor) (SearchTaskFilter, error) {
	v := maps.Clone(values)
	v.Del("iteration_id")
	v.Del("iteration_id_not")
	v.Set("q", values.Get("task_q"))
	v.Set("q_not", values.Get("task_q_not"))
	base, err := ParseActorTaskFilter(v, actor)
	f := SearchTaskFilter{TaskFilter: base}
	if err != nil {
		return f, err
	}
	f.fields, err = parseSearchFields(v)
	if err != nil {
		return f, err
	}
	if err = f.SetTriageFacets(v, actor); err != nil {
		return f, err
	}
	for i, suffix := range []string{"", "_not"} {
		iteration, err := keywordList(values["iteration_id"+suffix], "none", "current", "@rel:anypast", "@rel:previous", "@rel:current", "@rel:cooldown", "@rel:upcoming", "@rel:anyfuture")
		if err != nil {
			return f, err
		}
		crew, err := uuidList(values["crew_id"+suffix])
		if err != nil {
			return f, err
		}
		agents, err := uuidList(values["creator_agent_id"+suffix])
		if err != nil {
			return f, err
		}
		if i == 0 {
			f.iterations, f.crews, f.creatorAgents = iteration, crew, agents
		} else {
			f.excludedIterations, f.excludedCrews, f.excludedCreatorAgents = iteration, crew, agents
		}
		for _, spec := range []struct{ key, column, cast, layout string }{
			{"due_range", "t.due_date", "date", time.DateOnly},
			{"created_range", "t.created_at", "timestamptz", time.RFC3339},
		} {
			raw := values[spec.key+suffix]
			if len(raw) == 0 {
				continue
			}
			if len(raw) > maxFilterValues {
				return f, errInvalidFilter
			}
			facet := searchDateFacet{column: spec.column, cast: spec.cast, negate: i == 1}
			for _, value := range raw {
				if value == "none" && spec.cast == "date" {
					facet.values = append(facet.values, [2]string{})
					continue
				}
				lo, hi, ok := strings.Cut(value, "/")
				var start, end time.Time
				if !ok || (lo == "" && hi == "") {
					return f, errInvalidFilter
				}
				if lo != "" {
					start, err = time.Parse(spec.layout, lo)
					if err != nil {
						return f, errInvalidFilter
					}
				}
				if hi != "" {
					end, err = time.Parse(spec.layout, hi)
					if err != nil {
						return f, errInvalidFilter
					}
				}
				if lo != "" && hi != "" && !start.Before(end) {
					return f, errInvalidFilter
				}
				facet.values = append(facet.values, [2]string{lo, hi})
			}
			f.ranges = append(f.ranges, facet)
		}
	}
	return f, nil
}

func iterationSearchSQL(q *taskQuery, value string) string {
	const space = " FROM openfox.iterations i WHERE i.organization_id=t.organization_id AND i.space_id=t.space_id"
	const same = space + " AND i.id=t.iteration_id"
	switch value {
	case "none":
		return "t.iteration_id IS NULL"
	case "current", "@rel:current":
		return "EXISTS (SELECT 1" + same + " AND i.status='active')"
	case "@rel:anypast":
		return "EXISTS (SELECT 1" + same + " AND i.status='completed')"
	case "@rel:anyfuture":
		return "EXISTS (SELECT 1" + same + " AND i.status='planned')"
	case "@rel:upcoming":
		return "t.iteration_id=(SELECT i.id" + space + " AND i.status='planned' ORDER BY i.starts_at,i.number LIMIT 1)"
	case "@rel:previous":
		return "t.iteration_id=(SELECT i.id" + space + " AND i.status='completed' ORDER BY i.ends_at DESC,i.number DESC LIMIT 1)"
	case "@rel:cooldown":
		return "EXISTS (SELECT 1" + same + ` AND i.status='completed'
			AND (now() AT TIME ZONE s.iteration_timezone)::date >= (i.ends_at AT TIME ZONE s.iteration_timezone)::date
			AND (now() AT TIME ZONE s.iteration_timezone)::date < (i.ends_at AT TIME ZONE s.iteration_timezone)::date + 7*s.iteration_cooldown_weeks)`
	}
	return "t.iteration_id=" + q.arg(value) + "::uuid"
}

func (f SearchTaskFilter) conditions(q *taskQuery) []string {
	base := f.TaskFilter
	// Creator IDs and Agent authors share one OR group in the search picker.
	base.CreatorIDs, base.CreatorIDsNot = nil, nil
	// The content facet matches only literal text, unlike the main search keyword
	// which additionally resolves a display ID (including its historic aliases).
	base.Query, base.QueryNot = "", ""
	conds := base.conditions(q)
	for i, content := range []string{f.Query, f.QueryNot} {
		if content != "" {
			pattern := q.arg(SearchPattern(content))
			match := "(t.title ILIKE " + pattern + " OR t.description ILIKE " + pattern + ")"
			if i == 1 {
				match = "NOT " + match
			}
			conds = append(conds, match)
		}
	}
	facets := base.triageFacets
	facets.relations, facets.notRelations = nil, nil
	conds = append(conds, facets.conditions(q)...)
	add := func(parts []string, negate bool) {
		if len(parts) == 0 {
			return
		}
		predicate := "(" + strings.Join(parts, " OR ") + ")"
		if negate {
			predicate = "NOT COALESCE(" + predicate + ",false)"
		}
		conds = append(conds, predicate)
	}
	for i, relations := range [][]string{f.triageFacets.relations, f.triageFacets.notRelations} {
		parts := (triageFacets{role: f.triageFacets.role, relations: relations}).conditions(q)
		if slices.Contains(relations, "duplicate") {
			parts = append(parts, "ts.category='DUPLICATE'")
		}
		add(parts, i == 1)
	}
	for i, group := range [][]string{f.iterations, f.excludedIterations} {
		var parts []string
		for _, v := range group {
			parts = append(parts, iterationSearchSQL(q, v))
		}
		add(parts, i == 1)
	}
	for i, group := range [][]string{f.crews, f.excludedCrews} {
		if len(group) > 0 {
			add([]string{`EXISTS (SELECT 1 FROM openfox.task_assignees a WHERE a.organization_id=t.organization_id AND a.task_id=t.id AND a.crew_id=ANY(` + q.arg(group) + `::uuid[]))`}, i == 1)
		}
	}
	for i, g := range []struct{ users, agents []string }{{f.CreatorIDs, f.creatorAgents}, {f.CreatorIDsNot, f.excludedCreatorAgents}} {
		// Reuse the task list's original authorship predicates, including the
		// space_task_create audit which distinguishes a creator from an editor.
		var parts []string
		if len(g.users) > 0 {
			parts = append(parts, TaskFilter{CreatorIDs: g.users}.conditions(q)...)
		}
		if len(g.agents) > 0 {
			parts = append(parts, TaskFilter{CreatedViaAgent: g.agents}.conditions(q)...)
		}
		add(parts, i == 1)
	}
	for _, facet := range f.ranges {
		var parts []string
		for _, bounds := range facet.values {
			var interval []string
			if bounds[0] != "" {
				interval = append(interval, facet.column+">="+q.arg(bounds[0])+"::"+facet.cast)
			}
			if bounds[1] != "" {
				interval = append(interval, facet.column+"<"+q.arg(bounds[1])+"::"+facet.cast)
			}
			if len(interval) == 0 {
				parts = append(parts, facet.column+" IS NULL")
			} else {
				parts = append(parts, "("+strings.Join(interval, " AND ")+")")
			}
		}
		add(parts, facet.negate)
	}
	for _, field := range f.fields {
		conds = append(conds, field.condition(q))
	}
	return conds
}
