package codex

import (
	"context"
	"errors"
	"strings"
	"testing"

	"openfox/internal/runtime"
)

// Codex 现有的 app-server 调用方式没有可确认的计划 / 协作模式参数，只支持 FOLLOW_RUNTIME；
// DEFAULT 与 PLAN 在任何探测和进程启动之前失败（Executable 指向不存在的文件，能走到探测就会得到别的错误）。
func TestCodexCollaborationModesOnlyFollowRuntime(t *testing.T) {
	adapter := New("/nonexistent/codex")
	if got := strings.Join(adapter.CollaborationModes(), ","); got != "FOLLOW_RUNTIME" {
		t.Fatalf("codex collaboration modes = %s", got)
	}
	for _, mode := range []string{"DEFAULT", "PLAN"} {
		for _, approval := range []string{"", "READ_ONLY", "FULL_ACCESS"} {
			_, err := adapter.Start(context.Background(), runtime.RunRequest{Prompt: "p", ApprovalMode: approval, CollaborationMode: mode})
			if !errors.Is(err, runtime.ErrUnsupported) {
				t.Fatalf("codex %s/%s: err = %v", mode, approval, err)
			}
		}
	}
	// FOLLOW_RUNTIME 与历史值 FOLLOW_AGENT 不会被协作模式拦下（之后因可执行文件不存在而失败，不是 ErrUnsupported）。
	for _, mode := range []string{"", "FOLLOW_RUNTIME", "FOLLOW_AGENT"} {
		_, err := adapter.Start(context.Background(), runtime.RunRequest{Prompt: "p", CollaborationMode: mode})
		if err == nil || errors.Is(err, runtime.ErrUnsupported) {
			t.Fatalf("codex %q: err = %v", mode, err)
		}
	}
}
