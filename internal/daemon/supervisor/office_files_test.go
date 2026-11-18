package supervisor

import (
	"context"
	"encoding/json"
	"openfox/internal/artifactreview"
	"openfox/internal/office"
	"openfox/internal/runtime"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfficeFileTransportStaysOutsideModel(t *testing.T) {
	raw, err := os.ReadFile("../../office/testdata/sample.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	dir, workspace := t.TempDir(), t.TempDir()
	const id = "10000000-0000-4000-8000-000000000001"
	calls := 0
	base := func(ctx context.Context, name, call string, args json.RawMessage) runtime.ToolResult {
		calls++
		if name == "openfox_file_comment_export" {
			payload, _ := json.Marshal(map[string]any{"sha256": artifactreview.Digest(string(raw)), "mime_type": office.XLSX, "content_base64": raw})
			return runtime.TextResult(string(payload), true)
		}
		var wire struct {
			ContextID string `json:"context_id"`
			artifactreview.OfficeReplacement
		}
		if json.Unmarshal(args, &wire) != nil || string(wire.Content) != string(raw) || wire.ContextID != id || wire.SHA256 != artifactreview.Digest(string(raw)) {
			t.Fatal("bad binary transport")
		}
		return runtime.TextResult(`{"applied":true,"artifact_id":"new"}`, true)
	}
	invoke := withOfficeFiles(base, RunContext{FileComments: true, ApprovalMode: "WORKSPACE_WRITE"}, workspace, dir)
	args := json.RawMessage(`{"context_id":"` + id + `"}`)
	result := invoke(context.Background(), "openfox_file_comment_export", "export", args)
	if !result.OK || strings.Contains(result.Text, "content_base64") {
		t.Fatal("binary leaked to model", result.OK)
	}
	var exported struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(result.Text), &exported) != nil {
		t.Fatal("metadata")
	}
	if data, e := os.ReadFile(exported.Path); e != nil || string(data) != string(raw) {
		t.Fatal("source missing", e)
	}
	if retry := invoke(context.Background(), "openfox_file_comment_export", "export-retry", args); retry.Text != result.Text {
		t.Fatal("export retry changed source")
	}
	output := filepath.Join(workspace, "changed.xlsx")
	if err = os.WriteFile(output, raw, 0600); err != nil {
		t.Fatal(err)
	}
	replacement, _ := json.Marshal(map[string]string{"context_id": id, "path": output, "sha256": artifactreview.Digest(string(raw)), "summary": "change"})
	if !invoke(context.Background(), "openfox_file_comment_replace", "replace", replacement).OK {
		t.Fatal("valid file rejected")
	}
	before := calls
	for _, mode := range []string{"READ_ONLY", "PLAN"} {
		locked := withOfficeFiles(base, RunContext{FileComments: true, ApprovalMode: mode, CollaborationMode: mode}, workspace, dir)
		if locked(context.Background(), "openfox_file_comment_export", "deny", args).OK || locked(context.Background(), "openfox_file_comment_replace", "deny", replacement).OK {
			t.Fatal("readonly bypass")
		}
	}
	outside := filepath.Join(t.TempDir(), "secret.xlsx")
	_ = os.WriteFile(outside, raw, 0600)
	_ = os.Symlink(outside, filepath.Join(workspace, "link.xlsx"))
	for _, path := range []string{outside, filepath.Join(workspace, "link.xlsx")} {
		bad, _ := json.Marshal(map[string]string{"context_id": id, "path": path})
		if invoke(context.Background(), "openfox_file_comment_replace", "deny", bad).OK {
			t.Fatal("outside path accepted")
		}
	}
	if calls != before {
		t.Fatal("invalid or readonly calls reached center")
	}
	_ = os.Chmod(exported.Path, 0600)
	_ = os.WriteFile(exported.Path, []byte("local modified"), 0600)
	if invoke(context.Background(), "openfox_file_comment_export", "retry", args).OK {
		t.Fatal("modified source overwritten")
	}
}
