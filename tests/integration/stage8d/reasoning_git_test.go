package stage8d

import (
	"strings"
	"testing"

	"openfox/internal/conversations"
	"openfox/internal/execution"
)

func (h *harness) testReasoningSnapshots(t *testing.T) {
	for _, depth := range []string{"HIGH", "ULTRA"} {
		t.Run(depth, func(t *testing.T) {
			h.testReasoningSnapshot(t, depth)
		})
	}
}

func (h *harness) testReasoningSnapshot(t *testing.T, depth string) {
	capabilities := strings.Replace(fullCapabilities, `"codex":{"runnable":true,"models":[{"id":"gpt-5.1","display_name":"GPT","is_default":true}]`,
		`"codex":{"runnable":true,"reasoning_efforts":["DEFAULT","HIGH","EXTRA_HIGH","ULTRA"],"models":[{"id":"gpt-5.1","display_name":"GPT","is_default":true,"reasoning_efforts":["DEFAULT","`+depth+`"],"default_reasoning_effort":"`+depth+`"}]`, 1)
	h.setCapabilities(capabilities)
	h.setAgentConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")
	conversation := h.newConversation(alice)
	options, err := h.options(alice, orgA, conversation)
	if err != nil {
		t.Fatal(err)
	}
	var reportedDefault string
	for _, runtime := range options.Runtimes {
		if runtime.Provider == "codex" && len(runtime.Models) > 0 {
			reportedDefault = runtime.Models[0].DefaultReasoningEffort
		}
	}
	if reportedDefault != depth {
		t.Fatalf("model default not included in options: %+v", options)
	}
	expectErr(t, h.try(alice, orgA, conversation, conversations.OverridesPatch{ReasoningEffort: set("EXTRA_HIGH"), ExpectedVersion: ver(0)}), conversations.ErrReasoningEffortNotAvailable)
	if depth == "HIGH" {
		expectErr(t, h.try(alice, orgA, conversation, conversations.OverridesPatch{ReasoningEffort: set("ULTRA"), ExpectedVersion: ver(0)}), conversations.ErrReasoningEffortNotAvailable)
	}
	summary := h.patch(alice, conversation, conversations.OverridesPatch{ReasoningEffort: set(depth), ExpectedVersion: ver(0)})
	if summary.Effective.ReasoningEffort != depth || summary.Overrides.ReasoningEffort == nil || *summary.Overrides.ReasoningEffort != depth {
		t.Fatalf("reasoning override = %+v", summary)
	}
	run := h.sendAndDispatch(alice, conversation, depth+" reasoning")
	lease := h.claim(run)
	if got := h.context(lease); got.ReasoningEffort != depth {
		t.Fatalf("run depth = %q", got.ReasoningEffort)
	}
	h.patch(alice, conversation, conversations.OverridesPatch{ReasoningEffort: null(), ExpectedVersion: ver(1)})
	if got := h.context(lease); got.ReasoningEffort != depth {
		t.Fatalf("running snapshot changed to %q", got.ReasoningEffort)
	}
	h.finish(run, lease)
	next := h.sendAndDispatch(alice, conversation, "default reasoning")
	nextLease := h.claim(next)
	if got := h.context(nextLease); got.ReasoningEffort != "DEFAULT" {
		t.Fatalf("next run depth = %q", got.ReasoningEffort)
	}
	h.finish(next, nextLease)
	h.patch(alice, conversation, conversations.OverridesPatch{ReasoningEffort: set(depth), ExpectedVersion: ver(2)})
	expectErr(t, h.try(alice, orgA, conversation, conversations.OverridesPatch{Runtime: set("pi"), ExpectedVersion: ver(3)}), conversations.ErrReasoningEffortNotAvailable)
	h.setCapabilities(fullCapabilities)
	failed := h.sendAndMaterialize(alice, conversation, "old daemon must reject non-default depth")
	if got := h.run(failed); got.State != execution.StateFailed {
		t.Fatalf("unsupported depth did not fail: %+v", got)
	}
}

