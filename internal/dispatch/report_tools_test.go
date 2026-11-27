package dispatch

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// OPE-19：task_comment 的 artifact_ids 参数解析（未提供 / 格式错误 / 去重 / 上限）。

func reportTestUUID(n int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012x", n)
}

func reportTestArguments(t *testing.T, ids []string) json.RawMessage {
	t.Helper()
	list, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	return json.RawMessage(fmt.Sprintf(`{"body":"报告","artifact_ids":%s}`, list))
}

func TestReportArtifactIDsAbsent(t *testing.T) {
	cases := map[string]json.RawMessage{
		"空参数":        nil,
		"null":       json.RawMessage(`null`),
		"无该字段":       json.RawMessage(`{"body":"x"}`),
		"字段为 null":   json.RawMessage(`{"artifact_ids":null}`),
		"不是 JSON 对象": json.RawMessage(`not json`),
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			ids, present, problem := ReportArtifactIDs(arguments)
			if present || ids != nil || problem != "" {
				t.Fatalf("got ids=%v present=%v problem=%q, want absent", ids, present, problem)
			}
		})
	}
}

func TestReportArtifactIDsNormalizes(t *testing.T) {
	arguments := json.RawMessage(`{"artifact_ids":[" 00000000-0000-0000-0000-0000000000A1 ","00000000-0000-0000-0000-0000000000b2"]}`)
	ids, present, problem := ReportArtifactIDs(arguments)
	if !present || problem != "" {
		t.Fatalf("present=%v problem=%q, want present without problem", present, problem)
	}
	want := []string{"00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000b2"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestReportArtifactIDsLimit(t *testing.T) {
	atLimit := make([]string, MaxReportArtifacts)
	for i := range atLimit {
		atLimit[i] = reportTestUUID(i + 1)
	}
	ids, present, problem := ReportArtifactIDs(reportTestArguments(t, atLimit))
	if !present || problem != "" || len(ids) != MaxReportArtifacts {
		t.Fatalf("at limit: len=%d present=%v problem=%q, want %d accepted", len(ids), present, problem, MaxReportArtifacts)
	}
	overLimit := append(append([]string{}, atLimit...), reportTestUUID(MaxReportArtifacts+1))
	ids, present, problem = ReportArtifactIDs(reportTestArguments(t, overLimit))
	if !present || ids != nil || problem == "" {
		t.Fatalf("over limit: ids=%v present=%v problem=%q, want rejected", ids, present, problem)
	}
}

func TestReportArtifactIDsRejectsMalformed(t *testing.T) {
	id := reportTestUUID(1)
	cases := map[string]json.RawMessage{
		"空列表":       json.RawMessage(`{"artifact_ids":[]}`),
		"不是列表":      json.RawMessage(`{"artifact_ids":"x"}`),
		"元素不是字符串":   json.RawMessage(`{"artifact_ids":[1,2]}`),
		"不是 UUID":   json.RawMessage(`{"artifact_ids":["not-a-uuid"]}`),
		"重复":        reportTestArguments(t, []string{id, id}),
		"仅大小写不同的重复": reportTestArguments(t, []string{id, strings.ToUpper(id)}),
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			ids, present, problem := ReportArtifactIDs(arguments)
			if !present || ids != nil || problem == "" {
				t.Fatalf("got ids=%v present=%v problem=%q, want present with a problem", ids, present, problem)
			}
		})
	}
}
