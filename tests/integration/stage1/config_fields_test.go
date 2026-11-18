package stage1

import (
	"encoding/json"
	"net/http"
	"testing"
)

type cfgField struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Kind            string          `json:"kind"`
	DefaultValue    json.RawMessage `json:"default_value"`
	MemberScope     *string         `json:"member_scope"`
	UsedByTypeCount int             `json:"used_by_type_count"`
	CreatedBy       *struct {
		UserID string `json:"user_id"`
	} `json:"created_by"`
	ArchivedAt *string `json:"archived_at"`
	Version    int     `json:"version"`
	Options    []struct {
		ID       string `json:"id"`
		Label    string `json:"label"`
		Position int    `json:"position"`
		Version  int    `json:"version"`
	} `json:"options"`
}

// 自定义字段：默认值按类型校验、选项、绑定与已绑定删除 409、组织级任务类型列表的可见性。
func TestConfigCustomFields(t *testing.T) {
	env := newCfgEnv(t)
	shared := env.createSpace("shared")
	private := env.createSpace("private")
	create := func(body map[string]any) cfgField {
		t.Helper()
		body["name"] = "Field " + cfgSuffix(t)
		var field cfgField
		env.owner.must(http.StatusCreated, http.MethodPost, "custom-fields", body, &field)
		return field
	}

	env.member.expectError(http.StatusForbidden, "", http.MethodPost, "custom-fields",
		map[string]any{"name": "nope", "kind": "TEXT"})
	env.outsider.expectError(http.StatusNotFound, "", http.MethodGet, "custom-fields", nil)

	// 默认值按类型校验（非法 → 400）。
	twoOptions := []map[string]any{{"label": "A"}, {"label": "B", "color": "#00ff00"}}
	for _, invalid := range []map[string]any{
		{"kind": "TEXT", "default_value": 123},
		{"kind": "TEXT", "default_value": string(make([]byte, 257))},
		{"kind": "NUMBER", "default_value": "12"},
		{"kind": "BOOLEAN", "default_value": "true"},
		{"kind": "DATE", "default_value": "2026-13-01"},
		{"kind": "URL", "default_value": "ftp://example.test/file"},
		{"kind": "URL", "default_value": "javascript:alert(1)"},
		{"kind": "SELECT", "options": twoOptions, "default_value": map[string]any{"option_index": 2}},
		{"kind": "MULTI_SELECT", "options": twoOptions, "default_value": map[string]any{"option_indexes": []int{0, 5}}},
		{"kind": "MEMBER", "member_scope": "HUMANS", "default_value": map[string]any{"kind": "AGENT", "id": cfgMemberID}},
		{"kind": "MEMBER", "default_value": map[string]any{"kind": "HUMAN", "id": cfgOutsiderID}},
		{"kind": "TEXT", "member_scope": "ALL"},
		{"kind": "TEXT", "options": twoOptions},
		{"kind": "SELECT", "options": []map[string]any{{"label": "Same"}, {"label": "same"}}},
		{"kind": "COLOR"},
	} {
		invalid["name"] = "Bad " + cfgSuffix(t)
		env.owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "custom-fields", invalid)
	}

	// 合法默认值：规范化后存储；SELECT 的下标引用换成选项 id。
	number := create(map[string]any{"kind": "NUMBER", "default_value": 3.5})
	if string(number.DefaultValue) != "3.5" || number.CreatedBy == nil || number.CreatedBy.UserID != cfgOwnerID {
		t.Fatalf("number field = %+v %s", number, number.DefaultValue)
	}
	choice := create(map[string]any{"kind": "SELECT", "options": twoOptions, "default_value": map[string]any{"option_index": 1}})
	if len(choice.Options) != 2 || string(choice.DefaultValue) != `"`+choice.Options[1].ID+`"` {
		t.Fatalf("select field default = %s options = %+v", choice.DefaultValue, choice.Options)
	}
	member := create(map[string]any{"kind": "MEMBER", "default_value": map[string]any{"kind": "human", "id": cfgMemberID}})
	if member.MemberScope == nil || *member.MemberScope != "ALL" {
		t.Fatalf("member field scope = %v", member.MemberScope)
	}
	text := create(map[string]any{"kind": "TEXT"})
	if string(text.DefaultValue) != "null" || text.MemberScope != nil || len(text.Options) != 0 {
		t.Fatalf("text field = %+v %s", text, text.DefaultValue)
	}

	// PATCH：kind 不可改（未知字段 400），默认值按类型校验，可显式 null 清空。
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPatch, "custom-fields/"+number.ID,
		map[string]any{"kind": "TEXT", "version": number.Version})
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPatch, "custom-fields/"+number.ID,
		map[string]any{"default_value": "abc", "version": number.Version})
	var patched cfgField
	env.owner.must(http.StatusOK, http.MethodPatch, "custom-fields/"+number.ID,
		`{"default_value":null,"version":1}`, &patched)
	if string(patched.DefaultValue) != "null" || patched.Version != 2 {
		t.Fatalf("cleared default = %s v%d", patched.DefaultValue, patched.Version)
	}
	env.owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, "custom-fields/"+number.ID,
		map[string]any{"name": "Stale", "version": 1})

	// 选项：追加、重排（重编号连续）、删除默认值选项时清空默认值。
	var option struct {
		ID       string `json:"id"`
		Position int    `json:"position"`
		Version  int    `json:"version"`
	}
	env.owner.must(http.StatusCreated, http.MethodPost, "custom-fields/"+choice.ID+"/options",
		map[string]any{"label": "C"}, &option)
	if option.Position != 2 {
		t.Fatalf("appended option position = %d", option.Position)
	}
	env.owner.expectError(http.StatusConflict, "NAME_TAKEN", http.MethodPost, "custom-fields/"+choice.ID+"/options",
		map[string]any{"label": "a"})
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPost, "custom-fields/"+text.ID+"/options",
		map[string]any{"label": "x"})
	env.owner.must(http.StatusOK, http.MethodPatch, "custom-fields/"+choice.ID+"/options/"+option.ID,
		map[string]any{"position": 0, "version": option.Version}, &option)
	var field cfgField
	env.owner.must(http.StatusOK, http.MethodPatch, "custom-fields/"+choice.ID,
		map[string]any{"description": "reordered", "version": choice.Version}, &field)
	if len(field.Options) != 3 || field.Options[0].ID != option.ID {
		t.Fatalf("options after reorder = %+v", field.Options)
	}
	for index, item := range field.Options {
		if item.Position != index {
			t.Fatalf("option positions not contiguous: %+v", field.Options)
		}
	}
	env.owner.must(http.StatusNoContent, http.MethodDelete, "custom-fields/"+choice.ID+"/options/"+choice.Options[1].ID, nil, nil)
	var stored *string
	env.scalar(&stored, `SELECT default_value::text FROM openfox.custom_fields WHERE id = $1::uuid`, choice.ID)
	if stored != nil {
		t.Fatalf("default not cleared after deleting its option: %s", *stored)
	}
	env.owner.expectError(http.StatusNotFound, "", http.MethodDelete, "custom-fields/"+choice.ID+"/options/"+choice.Options[1].ID, nil)

	// 绑定：需空间管理权限；幂等；已绑定字段删除 → 409；解绑后可删除（归档）。
	bind := "spaces/" + shared.ID + "/task-types/" + shared.TypeID + "/fields/" + choice.ID
	env.member.expectError(http.StatusForbidden, "", http.MethodPut, bind, nil)
	env.owner.must(http.StatusNoContent, http.MethodPut, bind, nil, nil)
	env.owner.must(http.StatusNoContent, http.MethodPut, bind, nil, nil)
	env.owner.must(http.StatusNoContent, http.MethodPut, "spaces/"+shared.ID+"/task-types/"+shared.TypeID+"/fields/"+text.ID, nil, nil)
	var bound struct {
		CustomFields []cfgField `json:"custom_fields"`
	}
	env.member.must(http.StatusOK, http.MethodGet, "spaces/"+shared.ID+"/task-types/"+shared.TypeID+"/fields", nil, &bound)
	if len(bound.CustomFields) != 2 || bound.CustomFields[0].ID != choice.ID || bound.CustomFields[0].UsedByTypeCount != 1 {
		t.Fatalf("bound fields = %+v", bound.CustomFields)
	}
	env.owner.expectError(http.StatusConflict, "CUSTOM_FIELD_IN_USE", http.MethodDelete, "custom-fields/"+choice.ID, nil)
	env.owner.must(http.StatusNoContent, http.MethodDelete, bind, nil, nil)
	env.owner.must(http.StatusNoContent, http.MethodDelete, "custom-fields/"+choice.ID, nil, nil)
	env.owner.expectError(http.StatusNotFound, "", http.MethodPut, bind, nil)
	var listed struct {
		CustomFields []cfgField `json:"custom_fields"`
	}
	env.owner.must(http.StatusOK, http.MethodGet, "custom-fields", nil, &listed)
	for _, item := range listed.CustomFields {
		if item.ID == choice.ID {
			t.Fatal("archived field is listed by default")
		}
	}
	env.owner.must(http.StatusOK, http.MethodGet, "custom-fields?include_archived=true", nil, &listed)
	found := false
	for _, item := range listed.CustomFields {
		if item.ID == choice.ID && item.ArchivedAt != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("archived field missing with include_archived=true")
	}

	// 组织级任务类型：只列出调用者可读的空间（成员看不到未加入的私有空间）。
	type orgTypes struct {
		Spaces []struct {
			SpaceID   string `json:"space_id"`
			TaskTypes []struct {
				ID       string   `json:"id"`
				FieldIDs []string `json:"field_ids"`
			} `json:"task_types"`
		} `json:"spaces"`
	}
	var ownerView, memberView orgTypes
	env.owner.must(http.StatusOK, http.MethodGet, "task-types", nil, &ownerView)
	env.member.must(http.StatusOK, http.MethodGet, "task-types", nil, &memberView)
	spaceIDs := func(view orgTypes) map[string][]string {
		result := map[string][]string{}
		for _, space := range view.Spaces {
			for _, item := range space.TaskTypes {
				result[space.SpaceID] = append(result[space.SpaceID], item.FieldIDs...)
			}
			if _, ok := result[space.SpaceID]; !ok {
				result[space.SpaceID] = []string{}
			}
		}
		return result
	}
	ownerSpaces, memberSpaces := spaceIDs(ownerView), spaceIDs(memberView)
	if _, ok := ownerSpaces[private.ID]; !ok {
		t.Fatalf("owner (private space member) does not see private space: %v", ownerSpaces)
	}
	if _, ok := memberSpaces[private.ID]; ok {
		t.Fatalf("member sees unreadable private space: %v", memberSpaces)
	}
	if fields := memberSpaces[shared.ID]; len(fields) != 1 || fields[0] != text.ID {
		t.Fatalf("shared space field ids = %v", fields)
	}
	// 不可读空间的字段列表与绑定也被拒绝（私有空间对非成员隐藏 → 404）。
	env.member.expectError(http.StatusNotFound, "", http.MethodGet,
		"spaces/"+private.ID+"/task-types/"+private.TypeID+"/fields", nil)
}

