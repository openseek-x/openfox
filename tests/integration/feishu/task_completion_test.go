package feishuint

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSpaceToolCompletionClock(t *testing.T) {
	b := setupBots(t)
	bot := b.makeAssistant(b.foxSpace, "")
	run := b.newToolRun(bot.Agent.ID, b.memberID, "MEMBER")
	created := b.mustCall(run, "space_task_create", "completion-create", map[string]any{"title": "T02 tool task"})
	if !created.OK {
		t.Fatal(created.Text)
	}
	display := strings.TrimPrefix(created.Text[:strings.Index(created.Text, "「")], "已创建 ")
	var id string
	b.scan(&id, `SELECT id::text FROM openfox.tasks WHERE organization_id=$1::uuid AND title='T02 tool task'`, b.org)
	read := func() *time.Time {
		var at *time.Time
		b.scan(&at, `SELECT completed_at FROM openfox.tasks WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, id)
		return at
	}
	if read() != nil {
		t.Fatal("new tool task is not completed")
	}
	var previous *time.Time
	for i, status := range []string{"Done", "In Progress", "Done"} {
		result := b.mustCall(run, "space_task_update", fmt.Sprintf("completion-update-%d", i), map[string]any{"task": display, "status": status})
		if !result.OK {
			t.Fatal(result.Text)
		}
		at := read()
		if (at != nil) != (status == "Done") {
			t.Fatalf("status %s time %v", status, at)
		}
		if at != nil {
			if previous != nil && !at.After(*previous) {
				t.Fatal("tool recompletion did not advance time")
			}
			previous = at
		}
	}
}
