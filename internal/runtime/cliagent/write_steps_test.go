package cliagent

import (
	"strings"
	"testing"

	"openfox/internal/runtime"
)

// 写入 / 编辑步骤要带上内容或 diff，前端展开时才有东西可看。

func TestWriteStepKeepsContentThroughLifecycle(t *testing.T) {
	s := newSession(protocol{}, "/w", func(s string) string { return strings.ReplaceAll(s, "known-secret", "[REDACTED]") })
	frames := []string{
		j(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": "w1", "name": "Write", "input": map[string]any{"file_path": "/w/app/main.go", "content": "package main\n// known-secret\n"}},
		}}}),
		j(map[string]any{"type": "user", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "w1", "content": "ok"},
		}}}),
	}
	var steps []runtime.Step
	for _, frame := range frames {
		events, _ := parseClaude(s, []byte(frame))
		for _, event := range events {
			if event.Type == runtime.EventStep {
				steps = append(steps, decodeToolStep(t, event))
			}
		}
	}
	if len(steps) != 2 {
		t.Fatalf("steps = %+v", steps)
	}
	for _, step := range steps {
		if step.Kind != runtime.StepKindWriteFile || step.Path != "app/main.go" || step.Content != "package main\n// [REDACTED]\n" || step.Truncated {
			t.Fatalf("write step = %+v", step)
		}
	}
	if done := steps[1]; done.State != runtime.StepDone || done.Applied == nil || !*done.Applied {
		t.Fatalf("finished = %+v", done)
	}
}

func TestEditStepsBecomePatchWithDiff(t *testing.T) {
	s := newSession(protocol{}, "/w", func(s string) string { return s })
	cases := []struct {
		name, tool string
		input      map[string]any
		diff       string
		add, del   int
	}{
		{"claude edit", "Edit", map[string]any{"file_path": "/w/a.go", "old_string": "a := 1\n", "new_string": "a := 2\nb := 3"}, "@@\n-a := 1\n+a := 2\n+b := 3\n", 2, 1},
		{"opencode edit", "edit", map[string]any{"filePath": "/w/a.go", "oldString": "x", "newString": ""}, "@@\n-x\n", 0, 1},
		{"pi edit", "edit", map[string]any{"path": "a.go", "oldText": "", "newText": "y"}, "@@\n+y\n", 1, 0},
		{"multiedit", "MultiEdit", map[string]any{"file_path": "/w/a.go", "edits": []any{
			map[string]any{"old_string": "one", "new_string": "1"},
			map[string]any{"old_string": "two", "new_string": "2"},
		}}, "@@\n-one\n+1\n@@\n-two\n+2\n", 2, 2},
		{"pi edits", "edit", map[string]any{"path": "a.go", "edits": []any{map[string]any{"oldText": "-- sql", "newText": "--- keep"}}}, "@@\n--- sql\n+--- keep\n", 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step := s.toolStep(tc.tool, tc.input)
			if step.Kind != runtime.StepKindPatch || len(step.Files) != 1 {
				t.Fatalf("step = %+v", step)
			}
			file := step.Files[0]
			if file.Path != "a.go" || file.Kind != "update" || file.Diff != tc.diff || file.Additions != tc.add || file.Deletions != tc.del || file.Truncated {
				t.Fatalf("file = %+v", file)
			}
		})
	}
	// 取不到替换内容时退回只带路径的写入步骤
	step := s.toolStep("Edit", map[string]any{"file_path": "/w/a.go"})
	if step.Kind != runtime.StepKindWriteFile || step.Path != "a.go" || len(step.Files) != 0 {
		t.Fatalf("fallback = %+v", step)
	}
}

func TestEditPatchSurvivesStartAndFinish(t *testing.T) {
	s := newSession(protocol{}, "/w", func(s string) string { return s })
	input := map[string]any{"path": "a.go", "oldText": "old", "newText": "new"}
	s.startStep("e1", s.toolStep("edit", input))
	finished := s.finishStep("e1", s.toolStep("edit", map[string]any{}), runtime.StepFailed, "no match", nil)
	step := decodeToolStep(t, finished[0])
	if step.Kind != runtime.StepKindPatch || step.State != runtime.StepFailed || len(step.Files) != 1 || step.Files[0].Diff != "@@\n-old\n+new\n" {
		t.Fatalf("finished = %+v", step)
	}
}

func TestPatchToolShowsPatchText(t *testing.T) {
	s := newSession(protocol{}, "/w", func(s string) string { return s })
	patch := "*** Begin Patch\n*** Update File: src/app.ts\n@@\n-a\n+b\n*** End Patch"
	step := s.toolStep("patch", map[string]any{"patchText": patch})
	if step.Kind != runtime.StepKindWriteFile || step.Path != "src/app.ts" || step.Content != patch {
		t.Fatalf("patch = %+v", step)
	}
	step = s.toolStep("NotebookEdit", map[string]any{"notebook_path": "/w/n.ipynb", "new_source": "print(1)"})
	if step.Path != "n.ipynb" || step.Content != "print(1)" {
		t.Fatalf("notebook = %+v", step)
	}
}

func TestLargeWriteAndEditStayWithinEventLimit(t *testing.T) {
	s := newSession(protocol{}, "/w", func(s string) string { return s })
	// 引号在 JSON 里会转义成两个字节，内容上限之内也可能超过事件上限，需要继续缩减
	big := strings.Repeat("\"\n", 20*1024)
	write := s.toolStep("Write", map[string]any{"file_path": "/w/big.txt", "content": big + strings.Repeat("x", 30*1024)})
	if len(write.Content) != stepContentLimit || !write.Truncated {
		t.Fatalf("write bound = %d %v", len(write.Content), write.Truncated)
	}
	edit := s.toolStep("Edit", map[string]any{"file_path": "/w/big.txt", "old_string": big, "new_string": big})
	for _, step := range []runtime.Step{write, edit} {
		events := s.startStep("big-"+step.Kind, step)
		if len(events) != 1 || len(events[0].Payload) > stepPayloadLimit {
			t.Fatalf("%s payload = %d", step.Kind, len(events[0].Payload))
		}
		decoded := decodeToolStep(t, events[0])
		truncated := decoded.Truncated && decoded.Content != ""
		if step.Kind == runtime.StepKindPatch {
			truncated = len(decoded.Files) == 1 && decoded.Files[0].Diff != "" && decoded.Files[0].Truncated
		}
		if !truncated {
			t.Fatalf("%s decoded truncated = %v", step.Kind, decoded.Truncated)
		}
	}
}
