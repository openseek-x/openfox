import type {
  AgentDirectoryEntry,
  ConversationMessage,
  ConversationStatus,
  ConversationSummary,
  RunState,
} from "@openfox/api";
import type { MessageKey } from "@openfox/i18n";
import type { BadgeTone, IconName } from "@openfox/ui";

// 对话页签与对话页的纯函数：分组、排序、状态映射、消息分页合并、执行时间线锚点。

export type ChatMode = "recents" | "organized" | "sessions";
export const CHAT_MODES: readonly ChatMode[] = [
  "recents",
  "organized",
  "sessions",
];

export const isChatMode = (value: unknown): value is ChatMode =>
  CHAT_MODES.includes(value as ChatMode);

/** 活动 Run 的状态（显示「停止」按钮、计时） */
const ACTIVE_RUN_STATES: readonly RunState[] = [
  "QUEUED",
  "PREPARING",
  "RUNNING",
  "CANCEL_REQUESTED",
];
export const isActiveRunState = (state: RunState | undefined | null) =>
  Boolean(state && ACTIVE_RUN_STATES.includes(state));

/** 会话当前活动 Run 的 id；没有活动 Run 时为 null */
export function activeRunId(
  conversation: Pick<ConversationSummary, "run"> | undefined,
): string | null {
  const run = conversation?.run;
  return run && isActiveRunState(run.state) ? run.id : null;
}

export interface StatusMeta {
  label: MessageKey;
  tone: BadgeTone;
  icon: IconName;
}

/** 派生状态 → 状态字、徽标色与图标（Sidebar/ConversationItem、Header/Conversation）。IDLE 不显示。 */
export type DisplayStatus = ConversationStatus | "DISCARDED";

export function displayStatus(
  conversation: ConversationSummary,
): DisplayStatus {
  if (
    conversation.lifecycle === "OPEN" &&
    !isActiveRunState(conversation.run?.state)
  ) {
    return conversation.status_mark ?? conversation.status;
  }
  return conversation.status;
}

const STATUS_META: Partial<Record<DisplayStatus, StatusMeta>> = {
  WORKING: {
    label: "chat.status.WORKING",
    tone: "success",
    icon: "openfox-grid-spinner",
  },
  QUEUED: {
    label: "chat.status.QUEUED",
    tone: "neutral",
    icon: "openfox-queue-clock",
  },
  WAITING: {
    label: "chat.status.WAITING",
    tone: "warning",
    icon: "openfox-hand",
  },
  REVIEW: {
    label: "chat.status.REVIEW",
    tone: "info",
    icon: "openfox-scan-eye",
  },
  ERROR: {
    label: "chat.status.ERROR",
    tone: "danger",
    icon: "openfox-alert-circle",
  },
  DONE: {
    label: "chat.status.DONE",
    tone: "info",
    icon: "openfox-circle-check",
  },
  DISCARDED: {
    label: "tasks.detail.discardedMark",
    tone: "muted",
    icon: "openfox-ban",
  },
  ABORTED: { label: "chat.status.ABORTED", tone: "muted", icon: "openfox-ban" },
};

export function statusMeta(status: DisplayStatus): StatusMeta | null {
  return STATUS_META[status] ?? null;
}

/** 会话标题：自定义标题 → 绑定任务标题 → null（由界面显示「无标题」） */
export function conversationTitle(
  conversation: Pick<ConversationSummary, "title" | "task">,
): string | null {
  const title = conversation.title?.trim();
  if (title) return title;
  return conversation.task?.title?.trim() || null;
}

/** 列表排序：updated_at 降序，同一时刻按 id 降序（与后端游标一致） */
export function byRecent(a: ConversationSummary, b: ConversationSummary) {
  if (a.updated_at !== b.updated_at)
    return a.updated_at < b.updated_at ? 1 : -1;
  return a.id < b.id ? 1 : a.id > b.id ? -1 : 0;
}

