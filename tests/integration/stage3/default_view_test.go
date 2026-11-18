package stage3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
)

type defaultSpace struct {
	SpaceView
	DefaultViewID *string `json:"default_view_id"`
}

func defaultState(e *s3Env, space string) defaultSpace {
	var state defaultSpace
	e.owner().must(http.StatusOK, http.MethodGet, spacePath(space), nil, &state)
	return state
}

func defaultSource(e *s3Env, client *s3Client, space, visibility string) pvView {
	var v pvView
	client.must(http.StatusCreated, http.MethodPost, spacePath(space)+"/views", map[string]any{
		"name": "Default " + s3Suffix(e.t), "visibility": visibility,
		"filters": map[string]any{"priority": []string{"HIGH"}}, "display": map[string]any{"layout": "table", "completed": "last_7_days"},
	}, &v)
	return v
}

func setDefault(e *s3Env, space, view string) defaultSpace {
	state := defaultState(e, space)
	e.owner().must(http.StatusOK, http.MethodPatch, spacePath(space), map[string]any{"version": state.Version, "default_view_id": view}, &state)
	return state
}

func TestSpaceDefaultViewSettingsAndIsolation(t *testing.T) {
	e := newS3Env(t)
	o := e.owner()
	space := e.space.ID
	state := defaultState(e, space)
	if state.DefaultViewID != nil {
		t.Fatal("new space must have no default")
	}
	view := defaultSource(e, e.member(), space, "space")
	state = setDefault(e, space, view.ID)
	if state.DefaultViewID == nil || *state.DefaultViewID != view.ID {
		t.Fatal("default not persisted")
	}
	// Legacy partial writes do not clear a new optional field.
	o.must(http.StatusOK, http.MethodPatch, spacePath(space), map[string]any{"version": state.Version, "name": "Legacy PATCH"}, &state)
	if state.DefaultViewID == nil || *state.DefaultViewID != view.ID {
		t.Fatal("legacy PATCH cleared default")
	}
	personal := defaultSource(e, o, space, "personal")
	otherSpace := e.createSpace("private").ID
	foreign := defaultSource(e, o, otherSpace, "space")
	var crossTenant pvView
	e.outsider().must(http.StatusCreated, http.MethodPost, "/v1/organizations/"+e.otherOrg+"/"+spacePath(e.otherSpace.ID)+"/views", map[string]any{"name": "Other tenant", "visibility": "space"}, &crossTenant)
	for _, id := range []string{personal.ID, foreign.ID, crossTenant.ID, "bad", "00000000-0000-0000-0000-000000000001"} {
		o.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPatch, spacePath(space), map[string]any{"version": state.Version, "default_view_id": id})
	}
	e.member().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPatch, spacePath(space), map[string]any{"version": state.Version, "default_view_id": nil})
	e.addSpaceMember(space, s3GuestID, "VIEWER")
	e.guest().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPatch, spacePath(space), map[string]any{"version": state.Version, "default_view_id": nil})
	e.addSpaceMember(space, s3OtherID, "EDITOR")
	e.other().must(http.StatusOK, http.MethodPatch, spacePath(space), map[string]any{"version": state.Version, "default_view_id": view.ID}, &state)
	e.outsider().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, spacePath(space), nil)
	if _, err := e.superuser.Exec(e.ctx, `UPDATE openfox.spaces SET default_view_id=$2::uuid WHERE id=$1::uuid`, space, foreign.ID); err == nil {
		t.Fatal("same-space FK accepted foreign source")
	}
	// Disabling views suspends inheritance without erasing the setting.
	o.must(http.StatusOK, http.MethodPatch, spacePath(space), map[string]any{"version": state.Version, "features": map[string]any{"views": false}}, &state)
	if state.DefaultViewID != nil {
		t.Fatal("disabled views exposed a default")
	}
	o.must(http.StatusOK, http.MethodPatch, spacePath(space), map[string]any{"version": state.Version, "name": "Legacy disabled PATCH"}, &state)
	if state.DefaultViewID != nil {
		t.Fatal("legacy PATCH exposed disabled default")
	}
	o.expectError(http.StatusBadRequest, "VIEWS_DISABLED", http.MethodPatch, spacePath(space), map[string]any{"version": state.Version, "default_view_id": view.ID})
	o.must(http.StatusOK, http.MethodPatch, spacePath(space), map[string]any{"version": state.Version, "features": map[string]any{"views": true}}, &state)
	if state.DefaultViewID == nil || *state.DefaultViewID != view.ID {
		t.Fatal("reenabling lost saved default")
	}
	statuses := parallel(2, func(int) int {
		return o.do(http.MethodPatch, spacePath(space), map[string]any{"version": state.Version, "default_view_id": nil}).Status
	})
	if countStatus(statuses, http.StatusOK) != 1 || countStatus(statuses, http.StatusConflict) != 1 {
		t.Fatalf("CAS %v", statuses)
	}
	state = defaultState(e, space)
	err := completionTx(e, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
		id := view.ID
		_, err := (collaboration.Service{}).UpdateSpaceSettings(ctx, tx, e.org, space, actor, collaboration.SpacePatch{Version: uint64(state.Version), DefaultViewID: collaboration.OptionalString{Set: true, Value: &id}})
		if err != nil {
			return err
		}
		return errors.New("controlled rollback")
	})
	if err == nil || defaultState(e, space).DefaultViewID != nil {
		t.Fatal("failed transaction retained default")
	}
	// Copy configuration does not copy views or a cross-space UUID.
	setDefault(e, space, view.ID)
	var copied defaultSpace
	o.must(http.StatusCreated, http.MethodPost, "spaces", map[string]any{"name": "Copy " + s3Suffix(t), "copy_from_space_id": space}, &copied)
	if copied.DefaultViewID != nil {
		t.Fatal("configuration copy retained source default")
	}
}

