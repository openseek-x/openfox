package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"openfox/internal/runtime"
)

const (
	toolPrefix = "openfox_"
	// 任务段各部分的长度上限，保证整段不会挤占本次输入的预算。
	taskFieldLimit   = 4000
	taskCommentLimit = 1000
	taskSectionLimit = 16 * 1024
)

// taskTools 返回本次 Run 向模型声明的任务工具；没有任务（或旧中心）时为 nil，行为与之前一致。
func taskTools(runContext RunContext) []runtime.Tool {
	tools := spaceTools(runContext)
	if runContext.SocialTools {
		tools = append(tools, runtime.Tool{Name: toolPrefix + "conversation_mention", Description: "Mention one existing participant by exact email or user ID in a message in the current conversation.", InputSchema: json.RawMessage(`{"type":"object","properties":{"body":{"type":"string","minLength":1,"maxLength":10000},"mention":{"type":"string","maxLength":254}},"required":["body","mention"],"additionalProperties":false}`)})
	}
	if runContext.Task == nil {
		return tools
	}
	empty := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	comment := json.RawMessage(`{"type":"object","properties":{"body":{"type":"string","minLength":1,"maxLength":10000}},"required":["body"],"additionalProperties":false}`)
	if runContext.SocialTools {
		comment = json.RawMessage(`{"type":"object","properties":{"body":{"type":"string","minLength":1,"maxLength":10000},"reply_to_id":{"type":"string","format":"uuid","description":"ID of an existing comment from task_view"},"mention":{"type":"string","maxLength":254,"description":"One member's exact email or user ID"}},"required":["body"],"additionalProperties":false}`)
		tools = append(tools, runtime.Tool{Name: toolPrefix + "task_reaction", Description: "React to the bound task, or to a comment from task_view.", InputSchema: json.RawMessage(`{"type":"object","properties":{"emoji":{"type":"string","minLength":1,"maxLength":32},"comment_id":{"type":"string","format":"uuid"}},"required":["emoji"],"additionalProperties":false}`)})
	}
	return append(tools, []runtime.Tool{
		{Name: toolPrefix + "task_view", InputSchema: empty,
			Description: "View the task bound to this run: id, title, status, description, acceptance criteria, assignee and the most recent comments."},
		{Name: toolPrefix + "task_comment", Description: "Post a comment on the bound task as this agent (progress, findings, results). Use plain text or Markdown.",
			InputSchema: comment},
		{Name: toolPrefix + "task_start", InputSchema: empty,
			Description: "Move the bound task from the to-do state to the workflow's in-progress state. Does nothing if it is already in progress."},
		{Name: toolPrefix + "conversation_state", Description: "Set the handoff state of this conversation: waiting_review when the work is done and needs human acceptance, waiting_reply when a human must answer a question.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"state":{"type":"string","enum":["waiting_reply","waiting_review"]}},"required":["state"],"additionalProperties":false}`)},
	}...)
}

// spaceTools 返回飞书任务助手的空间任务工具；没有 space_tools（普通 Agent 或旧中心）时为 nil。
func spaceTools(runContext RunContext) []runtime.Tool {
	if runContext.SpaceTools == nil {
		return nil
	}
	return []runtime.Tool{
		{Name: toolPrefix + "space_task_create", Description: "Create a task in this space on behalf of the requester. Returns the task id and link.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":200,"description":"Task title."},"description":{"type":"string","maxLength":10000,"description":"Task description."},"priority":{"type":"string","enum":["none","urgent","high","medium","low"]},"assignee_email":{"type":"string","description":"Email of a space member to assign."},"due_date":{"type":"string","description":"Due date, YYYY-MM-DD."}},"required":["title"],"additionalProperties":false}`)},
		{Name: toolPrefix + "space_task_search", Description: "Search tasks in this space by keyword, status category or assignee.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Keyword in the title."},"status_category":{"type":"string","enum":["backlog","unstarted","started","completed","canceled"]},"assignee_email":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":20,"description":"Default 10."}},"additionalProperties":false}`)},
		{Name: toolPrefix + "space_task_update", Description: "Update the status, assignee or due date of an existing task in this space.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"task":{"type":"string","description":"Task display id in this space, e.g. OF-12."},"status":{"type":"string","description":"Workflow status name."},"assignee_email":{"type":"string"},"unassign":{"type":"boolean"},"due_date":{"type":"string","description":"YYYY-MM-DD."},"clear_due_date":{"type":"boolean"}},"required":["task"],"additionalProperties":false}`)},
	}
}