/** 合并多页会话：按 id 去重（后到的覆盖），已归档的剔除，按最近排序 */
export function mergeConversationPages(
  pages: ReadonlyArray<{ conversations: ConversationSummary[] }>,
): ConversationSummary[] {
  const map = new Map<string, ConversationSummary>();
  for (const page of pages)
    for (const item of page.conversations) map.set(item.id, item);
  return [...map.values()].filter((item) => !item.archived_at).sort(byRecent);
}

/** 微信模式：置顶与最近两组；没有置顶时 pinned 为空数组 */
export function splitPinned(list: ConversationSummary[]) {
  return {
    pinned: list.filter((item) => item.pinned),
    recent: list.filter((item) => !item.pinned),
  };
}

export interface AgentGroup {
  agent: ConversationSummary["agent"];
  conversations: ConversationSummary[];
  /** 组内最近一次更新 */
  updatedAt: string;
}

/** 多会话模式：按 Agent 分组，组按最近更新排序，组内保持最近排序 */
export function groupByAgent(list: ConversationSummary[]): AgentGroup[] {
  const groups = new Map<string, AgentGroup>();
  for (const item of [...list].sort(byRecent)) {
    const group = groups.get(item.agent_id);
    if (group) group.conversations.push(item);
    else
      groups.set(item.agent_id, {
        agent: item.agent,
        conversations: [item],
        updatedAt: item.updated_at,
      });
  }
  return [...groups.values()];
}

/** 多会话模式每组初始显示条数与每次展开增量 */
export const SESSION_GROUP_INITIAL = 5;
export const SESSION_GROUP_STEP = 20;

/** Slack 模式「直接对话」最多直接显示的行数，其余收进「更多」 */
export const DIRECT_VISIBLE = 8;

export interface DirectRow {
  entry: AgentDirectoryEntry;
  /** 与该 Agent 最近的会话 */
  latest: ConversationSummary | null;
}

/**
 * Slack 模式「直接对话」：每个可单聊的 Agent 一行（排除被本地移除的），
 * 有会话的按最近会话时间排在前面，其余按名称。
 */
export function directRows(
  entries: AgentDirectoryEntry[],
  conversations: ConversationSummary[],
  hidden: ReadonlySet<string>,
): DirectRow[] {
  const latest = new Map<string, ConversationSummary>();
  for (const item of [...conversations].sort(byRecent))
    if (!latest.has(item.agent_id)) latest.set(item.agent_id, item);
  return entries
    .filter(
      (entry) =>
        entry.type === "agent" &&
        entry.allow_direct_chat !== false &&
        !entry.archived_at &&
        !hidden.has(entry.id),
    )
    .map((entry) => ({ entry, latest: latest.get(entry.id) ?? null }))
    .sort((a, b) => {
      const at = a.latest?.updated_at ?? "";
      const bt = b.latest?.updated_at ?? "";
      if (at !== bt) return at < bt ? 1 : -1;
      return a.entry.name.localeCompare(b.entry.name);
    });
}

/** 消息分页（pages[0] 最新，之后越来越早）合并为按 seq 升序、去重的列表 */
export function mergeMessagePages(
  pages: ReadonlyArray<{ messages: ConversationMessage[] }>,
): ConversationMessage[] {
  const map = new Map<number, ConversationMessage>();
  for (const page of pages)
    for (const message of page.messages) map.set(message.seq, message);
  return [...map.values()].sort((a, b) => a.seq - b.seq);
}

export type TimelineItem =
  | { kind: "message"; message: ConversationMessage; runBefore: string | null }
  | { kind: "run"; runId: string; afterSeq: number };

/**
 * 决定每段执行时间线挂在哪里：
 * - AGENT 消息带 run_id：时间线挂在该消息上方（同一 Run 只挂一次）；
 * - USER 消息触发的 Run 没有对应 AGENT 消息（运行中、失败、取消）：挂在该用户消息之后；
 * - 会话的活动 Run 还没出现在任何消息里（刚排队）：挂在末尾。
 */