func TestSpaceDefaultViewSourceChangesAndRaces(t *testing.T) {
	e := newS3Env(t)
	space := e.space.ID
	for _, operation := range []string{"personal", "delete"} {
		t.Run(operation, func(t *testing.T) {
			for i := 0; i < 3; i++ {
				v := defaultSource(e, e.member(), space, "space")
				state := setDefault(e, space, v.ID)
				results := parallel(2, func(index int) int {
					if index == 0 {
						return e.owner().do(http.MethodPatch, spacePath(space), map[string]any{"version": state.Version, "default_view_id": v.ID}).Status
					}
					if operation == "personal" {
						return e.member().do(http.MethodPatch, "views/"+v.ID, map[string]any{"version": v.Version, "visibility": "personal"}).Status
					}
					return e.member().do(http.MethodDelete, fmt.Sprintf("views/%s?version=%d", v.ID, v.Version), nil).Status
				})
				if results[0] != http.StatusOK && results[0] != http.StatusConflict {
					t.Fatalf("setter race: %v", results)
				}
				if results[1] != http.StatusOK && results[1] != http.StatusNoContent {
					t.Fatalf("author lost old permission: %v", results)
				}
				after := defaultState(e, space)
				if after.DefaultViewID != nil || after.Version <= state.Version {
					t.Fatalf("stale default: %+v", after)
				}
			}
		})
	}
	// Editing the source is live, and the existing query intersection stays on the server.
	v := defaultSource(e, e.member(), space, "space")
	setDefault(e, space, v.ID)
	e.createTask(e.owner(), space, map[string]any{"priority": "HIGH"})
	e.createTask(e.owner(), space, map[string]any{"priority": "LOW"})
	var count struct {
		Total int `json:"total"`
	}
	e.owner().must(http.StatusOK, http.MethodGet, spacePath(space)+"/tasks/counts?view_id="+v.ID+"&priority=LOW", nil, &count)
	if count.Total != 0 {
		t.Fatal("temporary filter did not intersect source")
	}
	e.member().must(http.StatusOK, http.MethodPatch, "views/"+v.ID, map[string]any{"version": v.Version, "filters": map[string]any{"priority": []string{"LOW"}}}, &v)
	e.owner().must(http.StatusOK, http.MethodGet, spacePath(space)+"/tasks/counts?view_id="+v.ID+"&priority=LOW", nil, &count)
	if count.Total != 1 {
		t.Fatal("source edit was not live")
	}
	// A legacy/raw visibility write cannot make even the creator inherit a personal view.
	e.exec(`UPDATE openfox.views SET visibility='personal' WHERE id=$1::uuid`, v.ID)
	if defaultState(e, space).DefaultViewID != nil {
		t.Fatal("invalid stored default leaked personal view")
	}
}
