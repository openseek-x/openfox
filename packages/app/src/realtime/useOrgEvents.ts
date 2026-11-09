import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

import {
  ORGANIZATION_EVENT_TYPES,
  RUN_EVENT_TYPE,
  subscribeEvents,
  type OrganizationEvent,
} from "@openfox/api";

import { applyRunState, appendRunEvent } from "../conversations/steps/queries";
import { keys, useApi } from "../context";

/** 订阅的事件类型：组织事件（失效通知）+ Run 执行事件（直接并入事件缓存）。 */
const SUBSCRIBED_TYPES: readonly string[] = [
  ...ORGANIZATION_EVENT_TYPES,
  RUN_EVENT_TYPE,
];

/** 多个事件在这个窗口内合并成一次失效。 */
export const EVENT_COALESCE_MS = 200;

/**
 * 待失效的目标。组织事件只是失效通知，这里只记录「要刷新什么」，不读事件里的数据。
 * 契约里的 OrganizationEvent 暂未暴露 space_id / conversation_id，读到时精确失效，读不到时按组织整体失效。
 */
interface Pending {
  /** 整体失效（重连成功） */
  all: boolean;
  directChats?: boolean;
  /** 任务列表与计数：null 表示全部空间 */
  taskSpaces: Set<string> | null;
  taskLists: boolean;
  /** 任务详情与子资源：null 表示全部 */
  taskIds: Set<string> | null;
  taskDetails: boolean;
  inbox: boolean;
  conversations: boolean;
  /** 精确失效的会话（详情）；null 表示事件没带 conversation_id，整体失效会话查询 */
  conversationIds: Set<string> | null;
  /** Agent 执行 · 任务时间线（含 Agent 写的评论）：run.state 带 task_id 时精确失效，其余按全部任务时间线失效 */
  timelines: boolean;
  timelineTaskIds: Set<string> | null;
  /** 有新消息的会话（消息列表） */
  messageConversations: Set<string>;
  /** 有空间设置变化：失效空间列表与各空间的详情 / 设置 */
  spaces: boolean;
  /** 迭代变化：精确失效空间的设置、列表和详情 */
  iterationSpaces: Set<string> | null;
  iterationIds: Set<string>;
  /** 专项 / 视图变化：按前缀整体失效（事件只带 id） */
  projects: boolean;
  views: boolean;
  /** 分拣设置 / 规则变化：精确失效空间分拣查询 */
  triageSpaces: Set<string> | null;
  /** 组织设置变化（名称、图标、优先级档位等）；成员与邀请变化 */
  organization: boolean;
  members: boolean;
  /** 自动化变化：精确失效空间的自动化查询；null 表示事件没带 space_id，整体失效 */
  automationSpaces: Set<string> | null;
  /** 文档变化：精确失效空间的文档查询；null 表示事件没带 space_id，整体失效 */
  docSpaces: Set<string> | null;
  /** 技能变化：精确失效空间的技能与 Agent 技能查询；null 表示事件没带 space_id，整体失效 */
  skillSpaces: Set<string> | null;
  /**
   * 有新产物：精确失效会话产物列表与对应 Run 的产物；null 表示事件没带 conversation_id，整体失效。
   * 可选字段：缺省等同空集合（不影响按字面量构造 Pending 的既有代码）。
   */
  artifactConversations?: Set<string> | null;
  artifactRuns?: Set<string>;
}

const emptyPending = (): Pending => ({
  all: false,
  taskSpaces: new Set(),
  taskLists: false,
  taskIds: new Set(),
  taskDetails: false,
  inbox: false,
  conversations: false,
  conversationIds: new Set(),
  timelines: false,
  timelineTaskIds: new Set(),
  messageConversations: new Set(),
  spaces: false,
  iterationSpaces: new Set(),
  iterationIds: new Set(),
  projects: false,
  views: false,
  triageSpaces: new Set(),
  organization: false,
  members: false,
  automationSpaces: new Set(),
  docSpaces: new Set(),
  skillSpaces: new Set(),
  artifactConversations: new Set(),
  artifactRuns: new Set(),
});

const asString = (value: unknown): string | null =>
  typeof value === "string" && value ? value : null;

