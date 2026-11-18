package codex

import (
	"strings"
	"testing"
)

func TestLongCodexToolOutputArchivedRedacted(t *testing.T) {
	s, _ := newStepSession()
	var saved string
	s.recordOutput = func(_, _, text string) { saved = text }
	text := "HEAD s3cr3t\n" + strings.Repeat("x", stepOutputLimit+100) + "TAIL"
	steps, _ := feed(t, s, completed(map[string]any{"id": "large", "type": "commandExecution", "status": "completed", "command": "fixture", "aggregatedOutput": text, "exitCode": 0}))
	if strings.Contains(saved, "s3cr3t") || !strings.HasPrefix(saved, "HEAD [REDACTED]") || !strings.HasSuffix(saved, "TAIL") {
		t.Fatal("archive lost text or exposed secret")
	}
	if len(steps) != 1 || steps[0]["truncated"] != true || len(steps[0]["output"].(string)) > stepOutputLimit {
		t.Fatal("preview was not bounded")
	}
}
