package wp02_wp03

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSpaceWorkflowAndTaskAuthorization(t *testing.T) {
	ctx, harness := newHarness(t)
	ownerClient, err := harness.Client(ctx, userBearerAuth(Owner))
	if err != nil {
		t.Fatalf("create owner client: %v", err)
	}

	statusesResponse := requestJSON(t, ownerClient, http.MethodGet,
		route(harness, "/organizations/"+OrganizationA+"/task-statuses"), nil, nil)
	statusesBody := assertStatus(t, statusesResponse, http.StatusOK)
	var statuses struct {
		Statuses []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
		} `json:"statuses"`
	}
	if err := json.Unmarshal(statusesBody, &statuses); err != nil {
		t.Fatal(err)
	}
	if len(statuses.Statuses) != 7 {
		t.Fatalf("status count = %d, want 7", len(statuses.Statuses))
	}
	var todoID string
	var doneID string
	for _, status := range statuses.Statuses {
		if status.Category == "TODO" {
			todoID = status.ID
		}
		if status.Category == "DONE" {
			doneID = status.ID
		}
	}
	if todoID == "" || doneID == "" {
		t.Fatal("TODO/DONE statuses were not seeded")
	}

	statusCreateResponse := requestJSON(t, ownerClient, http.MethodPost,
		route(harness, "/organizations/"+OrganizationA+"/task-statuses"), map[string]any{
			"name": "Review queue", "description": "A second TODO status", "category": "TODO",
			"position": 3, "color": "blue-500", "icon": "inbox",
		}, nil)
	statusCreateBody := assertStatus(t, statusCreateResponse, http.StatusCreated)
	customStatusID := stringField(t, statusCreateBody, "id")
	customStatusVersion := numberField(t, statusCreateBody, "version")
	if stringField(t, statusCreateBody, "category") != "TODO" || stringField(t, statusCreateBody, "description") != "A second TODO status" {
		t.Fatalf("custom status = %s", statusCreateBody)
	}
	statusUpdateResponse := requestJSON(t, ownerClient, http.MethodPatch,
		route(harness, "/organizations/"+OrganizationA+"/task-statuses/"+customStatusID), map[string]any{
			"name": "Ready for review", "description": "Renamed TODO status", "version": customStatusVersion,
		}, nil)
	statusUpdateBody := assertStatus(t, statusUpdateResponse, http.StatusOK)
	updatedStatusVersion := numberField(t, statusUpdateBody, "version")
	statusArchiveResponse := requestJSON(t, ownerClient, http.MethodPatch,
		route(harness, "/organizations/"+OrganizationA+"/task-statuses/"+customStatusID), map[string]any{
			"archived": true, "version": updatedStatusVersion,
		}, nil)
	statusArchiveBody := assertStatus(t, statusArchiveResponse, http.StatusOK)
	statusDeleteResponse := requestJSON(t, ownerClient, http.MethodDelete,
		route(harness, "/organizations/"+OrganizationA+"/task-statuses/"+customStatusID), nil, nil)
	assertStatus(t, statusDeleteResponse, http.StatusNoContent)
	statusInUseResponse := requestJSON(t, ownerClient, http.MethodDelete,
		route(harness, "/organizations/"+OrganizationA+"/task-statuses/"+todoID), nil, nil)
	assertError(t, statusInUseResponse, http.StatusConflict, "STATUS_IS_WORKFLOW_DEFAULT")
	_ = statusArchiveBody

	typesResponse := requestJSON(t, ownerClient, http.MethodGet,
		route(harness, "/organizations/"+OrganizationA+"/spaces/00000000-0000-0000-0000-0000000000aa/task-types"), nil, nil)
	typesBody := assertStatus(t, typesResponse, http.StatusOK)
	var types struct {
		TaskTypes []struct {
			ID string `json:"id"`
		} `json:"task_types"`
	}
	if err := json.Unmarshal(typesBody, &types); err != nil {
		t.Fatal(err)
	}
	if len(types.TaskTypes) == 0 || types.TaskTypes[0].ID == "" {
		t.Fatalf("task types = %s", typesBody)
	}

	typeCreateResponse := requestJSON(t, ownerClient, http.MethodPost,
		route(harness, "/organizations/"+OrganizationA+"/spaces/00000000-0000-0000-0000-0000000000aa/task-types"), map[string]any{
			"name": "Disabled type", "description": "Not available for new tasks", "enabled": true,
			"status_ids": []string{todoID}, "default_status_id": todoID,
		}, nil)
	typeCreateBody := assertStatus(t, typeCreateResponse, http.StatusCreated)
	disabledTypeID := stringField(t, typeCreateBody, "id")
	disabledTypeVersion := numberField(t, typeCreateBody, "version")
	typeDisableResponse := requestJSON(t, ownerClient, http.MethodPatch,
		route(harness, "/organizations/"+OrganizationA+"/spaces/00000000-0000-0000-0000-0000000000aa/task-types/"+disabledTypeID), map[string]any{
			"enabled": false, "version": disabledTypeVersion,
		}, nil)
	typeDisableBody := assertStatus(t, typeDisableResponse, http.StatusOK)
	var disabledType map[string]any
	if err := json.Unmarshal(typeDisableBody, &disabledType); err != nil {
		t.Fatal(err)
	}
	if enabled, ok := disabledType["enabled"].(bool); !ok || enabled {
		t.Fatalf("disabled task type response = %s", typeDisableBody)
	}
	disabledTaskResponse := requestJSON(t, ownerClient, http.MethodPost,
		route(harness, "/organizations/"+OrganizationA+"/spaces/00000000-0000-0000-0000-0000000000aa/tasks"), map[string]any{
			"type_id": disabledTypeID, "status_id": todoID, "title": "must reject disabled type",
		}, nil)
	assertError(t, disabledTaskResponse, http.StatusNotFound, "NOT_FOUND")
	terminalDefaultResponse := requestJSON(t, ownerClient, http.MethodPost,
		route(harness, "/organizations/"+OrganizationA+"/spaces/00000000-0000-0000-0000-0000000000aa/task-types"), map[string]any{
			"name": "Invalid terminal default", "status_ids": []string{todoID, doneID}, "default_status_id": doneID,
		}, nil)
	assertError(t, terminalDefaultResponse, http.StatusBadRequest, "INVALID_REQUEST")

	createResponse := requestJSON(t, ownerClient, http.MethodPost,
		route(harness, "/organizations/"+OrganizationA+"/spaces/00000000-0000-0000-0000-0000000000aa/tasks"), map[string]any{
			"type_id": types.TaskTypes[0].ID, "status_id": todoID, "title": "WP-04 integration task",
			"description": "space and task contract", "acceptance": "the task is isolated", "priority": "HIGH",
			"labels": []string{"phase-zero"},
		}, nil)
	createBody := assertStatus(t, createResponse, http.StatusCreated)
	var created map[string]any
	if err := json.Unmarshal(createBody, &created); err != nil {
		t.Fatal(err)
	}
	taskID := stringField(t, createBody, "id")
	if stringField(t, createBody, "display_id") == "" {
		t.Fatal("task display id is empty")
	}
	version, ok := created["version"].(float64)
	if !ok || version < 1 {
		t.Fatalf("task version = %#v", created["version"])
	}

	getResponse := requestJSON(t, ownerClient, http.MethodGet,
		route(harness, "/organizations/"+OrganizationA+"/spaces/00000000-0000-0000-0000-0000000000aa/tasks/"+taskID), nil, nil)
	assertStatus(t, getResponse, http.StatusOK)

	updateResponse := requestJSON(t, ownerClient, http.MethodPatch,
		route(harness, "/organizations/"+OrganizationA+"/spaces/00000000-0000-0000-0000-0000000000aa/tasks/"+taskID), map[string]any{
			"title": "WP-04 integration task updated", "version": uint64(version),
		}, nil)
	updatedBody := assertStatus(t, updateResponse, http.StatusOK)
	updated := map[string]any{}
	if err := json.Unmarshal(updatedBody, &updated); err != nil {
		t.Fatal(err)
	}
	updatedVersion, ok := updated["version"].(float64)
	if !ok || updatedVersion <= version {
		t.Fatalf("updated task version = %#v, previous=%v", updated["version"], version)
	}

	staleResponse := requestJSON(t, ownerClient, http.MethodPatch,
		route(harness, "/organizations/"+OrganizationA+"/spaces/00000000-0000-0000-0000-0000000000aa/tasks/"+taskID), map[string]any{
			"title": "stale update", "version": uint64(version),
		}, nil)
	assertError(t, staleResponse, http.StatusConflict, "VERSION_CONFLICT")

	foreignResponse := requestJSON(t, ownerClient, http.MethodGet,
		route(harness, "/organizations/"+OrganizationB+"/spaces/00000000-0000-0000-0000-0000000000bb/tasks"), nil, nil)
	assertError(t, foreignResponse, http.StatusNotFound, "NOT_FOUND")
}