/** 把一条组织事件并入待失效集合。 */
export function collectEvent(
  pending: Pending,
  type: string,
  event: Partial<OrganizationEvent> & Record<string, unknown>,
): void {
  const payload = (event.payload ?? {}) as Record<string, unknown>;
  switch (type) {
    case "task.changed": {
      pending.taskLists = true;
      const spaceId = asString(event.space_id) ?? asString(payload.space_id);
      if (!spaceId) pending.taskSpaces = null;
      else pending.taskSpaces?.add(spaceId);
      pending.taskDetails = true;
      const ids = Array.isArray(payload.task_ids)
        ? payload.task_ids.filter(
            (id): id is string => typeof id === "string" && id !== "",
          )
        : [];
      const count = typeof payload.count === "number" ? payload.count : 0;
      // 没带 id 或超过 100 个被截断时，详情整体失效
      if (ids.length === 0 || count > ids.length) pending.taskIds = null;
      else for (const id of ids) pending.taskIds?.add(id);
      return;
    }
    case "direct_chat.changed":
      pending.directChats = true;
      return;
    case "inbox.changed":
      pending.inbox = true;
      return;
    case "run.state":
    case "conversation.updated":
    case "conversation.message": {
      // 带 conversation_id 时只失效该会话的详情（与消息），列表总是刷新（状态字、摘要、排序）
      pending.conversations = true;
      if (type === "run.state" || type === "conversation.updated") {
        // Automation history derives its status from the conversation's Run.
        // Refresh both the list and an open result drawer as execution advances.
        const spaceId = asString(event.space_id) ?? asString(payload.space_id);
        if (!spaceId) pending.automationSpaces = null;
        else pending.automationSpaces?.add(spaceId);
        // Agent 可能在运行中写任务评论、改任务状态：让对应任务的时间线与详情一起刷新
        pending.timelines = true;
        const taskId = type === "run.state" ? asString(payload.task_id) : null;
        if (taskId) pending.timelineTaskIds?.add(taskId);
        else pending.timelineTaskIds = null;
      }
      const conversationId = asString(event.conversation_id);
      if (!conversationId) pending.conversationIds = null;
      else {
        pending.conversationIds?.add(conversationId);
        if (type === "conversation.message")
          pending.messageConversations.add(conversationId);
      }
      return;
    }
    case "space.changed":
      // 空间数量少，整体刷新空间列表与详情即可，不按 id 精确失效
      pending.spaces = true;
      return;
    case "iteration.changed": {
      const spaceId = asString(event.space_id) ?? asString(payload.space_id);
      if (!spaceId) pending.iterationSpaces = null;
      else pending.iterationSpaces?.add(spaceId);
      const ids = Array.isArray(payload.iteration_ids)
        ? payload.iteration_ids.filter(
            (id): id is string => typeof id === "string" && id !== "",
          )
        : [];
      for (const id of ids) pending.iterationIds.add(id);
      return;
    }
    case "project.changed":
      pending.projects = true;
      return;
    case "view.changed":
      pending.views = true;
      return;
    case "triage.changed": {
      const spaceId = asString(event.space_id) ?? asString(payload.space_id);
      if (spaceId) pending.triageSpaces?.add(spaceId);
      return;
    }
    case "organization.changed":
      pending.organization = true;
      return;
    case "member.changed":
      pending.members = true;
      return;
    case "automation.changed": {
      const spaceId = asString(event.space_id) ?? asString(payload.space_id);
      if (!spaceId) pending.automationSpaces = null;
      else pending.automationSpaces?.add(spaceId);
      return;
    }
    case "doc.changed": {
      const spaceId = asString(event.space_id) ?? asString(payload.space_id);
      if (!spaceId) pending.docSpaces = null;
      else pending.docSpaces?.add(spaceId);
      return;
    }
    case "skill.changed": {
      const spaceId = asString(event.space_id) ?? asString(payload.space_id);
      if (!spaceId) pending.skillSpaces = null;
      else pending.skillSpaces?.add(spaceId);
      return;
    }
    case "run.artifact": {
      const conversationId = asString(event.conversation_id);
      if (!conversationId) pending.artifactConversations = null;
      else if (pending.artifactConversations !== null)
        (pending.artifactConversations ??= new Set()).add(conversationId);
      const runId = asString(payload.run_id);
      if (runId) (pending.artifactRuns ??= new Set()).add(runId);
      return;
    }
    default:
      return;
  }
}

/** 任务列表 / 计数查询键里的范围：space:<id> 或 my:…（我的任务跨空间，任何空间变化都要刷新）。 */
function scopeMatches(scope: unknown, spaces: Set<string> | null): boolean {
  if (spaces === null) return true;
  if (typeof scope !== "string") return true;
  if (scope.startsWith("my")) return true;
  return scope.startsWith("space:") && spaces.has(scope.slice(6));
}

