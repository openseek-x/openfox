import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 1 · 任务、我的任务、收件箱、评论、成员与 Agent 选择接口。
// 类型全部来自 schema.gen.ts，不手写。

type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;
type Query<K extends keyof operations> = NonNullable<
  operations[K]["parameters"]["query"]
>;

export type Task = S["Task"];
export type TaskDetail = S["TaskDetail"];
export type MyTask = S["MyTask"];
export type TaskPriority = S["TaskPriority"];
export type TaskAssignee = S["TaskAssignee"];
export type TaskAssigneeRef = S["TaskAssigneeRef"];
export type TaskLabelItem = S["TaskLabelItem"];
export type TaskCounts = S["TaskCounts"];
export type TaskStats = S["TaskStats"];
export type TaskComment = S["TaskComment"];
export type CommentMention = S["CommentMention"];
export type TimelineItem = S["TimelineItem"];
export type TaskGitLink = S["TaskGitLink"];
export type TimelineActivity = S["TimelineActivity"];
export type TimelineComment = S["TimelineComment"];
export type ReactionSummary = S["ReactionSummary"];
export type TaskFollower = S["TaskFollower"];
export type TaskRelation = S["TaskRelation"];
export type TaskLink = S["TaskLink"];
export type TaskFieldValue = S["TaskFieldValue"];
/** 空间任务类型（含工作流状态） */
export type SpaceTaskType = S["TaskType"];
export type TaskByDisplayID = S["TaskByDisplayID"];
export type WorkflowStatus = S["WorkflowStatus"];
export type InboxGroup = S["InboxGroup"];
export type InboxCounts = S["InboxCounts"];
export type InboxPage = S["InboxPage"];
export type InboxSection = S["InboxSection"];
export type InboxNotificationKind = S["InboxNotificationKind"];
export type OrganizationMember = S["OrganizationMember"];
export type SpaceMember = S["SpaceMember"];
export type SpaceAgent = S["SpaceAgent"];
export type AgentDirectory = S["AgentDirectory"];
export type AgentDirectoryEntry = S["AgentDirectoryEntry"];

export type TaskListQuery = Query<"listTasks">;
export type TaskCountQuery = Query<"countTasks">;
export type TaskStatsQuery = Query<"statsTasks">;
export type MyTaskStatsQuery = Query<"statsMyTasks">;
export type MyTaskListQuery = Query<"listMyTasks">;
export type InboxListQuery = Query<"listInbox">;
export type CreateTaskInput = JsonBody<"createTask">;
export type UpdateTaskInput = JsonBody<"updateTask">;
export type PostCommentInput = JsonBody<"postTaskComment">;
export type CreateTaskRelationInput = JsonBody<"createTaskRelation">;
export type CreateTaskLinkInput = JsonBody<"createTaskLink">;
export type UpdateTaskLinkInput = JsonBody<"updateTaskLink">;
/** 阶段 9c-1 · 任务移动到空间 */
export type TaskMovePreview = S["TaskMovePreview"];
export type TaskMovePreviewTask = S["TaskMovePreviewTask"];
export type TaskMoveResult = S["TaskMoveResult"];
export type PreviewTaskMoveInput = JsonBody<"previewTaskMove">;
export type MoveTaskInput = JsonBody<"moveTask">;
/** 阶段 10c · 任务批量修改（单事务，全部成功或全部失败） */
export type BatchUpdateTasksInput = JsonBody<"batchUpdateTasks">;
export type TaskBatchPatch = S["TaskBatchPatch"];
/** 阶段 10e · 任务回收站（最近删除） */
export type DeletedTask = S["DeletedTask"];
export type DeletedTaskActor = S["DeletedTaskActor"];
export type DeletedTaskPage =
  operations["listDeletedTasks"]["responses"]["200"]["content"]["application/json"];
/** 阶段 10b · 指派时选择运行对话；已指派的 Agent 再运行一次 */
export type AssignmentConversation = NonNullable<
  S["TaskUpdateRequest"]["assignment_conversation"]
