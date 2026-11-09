package collaboration

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func batchID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func batchReason(err error) string {
	var rule *RuleError
	if !errors.As(err, &rule) || rule.Code != "INVALID_REQUEST" {
		return ""
	}
	reason, _ := rule.Details["reason"].(string)
	return reason
}

func TestNormalizeBatchInputRejectsBadShapes(t *testing.T) {
	status := batchID(900)
	priority := "high"
	one := []BatchTaskRef{{ID: batchID(1), Version: 1}}
	many := make([]BatchTaskRef, MaxBatchTasks+1)
	for i := range many {
		many[i] = BatchTaskRef{ID: batchID(i + 1), Version: 1}
	}
	null := OptionalString{Set: true}
	cases := map[string]struct {
		input  BatchTaskUpdateInput
		reason string
	}{
		"empty":        {BatchTaskUpdateInput{Patch: BatchTaskPatch{Priority: &priority}}, "task_count"},
		"too many":     {BatchTaskUpdateInput{Tasks: many, Patch: BatchTaskPatch{Priority: &priority}}, "task_count"},
		"bad id":       {BatchTaskUpdateInput{Tasks: []BatchTaskRef{{ID: "x", Version: 1}}, Patch: BatchTaskPatch{Priority: &priority}}, "task_ref"},
		"zero version": {BatchTaskUpdateInput{Tasks: []BatchTaskRef{{ID: batchID(1)}}, Patch: BatchTaskPatch{Priority: &priority}}, "task_ref"},
		"duplicate": {BatchTaskUpdateInput{Tasks: []BatchTaskRef{{ID: batchID(1), Version: 1}, {ID: batchID(1), Version: 2}},
			Patch: BatchTaskPatch{Priority: &priority}}, "duplicate_task"},
		"no field":   {BatchTaskUpdateInput{Tasks: one}, "patch_fields"},
		"two fields": {BatchTaskUpdateInput{Tasks: one, Patch: BatchTaskPatch{Priority: &priority, StatusID: &status}}, "patch_fields"},
		"bad priority": {BatchTaskUpdateInput{Tasks: one, Patch: BatchTaskPatch{Priority: func() *string { v := "SOON"; return &v }()}},
			"priority"},
		"assignee kind": {BatchTaskUpdateInput{Tasks: one, Patch: BatchTaskPatch{Assignee: &BatchAssigneePatch{Kind: "TEAM", ID: null}}}, "assignee"},
		"assignee id missing": {BatchTaskUpdateInput{Tasks: one, Patch: BatchTaskPatch{Assignee: &BatchAssigneePatch{Kind: "AGENT"}}},
			"assignee"},
		"labels empty": {BatchTaskUpdateInput{Tasks: one, Patch: BatchTaskPatch{Labels: &BatchLabelPatch{}}}, "labels"},
		"labels overlap": {BatchTaskUpdateInput{Tasks: one, Patch: BatchTaskPatch{Labels: &BatchLabelPatch{
			Add: []string{batchID(500)}, Remove: []string{batchID(500)}}}}, "labels"},
	}
	for name, tc := range cases {
		input := tc.input
		if _, err := normalizeBatchInput(&input); batchReason(err) != tc.reason {
			t.Errorf("%s: err = %v, want reason %s", name, err, tc.reason)
		}
	}
}

func TestNormalizeBatchInputSortsAndNormalizes(t *testing.T) {
	priority := "high"
	input := BatchTaskUpdateInput{Tasks: []BatchTaskRef{{ID: batchID(3), Version: 2}, {ID: batchID(1), Version: 5}},
		Patch: BatchTaskPatch{Priority: &priority}}
	sorted, err := normalizeBatchInput(&input)
	if err != nil {
		t.Fatal(err)
	}
	if sorted[0].ID != batchID(1) || sorted[1].ID != batchID(3) || *input.Patch.Priority != "HIGH" {
		t.Fatalf("sorted = %+v, priority = %s", sorted, *input.Patch.Priority)
	}
	// 请求顺序保持不变（响应按请求顺序返回）
	if input.Tasks[0].ID != batchID(3) {
		t.Fatalf("request order changed: %+v", input.Tasks)
	}
}

func TestReplaceAssigneeKind(t *testing.T) {
	current := []TaskAssignee{{Kind: "HUMAN", ID: "h1"}, {Kind: "AGENT", ID: "a1"}, {Kind: "AGENT", ID: "a2"}, {Kind: "CREW", ID: "c1"}}
	refs := func(pairs ...string) []TaskAssigneeRef {
		out := []TaskAssigneeRef{}
		for i := 0; i < len(pairs); i += 2 {
			out = append(out, TaskAssigneeRef{Kind: pairs[i], ID: pairs[i+1]})
		}
		return out
	}
	cases := []struct {
		kind, id string
		want     []TaskAssigneeRef
	}{
		{"AGENT", "a3", refs("HUMAN", "h1", "AGENT", "a3", "CREW", "c1")},
		{"AGENT", "", refs("HUMAN", "h1", "CREW", "c1")},
		{"HUMAN", "h1", refs("HUMAN", "h1", "AGENT", "a1", "AGENT", "a2", "CREW", "c1")},
		{"HUMAN", "h2", refs("HUMAN", "h2", "AGENT", "a1", "AGENT", "a2", "CREW", "c1")},
	}
	for _, tc := range cases {
		if got := replaceAssigneeKind(current, tc.kind, tc.id); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s=%s: got %+v, want %+v", tc.kind, tc.id, got, tc.want)
		}
	}
	// 该类本来就没有、要求清空：原样返回
	if got := replaceAssigneeKind(current[:1], "AGENT", ""); !reflect.DeepEqual(got, refs("HUMAN", "h1")) {
		t.Errorf("clear absent kind: %+v", got)
	}
}

func TestApplyLabelPatch(t *testing.T) {
	current := []TaskLabelItem{{ID: "l1"}, {ID: "l2"}}
	if got := applyLabelPatch(current, []string{"l2", "l3"}, []string{"l1"}); !reflect.DeepEqual(got, []string{"l2", "l3"}) {
		t.Fatalf("got %v", got)
	}
	if got := applyLabelPatch(nil, nil, []string{"l1"}); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
