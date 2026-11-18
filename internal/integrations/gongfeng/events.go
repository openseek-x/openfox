package gongfeng

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

var ErrAuthentication = errors.New("gongfeng: invalid webhook authentication")

type Event struct {
	Name            string
	ProjectID       int64
	Kind, ObjectKey string
	ParentID        *int64
	Payload         map[string]any
	Test, Ignored   bool
}

func singleAlias(h http.Header, a, b string) (string, error) {
	av, bv := h.Values(a), h.Values(b)
	if len(av) > 1 || len(bv) > 1 {
		return "", ErrAuthentication
	}
	x, y := "", ""
	if len(av) == 1 {
		x = av[0]
	}
	if len(bv) == 1 {
		y = bv[0]
	}
	if len(av) == 1 && len(bv) == 1 && x != y {
		return "", ErrAuthentication
	}
	if x != "" {
		return x, nil
	}
	return y, nil
}
func AuthenticateEvent(h http.Header, secret string) (name string, test bool, err error) {
	token, err := singleAlias(h, "X-Token", "X-Gitlab-Token")
	if err != nil || secret == "" || subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
		return "", false, ErrAuthentication
	}
	name, err = singleAlias(h, "X-Event", "X-Gitlab-Event")
	if err != nil || len(name) < 1 || len(name) > 64 {
		return "", false, ErrAuthentication
	}
	sources := h.Values("X-Source-Type")
	if len(sources) > 1 {
		return "", false, ErrAuthentication
	}
	return name, len(sources) == 1 && sources[0] == "Test", nil
}

func ParseEvent(name string, body []byte) (Event, error) {
	e := Event{Name: name, Payload: map[string]any{}}
	var p struct {
		Kind      string `json:"object_kind"`
		ProjectID int64  `json:"project_id"`
		Before    string `json:"before"`
		After     string `json:"after"`
		Ref       string `json:"ref"`
		Count     *int   `json:"total_commits_count"`
		Commits   []struct {
			ID string `json:"id"`
		} `json:"commits"`
		RawAttributes json.RawMessage `json:"object_attributes"`
		Issue         struct {
			ID        int64 `json:"id"`
			ProjectID int64 `json:"project_id"`
		} `json:"issue"`
	}
	if json.Unmarshal(body, &p) != nil || len(body) == 0 || string(body) == "null" {
		return e, ErrInvalid
	}
	var a struct {
		ID              int64  `json:"id"`
		ProjectID       int64  `json:"project_id"`
		TargetProjectID int64  `json:"target_project_id"`
		NoteableID      int64  `json:"noteable_id"`
		NoteableType    string `json:"noteable_type"`
	}
	if len(p.RawAttributes) > 0 && json.Unmarshal(p.RawAttributes, &a) != nil {
		return e, ErrInvalid
	}
	agree := func(values ...int64) (int64, bool) {
		v := int64(0)
		for _, x := range values {
			if x < 0 {
				return 0, false
			}
			if x != 0 {
				if v != 0 && v != x {
					return 0, false
				}
				v = x
			}
		}
		return v, v > 0
	}
	var ok bool
	switch name {
	case "Issue Hook":
		if p.Kind != "issue" || a.ID <= 0 {
			return e, ErrInvalid
		}
		e.ProjectID, ok = agree(p.ProjectID, a.ProjectID)
		if !ok {
			return e, ErrInvalid
		}
		e.Kind, e.ObjectKey = "ISSUE_REFRESH", strconv.FormatInt(a.ID, 10)
		e.Payload["history"] = false
	case "Merge Request Hook":
		if p.Kind != "merge_request" || a.ID <= 0 {
			return e, ErrInvalid
		}
		e.ProjectID, ok = agree(p.ProjectID, a.TargetProjectID)
		if !ok {
			return e, ErrInvalid
		}
		e.Kind, e.ObjectKey = "MR_REFRESH", strconv.FormatInt(a.ID, 10)
	case "Note Hook":
		if p.Kind != "note" || a.ID <= 0 {
			return e, ErrInvalid
		}
		e.ProjectID, ok = agree(p.ProjectID, a.ProjectID, p.Issue.ProjectID)
		if !ok {
			return e, ErrInvalid
		}
		if a.NoteableType != "Issue" {
			e.Ignored = true
			return e, nil
		}
		// The official example has conflicting noteable_id and nested issue.id.
		// Never choose an arbitrary one as authority for cross-object routing.
		parent, consistent := agree(a.NoteableID, p.Issue.ID)
		if !consistent {
			return e, ErrInvalid
		}
		e.Kind, e.ObjectKey, e.ParentID = "NOTE_REFRESH", strconv.FormatInt(a.ID, 10), &parent
	case "Push Hook":
		if p.Kind != "push" || p.ProjectID <= 0 || p.Count == nil || *p.Count < 0 || len(p.Commits) > 30 || len(p.Commits) > *p.Count || len(p.Ref) > 1024 {
			return e, ErrInvalid
		}
		e.ProjectID = p.ProjectID
		if !strings.HasPrefix(p.Ref, "refs/heads/") {
			e.Ignored = true
			return e, nil
		}
		ids := make([]string, 0, len(p.Commits))
		seen := map[string]bool{}
		for _, c := range p.Commits {
			if !ValidCommitSHA(c.ID) || seen[c.ID] {
				return e, ErrInvalid
			}
			ids = append(ids, c.ID)
			seen[c.ID] = true
		}
		// Zero tips/new branches are kept as an explicit gap for the worker/UI;
		// no artificial one-commit success is inferred from the payload.
		if len(p.Before) != 40 || len(p.After) != 40 {
			return e, ErrInvalid
		}
		e.Kind, e.ObjectKey = "PUSH", p.Before+":"+p.After
		e.Payload = map[string]any{"before": p.Before, "after": p.After, "ref": p.Ref, "count": *p.Count, "commits": ids}
	default:
		e.ProjectID, ok = agree(p.ProjectID, a.ProjectID, a.TargetProjectID)
		if !ok {
			return e, ErrInvalid
		}
		e.Ignored = true
	}
	return e, nil
}
