package stage3

import (
	"net/http"
	"strings"
	"testing"
)

// S01: the UI reset patch must preserve unrelated values and persist across sessions.
func TestHiFoxThemeResetAcrossSessions(t *testing.T) {
	env := newS3Env(t)
	user := env.newUser()
	first := env.client(user)
	second := secondClient(env, user)
	other := env.client(env.newUser())
	var prefs profilePrefs
	first.must(http.StatusOK, http.MethodPatch, "/v1/me/preferences", map[string]any{
		"version": 0, "theme": "magic-blue", "outline_color": 3, "density": "compact", "locale": "zh-CN",
		"open_session_on_assign": false, "open_session_on_quick_command": false, "send_shortcut": "shift_enter",
		"command_shortcuts": map[string]string{"find_in_view": "mod+g"},
	}, &prefs)
	first.must(http.StatusOK, http.MethodPatch, "/v1/me/preferences", map[string]any{
		"version": prefs.Version, "theme": "system", "outline_color": 0,
	}, &prefs)
	second.must(http.StatusOK, http.MethodGet, "/v1/me/preferences", nil, &prefs)
	if prefs.Version != 2 || prefs.Theme != "system" || prefs.OutlineColor != 0 || prefs.Density != "compact" ||
		prefs.Locale == nil || *prefs.Locale != "zh-CN" || prefs.OpenSessionOnAssign || prefs.OpenSessionOnQuickCommand ||
		prefs.SendShortcut != "shift_enter" || prefs.CommandShortcuts["find_in_view"] != "mod+g" {
		t.Fatalf("reset lost unrelated fields or persistence: %+v", prefs)
	}
	first.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, "/v1/me/preferences", map[string]any{
		"version": 1, "theme": "dark", "outline_color": 4,
	})
	other.must(http.StatusOK, http.MethodGet, "/v1/me/preferences", nil, &prefs)
	if prefs.Version != 0 || prefs.Theme != "system" {
		t.Fatalf("other account changed: %+v", prefs)
	}
}

// S05: exercise existing authorization, persistence, validation and version contract.
func TestHiFoxTaskTypeIcon(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	var taskType struct {
		ID      string `json:"id"`
		Icon    string `json:"icon"`
		Version int    `json:"version"`
	}
	base := spacePath(env.space.ID) + "/task-types"
	owner.must(http.StatusCreated, http.MethodPost, base, map[string]any{"name": "Parity icon", "icon": "package"}, &taskType)
	path := base + "/" + taskType.ID
	patch := map[string]any{"version": taskType.Version, "icon": "bug"}
	env.member().expectError(http.StatusForbidden, "", http.MethodPatch, path, patch)
	env.other().expectError(http.StatusNotFound, "", http.MethodPatch, path, patch)
	env.client(s3OutsiderID).expectError(http.StatusNotFound, "", http.MethodPatch, path, patch)
	owner.must(http.StatusOK, http.MethodPatch, path, patch, &taskType)
	if taskType.Icon != "bug" || taskType.Version != 2 {
		t.Fatalf("saved icon: %+v", taskType)
	}
	owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, path, patch)
	for _, icon := range []string{strings.Repeat("x", 65), "invalid\nicon", "icon\x00name"} {
		owner.expectError(http.StatusBadRequest, "", http.MethodPatch, path, map[string]any{"version": 2, "icon": icon})
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_types WHERE id=$1::uuid AND icon='bug' AND version=2`, taskType.ID); count != 1 {
		t.Fatalf("icon was not persisted: %d", count)
	}
	var listing struct {
		TaskTypes []struct {
			ID   string `json:"id"`
			Icon string `json:"icon"`
		} `json:"task_types"`
	}
	env.member().must(http.StatusOK, http.MethodGet, base, nil, &listing)
	found := false
	for _, row := range listing.TaskTypes {
		if row.ID == taskType.ID {
			found = row.Icon == "bug"
		}
	}
	if !found {
		t.Fatal("task creation type catalog did not return saved icon")
	}
	task := env.createTask(owner, env.space.ID, map[string]any{"type_id": taskType.ID})
	var detail struct {
		TypeIcon string `json:"type_icon"`
	}
	owner.must(http.StatusOK, http.MethodGet, spacePath(env.space.ID)+"/tasks/"+task.ID, nil, &detail)
	if detail.TypeIcon != "bug" {
		t.Fatalf("task detail icon = %q", detail.TypeIcon)
	}
}