export function timelineItems(
  messages: ConversationMessage[],
  activeRun: string | null,
): TimelineItem[] {
  const agentRuns = new Set<string>();
  for (const message of messages)
    if (message.author_kind !== "USER" && message.run_id)
      agentRuns.add(message.run_id);
  const placed = new Set<string>();
  const items: TimelineItem[] = [];
  for (const message of messages) {
    const runId = message.run_id ?? null;
    if (message.author_kind === "USER") {
      items.push({ kind: "message", message, runBefore: null });
      if (runId && !agentRuns.has(runId) && !placed.has(runId)) {
        placed.add(runId);
        items.push({ kind: "run", runId, afterSeq: message.seq });
      }
      continue;
    }
    const attach =
      message.kind !== "ACTION_EXECUTED" && runId && !placed.has(runId)
        ? runId
        : null;
    if (attach) placed.add(attach);
    items.push({ kind: "message", message, runBefore: attach });
  }
  if (activeRun && !placed.has(activeRun))
    items.push({
      kind: "run",
      runId: activeRun,
      afterSeq: messages.at(-1)?.seq ?? 0,
    });
  return items;
}

/** 最后一条用户消息（ModelError「重试」重新发送它） */
export function lastUserMessage(
  messages: ConversationMessage[],
): ConversationMessage | null {
  for (let i = messages.length - 1; i >= 0; i--)
    if (messages[i]!.author_kind === "USER") return messages[i]!;
  return null;
}

/** 列表时间：今天显示时分，今年显示月日，更早显示年月日 */
export function shortTime(
  value: string,
  locale: string,
  now: Date = new Date(),
): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const sameDay = date.toDateString() === now.toDateString();
  if (sameDay)
    return new Intl.DateTimeFormat(locale, {
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    }).format(date);
  const sameYear = date.getFullYear() === now.getFullYear();
  return new Intl.DateTimeFormat(locale, {
    ...(sameYear ? {} : { year: "numeric" }),
    month: "numeric",
    day: "numeric",
  }).format(date);
}

/** 读写 JSON 数组形式的本地 id 集合；格式不对时视为空 */
export function parseIdSet(raw: string | null): Set<string> {
  if (!raw) return new Set();
  try {
    const value = JSON.parse(raw) as unknown;
    return new Set(
      Array.isArray(value)
        ? value.filter((v): v is string => typeof v === "string")
        : [],
    );
  } catch {
    return new Set();
  }
}

/**
 * 发消息时的提及：只保留正文里还在的 @名称；
 * 自己是会话所有者时自动 @ 会话 Agent（单聊时每条消息都交给 Agent 执行），
 * 共享会话的其他成员需要手动 @ Agent 才会触发执行。
 */
export function buildMentions(
  body: string,
  picked: ReadonlyArray<{ kind: "user" | "agent"; id: string; name: string }>,
  agentId: string,
  autoMentionAgent: boolean,
  /** 小队对话：允许 @ 的成员 Agent（缺省只能 @ agentId） */
  memberIds?: ReadonlyArray<string>,
): Array<{ user_id: string } | { agent_id: string }> {
  const result: Array<{ user_id: string } | { agent_id: string }> = [];
  const seen = new Set<string>();
  const allowed = new Set(memberIds ?? [agentId]);
  for (const item of picked) {
    if (!body.includes(`@${item.name}`)) continue;
    if (item.kind === "agent" && !allowed.has(item.id)) continue;
    const key = `${item.kind}:${item.id}`;
    if (seen.has(key)) continue;
    seen.add(key);
    result.push(
      item.kind === "agent" ? { agent_id: item.id } : { user_id: item.id },
    );
  }
  if (autoMentionAgent && !seen.has(`agent:${agentId}`))
    result.push({ agent_id: agentId });
  return result;
}

/** 列表摘要：去掉常见 Markdown 标记（标题、列表、引用、* / ** / ~~ 强调、行内代码、链接），压成一行纯文本；下划线不处理，避免误伤 snake_case */
export function plainPreview(text: string): string {
  return text
    .replace(/```[^\n]*\n?/g, " ")
    .replace(/^\s{0,3}(#{1,6}|>|[-*+]|\d+[.)])\s+/gm, "")
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/(\*\*|\*|~~|`)(?=\S)([^\n]*?\S)\1/g, "$2")
    .replace(/`/g, "")
    .replace(/\s+/g, " ")
    .trim();
}