>;
export type StartTaskAgentRunInput = JsonBody<"startTaskAgentRun">;
export type TaskAgentRunAccepted = S["TaskAgentRunAccepted"];
export type InboxActionInput = JsonBody<"applyInboxAction">;
export type InboxBulkInput = JsonBody<"applyInboxBulk">;

/** 一页任务；next_cursor 为空串表示没有下一页。 */
export interface TaskPageResult<T = Task> {
  tasks: T[];
  next_cursor: string;
}

const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;
const seg = encodeURIComponent;
const taskPath = (orgId: string, spaceId: string, taskId: string) =>
  `${org(orgId)}/spaces/${seg(spaceId)}/tasks/${seg(taskId)}`;

/** 任务相关方法组，由 createApi 展开到顶层。 */
export function createTasksApi(http: HttpClient) {
  return {
    tasks: {
      list: (orgId: string, spaceId: string, query: TaskListQuery = {}) =>
        http.request<TaskPageResult>(
          `${org(orgId)}/spaces/${seg(spaceId)}/tasks`,
          {
            query,
          },
        ),
      counts: (orgId: string, spaceId: string, query: TaskCountQuery = {}) =>
        http.request<TaskCounts>(
          `${org(orgId)}/spaces/${seg(spaceId)}/tasks/counts`,
          { query },
        ),
      /** 阶段 7e · 统计视图：与列表相同筛选下的服务端聚合 */
      stats: (orgId: string, spaceId: string, query: TaskStatsQuery = {}) =>
        http.request<TaskStats>(
          `${org(orgId)}/spaces/${seg(spaceId)}/tasks/stats`,
          { query },
        ),
      /** 阶段 7e · 收藏 / 取消收藏（个人偏好，幂等） */
      favorite: (orgId: string, spaceId: string, taskId: string) =>
        http.request<void>(`${taskPath(orgId, spaceId, taskId)}/favorite`, {
          method: "PUT",
        }),
      unfavorite: (orgId: string, spaceId: string, taskId: string) =>
        http.request<void>(`${taskPath(orgId, spaceId, taskId)}/favorite`, {
          method: "DELETE",
        }),
      get: (orgId: string, spaceId: string, taskId: string) =>
        http.request<TaskDetail>(taskPath(orgId, spaceId, taskId)),
      /** 按「PREFIX-n」解析，前缀不区分大小写；不可见时 404 */
      /** 阶段 3a：按旧编号（历史标识符）也能命中，canonical_display_id 为当前编号 */
      byDisplayId: (orgId: string, displayId: string) =>
        http.request<TaskByDisplayID>(
          `${org(orgId)}/tasks/by-display-id/${seg(displayId)}`,
        ),
      create: (
        orgId: string,
        spaceId: string,
        input: CreateTaskInput,
        idempotencyKey: string,
      ) =>
        http.request<Task>(`${org(orgId)}/spaces/${seg(spaceId)}/tasks`, {
          method: "POST",
          body: input,
          idempotencyKey,
        }),
      update: (
        orgId: string,
        spaceId: string,
        taskId: string,
        input: UpdateTaskInput,
      ) =>
        http.request<Task>(taskPath(orgId, spaceId, taskId), {
          method: "PATCH",
          body: input,
        }),
      /**
       * 阶段 10c · 批量修改同一空间内 1–100 个任务（patch 恰好一个字段）。
       * 任一任务失败整批回滚，error.details.task_ids 列出问题任务；Idempotency-Key 可选。
       */
      batchUpdate: (
        orgId: string,
        spaceId: string,
        input: BatchUpdateTasksInput,
        idempotencyKey?: string,
      ) =>
        http.request<{ tasks: Task[] }>(
          `${org(orgId)}/spaces/${seg(spaceId)}/tasks/batch-update`,
          { method: "POST", body: input, idempotencyKey },
        ),
      remove: (
        orgId: string,
        spaceId: string,
        taskId: string,
        version: number,
      ) =>
        http.request<void>(taskPath(orgId, spaceId, taskId), {
          method: "DELETE",
          query: { version },
        }),
      /**
       * 阶段 10e · 空间「最近删除」：空间管理员看全部（scope = all），其他人只看自己删除的（scope = mine）；
       * 按删除时间倒序，next_cursor 为空串表示没有下一页。
       */
      deleted: (
        orgId: string,
        spaceId: string,
        cursor?: string | null,
        limit = 50,
      ) =>
        http.request<DeletedTaskPage>(
          `${org(orgId)}/spaces/${seg(spaceId)}/deleted-tasks`,
          { query: { limit, cursor: cursor || undefined } },
        ),
      /**
       * 阶段 10e · 恢复已删除的任务（乐观锁）：version 是已删除任务的当前版本（删除时的 version + 1）。
       * 已恢复或版本不符 409 VERSION_CONFLICT；停用空间 409 SPACE_RETIRED。
       */
      restore: (
        orgId: string,
        spaceId: string,
        taskId: string,
        version: number,
        idempotencyKey?: string,
      ) =>
        http.request<Task>(`${taskPath(orgId, spaceId, taskId)}/restore`, {
          method: "POST",
          body: { version },
          idempotencyKey,
        }),
      /** 阶段 9c-1 · 预览移动到另一个空间（只读）：子树、字段映射、将移除的 Agent、运行数、解除的 Issue 同步 */
      movePreview: (
        orgId: string,
        spaceId: string,
        taskId: string,
        input: PreviewTaskMoveInput,
      ) =>
        http.request<TaskMovePreview>(
          `${taskPath(orgId, spaceId, taskId)}/move-preview`,
          { method: "POST", body: input },
        ),
      /**
       * 阶段 9c-1 · 把任务（含子任务）移到另一个空间。有 Agent / 运行时须 cancel_runs: true，
       * 否则 409 TASK_HAS_ACTIVE_RUNS；Idempotency-Key 必填。
       */
      move: (
        orgId: string,
        spaceId: string,
        taskId: string,
        input: MoveTaskInput,
        idempotencyKey: string,
      ) =>
        http.request<TaskMoveResult>(
          `${taskPath(orgId, spaceId, taskId)}/move`,
          {
            method: "POST",
            body: input,
            idempotencyKey,
          },
        ),
      /**
       * 阶段 10b · 让已是处理人的 Agent 再运行一次（不改处理人）：202 只表示已入队，
       * 运行在服务端处理指派事件后开始。每次调用都会入队，调用方每次用户确认生成一个
       * Idempotency-Key（操作名 task.agent_run），同一次确认内的重试复用同一个键。
       */
      startAgentRun: (
        orgId: string,
        spaceId: string,
        taskId: string,
        input: StartTaskAgentRunInput,
        idempotencyKey?: string,
      ) =>
        http.request<TaskAgentRunAccepted>(
          `${taskPath(orgId, spaceId, taskId)}/agent-runs`,
          { method: "POST", body: input, idempotencyKey },
        ),
      /** 空间任务类型（含工作流状态） */
      types: async (orgId: string, spaceId: string) => {
        const result = await http.request<{ task_types: SpaceTaskType[] }>(
          `${org(orgId)}/spaces/${seg(spaceId)}/task-types`,
        );
        return result.task_types.map((type) => ({
          ...type,
          statuses: type.statuses ?? [],
        }));
      },
      timeline: (
        orgId: string,
        spaceId: string,
        taskId: string,
        cursor?: string | null,
        thread?: string,
      ) =>
        http.request<{
          items: TimelineItem[];
          total?: number;
          next_cursor: string | null;
        }>(`${taskPath(orgId, spaceId, taskId)}/timeline`, {
          query: { limit: 100, cursor, thread },
        }),
      /** 阶段 6b-1 · 任务关联的 Pull Request 与 Commit */
      gitLinks: async (orgId: string, spaceId: string, taskId: string) =>
        (
          await http.request<{ links: TaskGitLink[] }>(
            `${taskPath(orgId, spaceId, taskId)}/git-links`,
          )
        ).links,
      followers: (orgId: string, spaceId: string, taskId: string) =>
        http.request<{ followers: TaskFollower[]; following: boolean }>(
          `${taskPath(orgId, spaceId, taskId)}/followers`,
        ),
      follow: (
        orgId: string,
        spaceId: string,
        taskId: string,
        kind: "human" | "agent",
        id: string,
      ) =>
        http.request<void>(
          `${taskPath(orgId, spaceId, taskId)}/followers/${kind}/${seg(id)}`,
          { method: "PUT" },
        ),
      unfollow: (
        orgId: string,
        spaceId: string,
        taskId: string,
        kind: "human" | "agent",
        id: string,
      ) =>
        http.request<void>(
          `${taskPath(orgId, spaceId, taskId)}/followers/${kind}/${seg(id)}`,
          { method: "DELETE" },
        ),
      relations: async (orgId: string, spaceId: string, taskId: string) =>
        (
          await http.request<{ relations: TaskRelation[] }>(
            `${taskPath(orgId, spaceId, taskId)}/relations`,
          )
        ).relations,
      createRelation: (
        orgId: string,
        spaceId: string,
        taskId: string,
        input: CreateTaskRelationInput,
        idempotencyKey: string,
      ) =>
        http.request<TaskRelation>(
          `${taskPath(orgId, spaceId, taskId)}/relations`,
          { method: "POST", body: input, idempotencyKey },
        ),
      removeRelation: (
        orgId: string,
        spaceId: string,
        taskId: string,
        relationId: string,
      ) =>
        http.request<void>(
          `${taskPath(orgId, spaceId, taskId)}/relations/${seg(relationId)}`,
          { method: "DELETE" },
        ),
      links: async (orgId: string, spaceId: string, taskId: string) =>
        (
          await http.request<{ links: TaskLink[] }>(
            `${taskPath(orgId, spaceId, taskId)}/links`,
          )
        ).links,
      createLink: (
        orgId: string,
        spaceId: string,
        taskId: string,
        input: CreateTaskLinkInput,
        idempotencyKey: string,
      ) =>
        http.request<TaskLink>(`${taskPath(orgId, spaceId, taskId)}/links`, {
          method: "POST",
          body: input,
          idempotencyKey,
        }),
      updateLink: (
        orgId: string,
        spaceId: string,
        taskId: string,
        linkId: string,
        input: UpdateTaskLinkInput,
      ) =>
        http.request<TaskLink>(
          `${taskPath(orgId, spaceId, taskId)}/links/${seg(linkId)}`,
          { method: "PATCH", body: input },
        ),
      removeLink: (
        orgId: string,
        spaceId: string,
        taskId: string,
        linkId: string,
      ) =>
        http.request<void>(
          `${taskPath(orgId, spaceId, taskId)}/links/${seg(linkId)}`,
          { method: "DELETE" },
        ),
      fields: async (orgId: string, spaceId: string, taskId: string) =>
        (
          await http.request<{ fields: TaskFieldValue[] }>(
            `${taskPath(orgId, spaceId, taskId)}/fields`,
          )
        ).fields,
      setField: (
        orgId: string,
        spaceId: string,
        taskId: string,
        fieldId: string,
        value: unknown,
      ) =>
        http.request<{ field_id: string; value: unknown; updated_at: string }>(
          `${taskPath(orgId, spaceId, taskId)}/fields/${seg(fieldId)}`,
          { method: "PUT", body: { value } },
        ),
      clearField: (
        orgId: string,
        spaceId: string,
        taskId: string,
        fieldId: string,
      ) =>
        http.request<void>(
          `${taskPath(orgId, spaceId, taskId)}/fields/${seg(fieldId)}`,
          { method: "DELETE" },
        ),
      /** 不传 commentId = 给描述加表情 */
      addReaction: (
        orgId: string,
        spaceId: string,
        taskId: string,
        emoji: string,
        commentId?: string,
      ) =>
        http.request<void>(`${taskPath(orgId, spaceId, taskId)}/reactions`, {
          method: "PUT",
          body: commentId ? { emoji, comment_id: commentId } : { emoji },
        }),
      removeReaction: (
        orgId: string,
        spaceId: string,
        taskId: string,
        emoji: string,
        commentId?: string,
      ) =>
        http.request<void>(`${taskPath(orgId, spaceId, taskId)}/reactions`, {
          method: "DELETE",
          query: { emoji, comment_id: commentId },
        }),
    },
    comments: {
      /** client_comment_id 保证幂等：同 ID 同内容返回已有评论 */
      edit: (
        orgId: string,
        spaceId: string,
        taskId: string,
        id: string,
        input: JsonBody<"editTaskComment">,
        idempotencyKey: string,
      ) =>
        http.request<void>(
          `${taskPath(orgId, spaceId, taskId)}/comments/${seg(id)}`,
          { method: "PATCH", body: input, idempotencyKey },
        ),
      remove: (
        orgId: string,
        spaceId: string,
        taskId: string,
        id: string,
        idempotencyKey: string,
      ) =>
        http.request<void>(
          `${taskPath(orgId, spaceId, taskId)}/comments/${seg(id)}`,
          { method: "DELETE", idempotencyKey },
        ),
      setResolution: (
        orgId: string,
        spaceId: string,
        taskId: string,
        id: string,
        resolved: boolean,
        idempotencyKey: string,
      ) =>
        http.request<void>(
          `${taskPath(orgId, spaceId, taskId)}/comments/${seg(id)}/resolution`,
          { method: "PUT", body: { resolved }, idempotencyKey },
        ),
      create: (
        orgId: string,
        spaceId: string,
        taskId: string,
        input: PostCommentInput,
      ) =>
        http.request<TaskComment>(
          `${taskPath(orgId, spaceId, taskId)}/comments`,
          { method: "POST", body: input },
        ),
    },
    myTasks: {
      list: (orgId: string, query: MyTaskListQuery = {}) =>
        http.request<TaskPageResult<MyTask>>(`${org(orgId)}/my-tasks`, {
          query,
        }),
      counts: (orgId: string, query: MyTaskListQuery = {}) =>
        http.request<TaskCounts>(`${org(orgId)}/my-tasks/counts`, { query }),
      stats: (orgId: string, query: MyTaskStatsQuery = {}) =>
        http.request<TaskStats>(`${org(orgId)}/my-tasks/stats`, { query }),
    },
    members: {
      /** 组织成员一页（最多 100） */
      list: (orgId: string, cursor?: string) =>
        http.request<{ members: OrganizationMember[]; next_cursor: string }>(
          `${org(orgId)}/members`,
          { query: { limit: 100, cursor } },
        ),
    },
    spaceMembers: {
      list: async (orgId: string, spaceId: string) =>
        (
          await http.request<{ members: SpaceMember[] }>(
            `${org(orgId)}/spaces/${seg(spaceId)}/members`,
          )
        ).members,
    },
    spaceAgents: {
      list: async (orgId: string, spaceId: string) =>
        (
          await http.request<{ agents: SpaceAgent[] }>(
            `${org(orgId)}/spaces/${seg(spaceId)}/agents`,
          )
        ).agents,
    },
    agentDirectory: {
      list: (orgId: string) =>
        http.request<AgentDirectory>(`${org(orgId)}/agent-directory`),
    },
  };
}

/** 收件箱列表与操作；由 createApi 合并进已有的 inbox 方法组。 */
export function createInboxMethods(http: HttpClient) {
  return {
    list: (orgId: string, query: InboxListQuery = {}) =>
      http.request<InboxPage>(`${org(orgId)}/inbox`, { query }),
    action: (orgId: string, input: InboxActionInput) =>
      http.request<{ affected: number }>(`${org(orgId)}/inbox/actions`, {
        method: "POST",
        body: input,
      }),
    bulk: (orgId: string, input: InboxBulkInput) =>
      http.request<{ affected: number }>(`${org(orgId)}/inbox/bulk`, {
        method: "POST",
        body: input,
      }),
  };
}