/** 执行一次合并后的失效。 */
export function flushPending(
  queryClient: QueryClient,
  orgId: string,
  pending: Pending,
): void {
  const invalidate = (queryKey: readonly unknown[]) =>
    void queryClient.invalidateQueries({ queryKey });
  if (pending.all || pending.directChats)
    invalidate(["org", orgId, "direct-chats"]);
  if (pending.all) {
    invalidate(keys.tasks(orgId));
    invalidate(["org", orgId, "automations"]);
    invalidate(["org", orgId, "docs"]);
    invalidate(["org", orgId, "skills"]);
    invalidate(keys.inbox(orgId));
    invalidate(keys.inboxStats(orgId));
    invalidate(keys.conversationOffers(orgId));
    invalidate(keys.conversations(orgId));
    // 正在查看的 Run：详情与事件重拉（事件 queryFn 会与缓存合并，不丢实时追加的事件）
    invalidate(keys.runs(orgId));
    invalidate(keys.spaces(orgId));
    invalidate(keys.projects(orgId));
    invalidate(keys.views(orgId));
    invalidate(keys.organizations);
    invalidate(keys.organizationSettings(orgId));
    invalidate(keys.members(orgId));
    invalidate(keys.invitations(orgId));
    return;
  }
  if (pending.organization) {
    // 组织切换器、设置页；切换为 4 级会把「最低」任务与专项改为「低」
    invalidate(keys.organizations);
    invalidate(keys.organizationSettings(orgId));
    invalidate(keys.tasks(orgId));
    invalidate(keys.projects(orgId));
  }
  if (pending.members) {
    invalidate(keys.organizations);
    // Task projections and invitation filters depend on the viewer's directory access.
    invalidate(keys.tasks(orgId));
    invalidate(["org", orgId, "search"]);
    invalidate(keys.spaces(orgId));
    invalidate(keys.members(orgId));
    invalidate(keys.invitations(orgId));
    invalidate(keys.organizationSettings(orgId));
  }
  if (pending.spaces) {
    // 空间列表、详情、仓库等都挂在 spaces 前缀下；空间任务类型与标签由各自的写操作失效
    invalidate(keys.spaces(orgId));
  }
  if (pending.iterationSpaces === null) {
    invalidate(keys.spaces(orgId));
    invalidate(keys.tasks(orgId));
  } else if (pending.iterationSpaces.size > 0) {
    for (const spaceId of pending.iterationSpaces) {
      invalidate(keys.iterationSettings(orgId, spaceId));
      invalidate(keys.iterations(orgId, spaceId));
      invalidate(keys.tasks(orgId));
    }
    for (const iterationId of pending.iterationIds) {
      void queryClient.invalidateQueries({
        predicate: (query) => query.queryKey.includes(iterationId),
      });
    }
  }
  // 专项变化会改任务的 project_id（含删除专项清空）；视图变化影响打开视图的任务查询
  if (pending.projects) {
    invalidate(keys.projects(orgId));
    invalidate(keys.tasks(orgId));
  }
  if (pending.views) {
    invalidate(keys.views(orgId));
    invalidate(keys.tasks(orgId));
  }
  // 自动化变化只失效对应空间的列表、详情与运行记录（前缀匹配）
  if (pending.automationSpaces === null) {
    invalidate(["org", orgId, "automations"]);
  } else {
    for (const spaceId of pending.automationSpaces)
      invalidate(keys.automations(orgId, spaceId));
  }
  // 文档变化只失效对应空间的文档集、节点、详情与搜索（同一前缀）
  if (pending.docSpaces === null) {
    invalidate(["org", orgId, "docs"]);
  } else {
    for (const spaceId of pending.docSpaces)
      invalidate(keys.docs(orgId, spaceId));
  }
  // 技能变化只失效对应空间的技能列表、详情、文件与 Agent 装配（同一前缀）
  if (pending.skillSpaces === null) {
    invalidate(["org", orgId, "skills"]);
  } else {
    for (const spaceId of pending.skillSpaces)
      invalidate(keys.skills(orgId, spaceId));
  }
  if (pending.triageSpaces === null) {
    invalidate(keys.spaces(orgId));
  } else {
    for (const spaceId of pending.triageSpaces) {
      invalidate(keys.triage(orgId, spaceId));
      invalidate(keys.space(orgId, spaceId));
    }
  }
  if (pending.taskLists) {
    const spaces = pending.taskSpaces;
    void queryClient.invalidateQueries({
      queryKey: keys.tasks(orgId),
      predicate: (query) => {
        const kind = query.queryKey[3];
        return (
          (kind === "list" || kind === "counts") &&
          scopeMatches(query.queryKey[4], spaces)
        );
      },
    });
  }
  if (pending.taskDetails) {
    const ids = pending.taskIds;
    if (ids === null) {
      invalidate(keys.taskDetails(orgId));
      invalidate([...keys.tasks(orgId), "sub"]);
    } else if (ids.size > 0) {
      // 详情按展示编号缓存，用缓存数据里的 id 匹配
      void queryClient.invalidateQueries({
        queryKey: keys.taskDetails(orgId),
        predicate: (query) => {
          const id = (query.state.data as { id?: unknown } | undefined)?.id;
          return typeof id === "string" && ids.has(id);
        },
      });
      for (const id of ids) invalidate([...keys.tasks(orgId), "sub", id]);
    }
  }
  if (pending.timelines) {
    const ids = pending.timelineTaskIds;
    if (ids === null)
      void queryClient.invalidateQueries({
        queryKey: [...keys.tasks(orgId), "sub"],
        predicate: (query) => query.queryKey[5] === "timeline",
      });
    else for (const id of ids) invalidate(keys.taskSub(orgId, id, "timeline"));
  }
  // 产物列表挂在会话详情键下（会话整体失效时一并刷新）；Run 时间线末尾的产物也刷新
  if (pending.artifactConversations === null) {
    void queryClient.invalidateQueries({
      queryKey: keys.conversations(orgId),
      predicate: (query) => query.queryKey.at(-1) === "artifacts",
    });
  } else {
    for (const id of pending.artifactConversations ?? [])
      invalidate(keys.conversationArtifacts(orgId, id));
  }
  for (const runId of pending.artifactRuns ?? [])
    invalidate(keys.runSub(orgId, runId, "artifacts"));
  if (pending.inbox) {
    invalidate(keys.inbox(orgId));
    invalidate(keys.inboxStats(orgId));
    invalidate(keys.conversationOffers(orgId));
  }
  if (pending.conversations) {
    const ids = pending.conversationIds;
    if (ids === null) invalidate(keys.conversations(orgId));
    else {
      invalidate(keys.conversationLists(orgId));
      for (const id of ids) invalidate(keys.conversation(orgId, id));
      for (const id of pending.messageConversations)
        invalidate(keys.conversationMessages(orgId, id));
    }
    invalidate(keys.assistantSessions(orgId));
  }
}