func (h *harness) testGitSnapshots(t *testing.T) {
	h.setCapabilities(fullCapabilities)
	h.setAgentConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")
	h.exec(`UPDATE openfox.organization_git_settings SET author_name_mode='MANUAL',author_email_mode='MANUAL',author_name='Organization',author_email='org@example.test',co_author_enabled=true WHERE organization_id=$1::uuid`, orgA)
	h.exec(`UPDATE openfox.space_git_settings SET author_name_mode='MANUAL',author_email_mode='INHERIT',author_name='Space',author_email='',co_author_enabled=NULL WHERE organization_id=$1::uuid AND space_id=$2::uuid`, orgA, spaceA)
	conversation := h.newConversation(alice)
	legacy := h.sendAndDispatch(alice, conversation, "explicit legacy Git config")
	legacyLease := h.claim(legacy)
	if got := h.context(legacyLease); got.GitAuthor == nil || got.GitAuthor.Name != "alice & Override agent" || strings.Contains(got.Instructions, "Co-authored-by:") {
		t.Fatalf("legacy configuration changed: %+v", got.GitAuthor)
	}
	h.finish(legacy, legacyLease)
	h.exec(`INSERT INTO openfox.agent_config_versions (organization_id,agent_id,config_version,computer_id,grant_id,runtime,model,max_concurrency,queue_timeout_seconds,run_timeout_seconds,created_by,git_author_name_mode,git_author_email_mode,git_co_author_enabled)
		SELECT organization_id,agent_id,(SELECT max(config_version)+1 FROM openfox.agent_config_versions WHERE organization_id=$1::uuid AND agent_id=$2::uuid),computer_id,grant_id,runtime,model,max_concurrency,queue_timeout_seconds,run_timeout_seconds,created_by,'INHERIT','INHERIT',NULL
		FROM openfox.agent_config_versions WHERE organization_id=$1::uuid AND agent_id=$2::uuid AND config_version=1`, orgA, agentA)
	h.exec(`UPDATE openfox.agents SET current_config_version=(SELECT max(config_version) FROM openfox.agent_config_versions WHERE organization_id=$1::uuid AND agent_id=$2::uuid) WHERE organization_id=$1::uuid AND id=$2::uuid`, orgA, agentA)
	run := h.sendAndDispatch(alice, conversation, "inherit Git defaults")
	lease := h.claim(run)
	got := h.context(lease)
	if got.GitAuthor == nil || got.GitAuthor.Name != "Space" || got.GitAuthor.Email != "org@example.test" || !strings.Contains(got.Instructions, "Co-authored-by: Override agent <alice@example.test>") {
		t.Fatalf("inherited config = %+v, instructions=%q", got.GitAuthor, got.Instructions)
	}
	h.exec(`UPDATE openfox.organization_git_settings SET author_email='next@example.test' WHERE organization_id=$1::uuid`, orgA)
	h.exec(`UPDATE openfox.space_git_settings SET author_name_mode='INHERIT',author_name='',co_author_enabled=false WHERE organization_id=$1::uuid AND space_id=$2::uuid`, orgA, spaceA)
	if again := h.context(lease); again.GitAuthor == nil || *again.GitAuthor != *got.GitAuthor || again.Instructions != got.Instructions {
		t.Fatal("in-flight Git settings changed")
	}
	h.finish(run, lease)
	next := h.sendAndDispatch(alice, conversation, "new Git defaults")
	nextLease := h.claim(next)
	if nextContext := h.context(nextLease); nextContext.GitAuthor == nil || nextContext.GitAuthor.Name != "Organization" || nextContext.GitAuthor.Email != "next@example.test" || strings.Contains(nextContext.Instructions, "Co-authored-by:") {
		t.Fatalf("next Git config = %+v", nextContext.GitAuthor)
	}
	h.finish(next, nextLease)
	for _, item := range []struct {
		nameMode, emailMode, spaceNameMode, spaceEmailMode, wantName, wantEmail string
	}{
		{"USER_AND_AGENT", "USER", "INHERIT", "INHERIT", "alice & Override agent", "alice@example.test"},
		{"OWNER_AND_AGENT", "OWNER", "INHERIT", "INHERIT", "alice & Override agent", "alice@example.test"},
		{"MANUAL", "MANUAL", "MACHINE", "MACHINE", "", ""},
		{"MACHINE", "MACHINE", "USER", "OWNER", "alice", "alice@example.test"},
		{"USER", "USER", "OWNER", "MACHINE", "alice", ""},
	} {
		h.exec(`UPDATE openfox.organization_git_settings SET author_name_mode=$2,author_email_mode=$3 WHERE organization_id=$1::uuid`, orgA, item.nameMode, item.emailMode)
		h.exec(`UPDATE openfox.space_git_settings SET author_name_mode=$3,author_email_mode=$4 WHERE organization_id=$1::uuid AND space_id=$2::uuid`, orgA, spaceA, item.spaceNameMode, item.spaceEmailMode)
		current := h.sendAndDispatch(alice, conversation, "Git source mode snapshot")
		currentLease := h.claim(current)
		context := h.context(currentLease)
		if item.wantName == "" && item.wantEmail == "" {
			if context.GitAuthor != nil {
				t.Fatalf("MACHINE must not inject inherited manual values: %+v", context.GitAuthor)
			}
		} else if context.GitAuthor == nil || context.GitAuthor.Name != item.wantName || context.GitAuthor.Email != item.wantEmail {
			t.Fatalf("source snapshot %+v: %+v", item, context.GitAuthor)
		}
		h.exec(`UPDATE openfox.space_git_settings SET author_name_mode='MACHINE',author_email_mode='MACHINE' WHERE organization_id=$1::uuid AND space_id=$2::uuid`, orgA, spaceA)
		before := context.GitAuthor
		after := h.context(currentLease).GitAuthor
		if (before == nil) != (after == nil) || (before != nil && *before != *after) {
			t.Fatal("source mode changed an existing Run snapshot")
		}
		h.finish(current, currentLease)
	}
}