// taskToolCall 返回 RunRequest.ToolCall：去掉前缀后转发到中心；没有任务时为 nil。
func (r *runState) taskToolCall(runContext RunContext) func(context.Context, string, string, json.RawMessage) (string, bool) {
	if runContext.Task == nil && runContext.SpaceTools == nil && !runContext.SocialTools {
		return nil
	}
	return func(ctx context.Context, name, callID string, arguments json.RawMessage) (string, bool) {
		tool, found := strings.CutPrefix(name, toolPrefix)
		if !found {
			return "unknown tool", false
		}
		text, ok, err := r.supervisor.config.Client.CallTool(ctx, r.offer.OrganizationID, r.offer.RunID, r.offer.Generation, tool, callID, arguments)
		if err != nil {
			r.logger.Warn("supervisor_task_tool_failed", "tool", tool, "error", r.redact.text(err.Error()))
		}
		return text, ok
	}
}

// buildSpaceToolsSection 生成任务助手的空间工具说明（空间、链接格式与上限）；没有空间工具返回空串。
func buildSpaceToolsSection(tools *RunSpaceTools) string {
	if tools == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Space task tools\n")
	fmt.Fprintf(&b, "You work only in space \"%s\" (task prefix %s). Tools: openfox_space_task_create, openfox_space_task_search, openfox_space_task_update.\n",
		boundUTF8(tools.SpaceName, 200), boundUTF8(tools.SpacePrefix, 32))
	if tools.PublicURL != "" {
		fmt.Fprintf(&b, "Task links look like %s/o/<organization>/spaces/%s/tasks?task=<display id>; use the link returned by the tool.\n",
			boundUTF8(tools.PublicURL, 300), boundUTF8(tools.SpaceID, 64))
	}
	fmt.Fprintf(&b, "Limits per run: at most %d tasks created and %d updates; stop and tell the user when a limit is reached.\n\n", tools.Limits.Create, tools.Limits.Update)
	return b.String()
}

// buildTaskSection 生成任务上下文与工作约定；task 为空返回空串。
func buildTaskSection(task *RunTask) string {
	if task == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Task context\n")
	fmt.Fprintf(&b, "This run is bound to task %s: %s\n", boundUTF8(task.DisplayID, 64), boundUTF8(task.Title, 500))
	fmt.Fprintf(&b, "Status: %s (%s)\n", boundUTF8(task.StatusName, 100), boundUTF8(task.StatusCategory, 32))
	if task.Origin == "TASK_ASSIGNMENT" {
		b.WriteString("This run was triggered by assigning the task to you.\n")
	}
	if task.Description != "" {
		b.WriteString("\nDescription:\n" + boundUTF8(task.Description, taskFieldLimit) + "\n")
	}
	if task.Acceptance != "" {
		b.WriteString("\nAcceptance criteria:\n" + boundUTF8(task.Acceptance, taskFieldLimit) + "\n")
	}
	if len(task.Comments) > 0 {
		b.WriteString("\nRecent comments (oldest first):\n")
		for _, comment := range task.Comments {
			fmt.Fprintf(&b, "- [%s] %s %s: %s\n", comment.AuthorKind, boundUTF8(comment.AuthorName, 100), comment.CreatedAt, boundUTF8(comment.Body, taskCommentLimit))
		}
	}
	b.WriteString(`
## Working agreement for this task
- If this run was triggered by task assignment, first read the task comments above, then call openfox_task_start.
- Use openfox_task_view when you need fresher or fuller task context.
- Report progress and conclusions with openfox_task_comment.
- When the work is done, call openfox_conversation_state with waiting_review so a human can accept it.
- If you need a human to answer a question, ask it in your reply and call openfox_conversation_state with waiting_reply.
- Never claim the task is completed and never try to change it to a completed or cancelled state; a human decides that.

`)
	section := b.String()
	if len(section) > taskSectionLimit {
		// 评论与描述已分别截断，仍超长时保留末尾的工作约定。
		agreement := section[strings.Index(section, "## Working agreement"):]
		section = boundUTF8(section, taskSectionLimit-len(agreement)-32) + "\n[truncated]\n\n" + agreement
	}
	return section
}