/** 事件合并器：收到事件后 200ms 内的其他事件合并为一次失效。 */
export function createEventBatcher(
  queryClient: QueryClient,
  orgId: string,
  delay = EVENT_COALESCE_MS,
) {
  let pending = emptyPending();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const schedule = () => {
    if (timer !== undefined) return;
    timer = setTimeout(() => {
      timer = undefined;
      const current = pending;
      pending = emptyPending();
      flushPending(queryClient, orgId, current);
    }, delay);
  };
  return {
    push(type: string, event: unknown) {
      if (typeof event !== "object" || event === null) return;
      // Run 执行事件不做失效，直接追加进对应 Run 的事件缓存
      if (type === RUN_EVENT_TYPE) {
        appendRunEvent(queryClient, orgId, event);
        return;
      }
      // run.state 立即更新缓存里的 Run 状态，会话列表仍按合并窗口失效
      if (type === "run.state")
        applyRunState(
          queryClient,
          orgId,
          event as Partial<OrganizationEvent> & Record<string, unknown>,
        );
      collectEvent(
        pending,
        type,
        event as Partial<OrganizationEvent> & Record<string, unknown>,
      );
      schedule();
    },
    /** 重连成功：期间可能漏掉事件，整体失效一次 */
    resync() {
      pending.all = true;
      schedule();
    },
    dispose() {
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
    },
  };
}

/**
 * 订阅组织的统一事件流（每个组织一条 EventSource），按事件类型失效 TanStack Query 缓存。
 * 断线由 EventSource 自动重连并带 Last-Event-ID；重连成功后整体失效一次。
 * 环境不支持 EventSource（jsdom、旧浏览器）时什么都不做，页面靠查询自身的刷新兜底。
 */
export function useOrgEvents(orgId: string): void {
  const api = useApi();
  const queryClient = useQueryClient();
  useEffect(() => {
    if (!orgId || typeof globalThis.EventSource !== "function") return;
    const batcher = createEventBatcher(queryClient, orgId);
    let close: (() => void) | undefined;
    try {
      close = subscribeEvents(api.events.url(orgId), SUBSCRIBED_TYPES, {
        // 补齐首屏快照与 SSE 建连之间完成的消息。只重读当前打开的会话。
        onFirstOpen: () => {
          void queryClient.invalidateQueries({
            queryKey: keys.conversations(orgId),
            type: "active",
            predicate: ({ queryKey }) =>
              queryKey.length === 5 &&
              (queryKey[3] === "detail" || queryKey[3] === "messages"),
          });
        },
        onEvent: (type, data) => batcher.push(type, data),
        onReconnect: () => batcher.resync(),
      });
    } catch {
      // 构造 EventSource 失败（地址被拒绝等）时降级为不订阅
    }
    return () => {
      close?.();
      batcher.dispose();
    };
  }, [api, queryClient, orgId]);
}
