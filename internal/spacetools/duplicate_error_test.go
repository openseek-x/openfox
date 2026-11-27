package spacetools

// OPE-17 · D1 第二期：Agent 工具把任务改到重复状态而没有目标时，服务端返回 400 INVALID_DUPLICATE。
// 这里验证业务错误被翻译成给 Agent 的说明，而不是当作系统错误返回 500。

import (
	"fmt"
	"strings"
	"testing"

	"openfox/internal/collaboration"
)

func TestErrorTextInvalidDuplicate(t *testing.T) {
	rule := collaboration.NewRuleError(400, "INVALID_DUPLICATE", "duplicate target is not available")
	for name, err := range map[string]error{
		"direct":  rule,
		"wrapped": fmt.Errorf("update task: %w", rule),
	} {
		text, business := spaceToolErrorText(err)
		if !business {
			t.Fatalf("%s: INVALID_DUPLICATE must be a business error, got a system error", name)
		}
		if !strings.HasPrefix(text, "操作被拒绝：") || !strings.Contains(text, rule.Message) {
			t.Fatalf("%s: text = %q", name, text)
		}
	}
}
