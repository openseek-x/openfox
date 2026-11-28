import type { ConversationMessage, ConversationSummary } from "@openfox/api";

export const TS = "2026-10-29T08:00:00Z";

export function summary(
  id: string,
  extra: Partial<ConversationSummary> = {},
): ConversationSummary {
  return {
    organization_id: "o1",
    id,
    space_id: "s1",
    agent_id: "a1",
    owner_user_id: "u1",
    visibility: "PRIVATE",
    lifecycle: "OPEN",
    handoff: "NONE",
    origin: "USER",
    version: 3,
    created_at: TS,
    updated_at: TS,
    title: `会话 ${id}`,
    archived_at: null,
    pinned: false,
    last_read_seq: 0,
    unread: false,
    last_message: {
      seq: 1,
      author_kind: "AGENT",
      preview: `摘要 ${id}`,
      created_at: TS,
    },
    agent: {
      id: "a1",
      name: "熊猫",
      title: "产品经理",
      avatar: null,
      online: true,
    },
    task: null,
    run: null,
    status: "IDLE",
    overrides: { runtime: null, model: null, approval_mode: null, version: 0 },
    effective: {
      runtime: "codex",
      model: "default",
      approval_mode: "FOLLOW_RUNTIME",
    },
    agent_defaults: {
      runtime: "codex",
      model: "default",
      approval_mode: "FOLLOW_RUNTIME",
    },
    ...extra,
  };
}

export function message(
  seq: number,
  extra: Partial<ConversationMessage> = {},
): ConversationMessage {
  return {
    organization_id: "o1",
    id: `m${seq}`,
    conversation_id: "c1",
    seq,
    author_kind: seq % 2 === 1 ? "USER" : "AGENT",
    body: `消息 ${seq}`,
    created_at: TS,
    kind: "TEXT",
    run_id: null,
    ...extra,
  };
}
