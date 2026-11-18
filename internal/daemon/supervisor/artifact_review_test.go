package supervisor

import (
	"context"
	"openfox/internal/runtime"
	"strings"
	"testing"
)

func TestFileCommentsUseOrdinaryConversation(t *testing.T) {
	center := newCenter()
	center.extend = func(rc *RunContext) {
		rc.FileComments = true
		rc.Runtime = "codex"
		rc.ApprovalMode = "READ_ONLY"
		rc.Prompt = "feedback plus ordinary conversation"
		rc.SocialTools = true
	}
	adapter := &fakeAdapter{tools: true, approvalModes: []string{"READ_ONLY"}}
	adapter.onStart = func(request runtime.RunRequest) {
		if strings.Contains(request.WorkingDirectory, "openfox-artifact-review-") {
			t.Error("isolated review workspace")
		}
		if request.ApprovalMode != "READ_ONLY" || !request.RequireTools || request.ArtifactDirectory == "" {
			t.Error("ordinary run settings lost")
		}
		names := map[string]bool{}
		for _, tool := range request.Tools {
			names[tool.Name] = true
		}
		for _, name := range []string{"openfox_file_comment_read", "openfox_file_comment_apply", "openfox_publish_artifact", "openfox_conversation_mention"} {
			if !names[name] {
				t.Errorf("missing tool %s", name)
			}
		}
		if result := request.ToolCall(context.Background(), "openfox_file_comment_read", "read-1", []byte(`{"context_id":"context","start":0}`)); !result.OK {
			t.Error("tool not forwarded")
		}
	}
	newHarness(t, center, adapter, "")
	waitFor(t, "normal chat result", func() bool { return len(center.snapshot().results) == 1 })
}
