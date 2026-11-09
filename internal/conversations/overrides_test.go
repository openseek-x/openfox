package conversations

import (
	"encoding/json"
	"strings"
	"testing"
)

// 请求体里「字段缺省」「null」「取值」三种情形必须可区分，且不接受电脑 / 授权字段（API 层 DisallowUnknownFields）。
func TestOverridesPatchDecoding(t *testing.T) {
	var patch OverridesPatch
	if err := json.Unmarshal([]byte(`{"runtime":null,"model":"sonnet","expected_version":3}`), &patch); err != nil {
		t.Fatal(err)
	}
	if !patch.Runtime.Set || patch.Runtime.Value != nil || !patch.Model.Set || *patch.Model.Value != "sonnet" ||
		patch.ApprovalMode.Set || patch.ExpectedVersion == nil || *patch.ExpectedVersion != 3 {
		t.Fatalf("patch = %+v", patch)
	}
	var wrongType OverridesPatch
	if err := json.Unmarshal([]byte(`{"model":5,"expected_version":1}`), &wrongType); err == nil {
		t.Fatal("non-string model accepted")
	}
	decoder := json.NewDecoder(stringsReader(`{"computer_id":"x","expected_version":1}`))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&OverridesPatch{}); err == nil {
		t.Fatal("computer_id must not be an accepted field")
	}
}

func stringsReader(value string) *strings.Reader { return strings.NewReader(value) }
