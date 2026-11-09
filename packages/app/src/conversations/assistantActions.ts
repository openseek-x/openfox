import type { MessageCreateInput } from "@openfox/api";

export type AssistantAction = NonNullable<
  MessageCreateInput["assistant_action"]
>;

export const assistantActions = {
  create_agent: {
    icon: "openfox-robot-head",
    label: "chat.assistant.capabilities.createAgent",
    hint: "chat.assistant.capabilities.createAgentDesc",
    placeholder: "chat.assistant.capabilities.agentPlaceholder",
  },
  create_task: {
    icon: "openfox-task-list",
    label: "chat.assistant.capabilities.createTask",
    hint: "chat.assistant.capabilities.createTaskDesc",
    placeholder: "chat.assistant.capabilities.taskPlaceholder",
  },
} as const satisfies Record<AssistantAction, object>;