// 关联类型：内置在前且不可改删，自定义类型的增删改。
func TestConfigRelationTypes(t *testing.T) {
	env := newCfgEnv(t)
	type relationType struct {
		ID        string  `json:"id"`
		Builtin   bool    `json:"builtin"`
		Key       *string `json:"key"`
		Name      string  `json:"name"`
		Symmetric bool    `json:"symmetric"`
		Version   *int    `json:"version"`
		CreatedAt *string `json:"created_at"`
	}
	var list struct {
		RelationTypes []relationType `json:"relation_types"`
	}
	env.member.must(http.StatusOK, http.MethodGet, "task-relation-types", nil, &list)
	if len(list.RelationTypes) != 4 || list.RelationTypes[0].ID != "builtin:BLOCKS" || !list.RelationTypes[3].Symmetric ||
		list.RelationTypes[0].Version != nil || list.RelationTypes[0].CreatedAt != nil || list.RelationTypes[0].Name != "" {
		t.Fatalf("builtin relation types = %+v", list.RelationTypes)
	}
	env.member.expectError(http.StatusForbidden, "", http.MethodPost, "task-relation-types",
		map[string]any{"name": "x", "outward_label": "a", "inward_label": "b"})
	env.owner.expectError(http.StatusConflict, "RELATION_TYPE_BUILTIN", http.MethodPatch, "task-relation-types/builtin:BLOCKS",
		map[string]any{"name": "x", "version": 1})
	env.owner.expectError(http.StatusConflict, "RELATION_TYPE_BUILTIN", http.MethodDelete, "task-relation-types/builtin:RELATES", nil)
	env.owner.expectError(http.StatusNotFound, "", http.MethodDelete, "task-relation-types/builtin:NOPE", nil)
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPost, "task-relation-types",
		map[string]any{"name": "   ", "outward_label": "a", "inward_label": "b"})

	var custom relationType
	env.owner.must(http.StatusCreated, http.MethodPost, "task-relation-types",
		map[string]any{"name": " Implements ", "outward_label": "implements", "inward_label": "implemented by"}, &custom)
	if custom.Builtin || custom.Key != nil || custom.Name != "Implements" || custom.Version == nil || *custom.Version != 1 {
		t.Fatalf("custom relation type = %+v", custom)
	}
	env.owner.expectError(http.StatusConflict, "NAME_TAKEN", http.MethodPost, "task-relation-types",
		map[string]any{"name": "implements", "outward_label": "a", "inward_label": "b"})
	env.owner.must(http.StatusOK, http.MethodPatch, "task-relation-types/"+custom.ID,
		map[string]any{"inward_label": "realized by", "version": 1}, &custom)
	env.owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, "task-relation-types/"+custom.ID,
		map[string]any{"name": "Stale", "version": 1})
	env.member.must(http.StatusOK, http.MethodGet, "task-relation-types", nil, &list)
	if len(list.RelationTypes) != 5 || list.RelationTypes[4].ID != custom.ID {
		t.Fatalf("relation types after create = %+v", list.RelationTypes)
	}
	env.owner.must(http.StatusNoContent, http.MethodDelete, "task-relation-types/"+custom.ID, nil, nil)
	env.owner.expectError(http.StatusNotFound, "", http.MethodDelete, "task-relation-types/"+custom.ID, nil)
	// 跨租户：用本组织路径访问别组织的自定义类型 → 404。
	var foreign relationType
	env.outsider.doIn(env.otherOrg, http.MethodPost, "task-relation-types",
		map[string]any{"name": "Foreign", "outward_label": "a", "inward_label": "b"}).decode(t, &foreign)
	env.owner.expectError(http.StatusNotFound, "", http.MethodPatch, "task-relation-types/"+foreign.ID,
		map[string]any{"name": "steal", "version": 1})
}
