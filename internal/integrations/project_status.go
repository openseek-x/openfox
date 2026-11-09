package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

func (s ProjectSource) UpdateProjectStatus(ctx context.Context, project, id, kind, target string) error {
	if !companyPattern.MatchString(project) || target == "" {
		return ErrInvalidRequest
	}
	switch s.Provider {
	case "tapd":
		return s.updateTAPDFields(ctx, project, id, kind, nil, nil, &target)
	case "pingcode":
		if !companyPattern.MatchString(id) {
			return ErrInvalidRequest
		}
		state := ""
		e := s.pingPages(ctx, "/pjm/work_item/states", url.Values{"project_id": {project}}, func(raw json.RawMessage) error {
			var v ExternalProject
			if json.Unmarshal(raw, &v) != nil {
				return ErrUpstreamUnavailable
			}
			if strings.EqualFold(v.Name, target) {
				if state != "" {
					return ErrInvalidRequest
				}
				state = v.ID
			}
			return nil
		})
		if e != nil {
			return e
		}
		if state == "" {
			return ErrInvalidRequest
		}
		var out struct {
			ID             string
			Project, State ExternalProject
		}
		if e = s.writeProjectJSON(ctx, http.MethodPatch, "/pjm/work_items/"+id, map[string]string{"state_id": state}, &out); e != nil {
			return e
		}
		if out.ID != id || out.Project.ID != project || out.State.ID != state {
			return ErrUpstreamUnavailable
		}
		return nil
	case "teambition":
		if !companyPattern.MatchString(id) {
			return ErrInvalidRequest
		}
		state := ""
		e := s.teambitionPages(ctx, "/v3/project/"+project+"/taskflowstatus/search", func(v teambitionItem) error {
			if strings.EqualFold(v.Name, target) {
				if state != "" {
					return ErrInvalidRequest
				}
				state = v.ID
			}
			return nil
		})
		if e != nil {
			return e
		}
		if state == "" {
			return ErrInvalidRequest
		}
		var out struct {
			Result struct {
				TaskflowstatusID string `json:"taskflowstatusId"`
			}
			ErrorMessage string
		}
		if e = s.writeProjectJSON(ctx, http.MethodPut, "/v3/task/"+id+"/taskflowstatus", map[string]string{"taskflowstatusId": state}, &out); e != nil {
			return e
		}
		if out.ErrorMessage != "" || out.Result.TaskflowstatusID != state {
			return ErrUpstreamUnavailable
		}
		return nil
	case "shortcut":
		workspace, story, ok := strings.Cut(id, "/")
		if !ok || workspace != s.ExternalID || !tapdCompanyPattern.MatchString(story) {
			return ErrInvalidRequest
		}
		var flows []struct {
			States []struct {
				ID   json.Number
				Name string
			}
		}
		if e := s.get(ctx, "/workflows", nil, &flows); e != nil {
			return e
		}
		state := json.Number("")
		for _, f := range flows {
			for _, v := range f.States {
				if strings.EqualFold(v.Name, target) {
					if state != "" {
						return ErrInvalidRequest
					}
					state = v.ID
				}
			}
		}
		if state == "" {
			return ErrInvalidRequest
		}
		var result struct {
			ID      json.Number
			Project json.Number `json:"project_id"`
			State   json.Number `json:"workflow_state_id"`
		}
		if e := s.writeProjectJSON(ctx, http.MethodPut, "/stories/"+story, map[string]any{"workflow_state_id": state}, &result); e != nil {
			return e
		}
		if result.ID.String() != story || result.Project.String() != project || result.State != state {
			return ErrUpstreamUnavailable
		}
		return nil
	default:
		return ErrInvalidRequest
	}
}
