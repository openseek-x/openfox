package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openfox/internal/collaboration"
	"openfox/internal/identity"
)

// 移动接口的错误映射（不需要数据库）。
func TestTaskMoveErrorMapping(t *testing.T) {
	plan := collaboration.MoveTaskPlan{AgentAssignees: 1, ActiveRuns: 2}
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"needs confirmation", plan.ConfirmationError(), http.StatusConflict, "TASK_HAS_ACTIVE_RUNS"},
		{"invalid target", collaboration.ErrInvalidTarget, http.StatusBadRequest, "INVALID_TARGET_SPACE"},
		{"version conflict", collaboration.ErrVersionConflict, http.StatusConflict, "VERSION_CONFLICT"},
		{"source retired", collaboration.ErrSpaceRetired, http.StatusConflict, "SPACE_RETIRED"},
		{"task missing", collaboration.ErrTaskNotFound, http.StatusNotFound, "NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeCollaborationError(recorder, httptest.NewRequest(http.MethodPost, "/", nil), tc.err)
			var body struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != tc.wantStatus || body.Error.Code != tc.wantCode {
				t.Fatalf("status = %d code = %s, want %d %s", recorder.Code, body.Error.Code, tc.wantStatus, tc.wantCode)
			}
			if tc.wantCode == "TASK_HAS_ACTIVE_RUNS" && (body.Error.Details["active_runs"] != float64(2) ||
				body.Error.Details["agent_assignees"] != float64(1)) {
				t.Fatalf("details = %+v", body.Error.Details)
			}
		})
	}
	if !plan.NeedsConfirmation() || (collaboration.MoveTaskPlan{}).NeedsConfirmation() {
		t.Fatal("NeedsConfirmation is wrong")
	}
}

// 两个移动路由都已注册：未登录时 401（路由不存在会是 404）。
func TestTaskMoveRoutesRegistered(t *testing.T) {
	handler := New(identity.Service{}, nil, false)
	base := "/v1/organizations/00000000-0000-0000-0000-0000000000a1/spaces/00000000-0000-0000-0000-0000000000aa/tasks/" +
		"00000000-0000-0000-0000-0000000000ab/"
	for _, suffix := range []string{"move-preview", "move"} {
		request := httptest.NewRequest(http.MethodPost, base+suffix, strings.NewReader(`{}`))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("POST %s = %d, want 401", suffix, recorder.Code)
		}
	}
}
