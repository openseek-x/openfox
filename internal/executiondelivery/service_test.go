package executiondelivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeEventCompactsPayloadAndPreservesHash(t *testing.T) {
	input, hash, err := normalizeEvent(EventInput{
		ID: "00000000-0000-0000-0000-000000000001", Generation: 2, Sequence: 1,
		Type: "run.output", Payload: json.RawMessage(`{ "text": "hello" }`),
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(input.Payload) != `{"text":"hello"}` {
		t.Fatalf("payload = %s", input.Payload)
	}
	digest := sha256.Sum256(input.Payload)
	if hash != hex.EncodeToString(digest[:]) {
		t.Fatalf("hash = %q", hash)
	}
}

func TestNormalizeResultRejectsDuplicateArtifactsAndConflictingHash(t *testing.T) {
	input := ResultInput{
		ID: "00000000-0000-0000-0000-000000000001", Generation: 1, Status: "SUCCEEDED",
		Checks: json.RawMessage(`[]`), ArtifactIDs: []string{
			"00000000-0000-0000-0000-000000000002",
			"00000000-0000-0000-0000-000000000002",
		},
	}
	if _, _, err := normalizeResult(input, 1); err != ErrInvalidRequest {
		t.Fatalf("duplicate artifact error = %v", err)
	}
	input.ArtifactIDs = input.ArtifactIDs[:1]
	input.PayloadHash = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, _, err := normalizeResult(input, 1); err != ErrResultConflict {
		t.Fatalf("hash conflict error = %v", err)
	}
}

func TestNormalizeEventValidatesStepSchema(t *testing.T) {
	base := EventInput{ID: "00000000-0000-0000-0000-000000000001", Generation: 1, Sequence: 1, Type: StepEventType}
	cases := []struct {
		payload string
		ok      bool
	}{
		{`{"data":{"schema":1,"step":{"id":"cmd-1","kind":"exec","state":"running","output_append":"x"}}}`, true},
		{`{"data":{"schema":1,"step":{"id":"prep","kind":"prep_env","state":"done","group_id":"g"}}}`, true},
		{`{"data":{"schema":2,"step":{"id":"a","kind":"exec","state":"done"}}}`, false},
		{`{"data":{"step":{"id":"a","kind":"exec","state":"done"}}}`, false},
		{`{"data":{"schema":1,"step":{"id":"","kind":"exec","state":"done"}}}`, false},
		{`{"data":{"schema":1,"step":{"id":"` + strings.Repeat("a", 65) + `","kind":"exec","state":"done"}}}`, false},
		{`{"data":{"schema":1,"step":{"id":"a","kind":"shell","state":"done"}}}`, false},
		{`{"data":{"schema":1,"step":{"id":"a","kind":"exec","state":"pending"}}}`, false},
		{`{"data":{"schema":1,"step":{"id":"a","kind":"exec"}}}`, false},
		{`{"data":{"schema":1,"step":{"id":7,"kind":"exec","state":"done"}}}`, false},
		{`{"data":{"schema":1}}`, false},
		{`{"text":"no data"}`, false},
	}
	for _, c := range cases {
		input := base
		input.Payload = json.RawMessage(c.payload)
		_, _, err := normalizeEvent(input, 1)
		if (err == nil) != c.ok {
			t.Errorf("payload %s: err = %v, want ok=%v", c.payload, err, c.ok)
		}
	}
	// 其他类型不做 step 校验。
	other := base
	other.Type, other.Payload = "tool", json.RawMessage(`{"data":{"schema":9}}`)
	if _, _, err := normalizeEvent(other, 1); err != nil {
		t.Fatalf("non-step event rejected: %v", err)
	}
}

func TestNormalizeResultUsage(t *testing.T) {
	input := ResultInput{ID: "00000000-0000-0000-0000-000000000001", Generation: 1, Status: "SUCCEEDED"}
	_, withoutUsage, err := normalizeResult(input, 1)
	if err != nil {
		t.Fatal(err)
	}
	input.Usage = &Usage{Input: 10, CachedInput: 2, Output: 5}
	_, withUsage, err := normalizeResult(input, 1)
	if err != nil || withUsage == withoutUsage {
		t.Fatalf("usage must change the canonical hash: %v", err)
	}
	// 省略 usage 的哈希与旧版本一致：旧版规范化载荷的字段顺序与取值如下。
	legacy, _ := json.Marshal(struct {
		ID         string          `json:"id"`
		Generation int64           `json:"generation"`
		Status     string          `json:"status"`
		Summary    string          `json:"summary"`
		Output     string          `json:"output"`
		Checks     json.RawMessage `json:"checks"`
		Unverified bool            `json:"unverified"`
		Artifacts  []string        `json:"artifact_ids"`
		BranchRef  string          `json:"branch_ref"`
	}{input.ID, 1, "SUCCEEDED", "", "", json.RawMessage(`[]`), false, nil, ""})
	if digest := sha256.Sum256(legacy); hex.EncodeToString(digest[:]) != withoutUsage {
		t.Fatalf("hash without usage changed from the legacy canonical form")
	}
	for _, bad := range []Usage{{Input: -1}, {Output: MaxUsageTokens + 1}, {ReasoningOutput: -5}} {
		input.Usage = &bad
		if _, _, err := normalizeResult(input, 1); err != ErrInvalidRequest {
			t.Fatalf("usage %+v error = %v", bad, err)
		}
	}
}
