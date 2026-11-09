import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type QueryClient,
  type QueryKey,
} from "@tanstack/react-query";
import { useMemo } from "react";

import {
  isApiError,
  type CreateTaskInput,
  type MyTaskListQuery,
  type Task,
  type TaskCounts,
  type TaskDetail,
  type TaskListQuery,
  type TaskPageResult,
  type TaskStats,
  type UpdateTaskInput,
} from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../context";
import { restoreDeletedTask } from "./trash/queries";

export const COLUMN_PAGE_SIZE = 50;

/** 带「撤销」的提示显示更久，留出点击时间（毫秒） */
export const UNDO_TOAST_MS = 6000;

type TaskPages = InfiniteData<TaskPageResult<Task>, string>;

/** 任务数据来源：空间任务或我的任务，二者共用视图组件。 */
export interface TaskSource {
  /** 查询键里的范围：space:<id> 或 my */
  scope: string;
  list: (query: TaskListQuery) => Promise<TaskPageResult<Task>>;
  counts: (query: TaskListQuery) => Promise<TaskCounts>;
  /** 统计视图聚合（与列表同筛选）；tz 为 IANA 时区名 */
  stats: (query: TaskListQuery, tz: string) => Promise<TaskStats>;
  /** 是否允许手动排序（我的任务跨空间，不支持） */
  manualSort: boolean;
}

export function useSpaceSource(orgId: string, spaceId: string): TaskSource {
  const api = useApi();
  return useMemo(
    () => ({
      scope: `space:${spaceId}`,
      list: (query) => api.tasks.list(orgId, spaceId, query),
      counts: (query) => api.tasks.counts(orgId, spaceId, query),
      stats: (query, tz) => api.tasks.stats(orgId, spaceId, { ...query, tz }),
      manualSort: true,
    }),
    [api, orgId, spaceId],
  );
}

/** 我的任务跨空间，不支持按估时排序（只有空间任务列表支持）；其余参数原样透传。 */
function myQuery(
  query: TaskListQuery,
  extra: Pick<MyTaskListQuery, "scope" | "space_id">,
): MyTaskListQuery {
  const { sort, ...rest } = query;
  return {
    ...rest,
    sort:
      sort === "estimate" || sort === "iteration" || sort === "project"
        ? undefined
        : sort,
    ...extra,
  };
}

export function useMySource(
  orgId: string,
  extra: Pick<MyTaskListQuery, "scope" | "space_id">,
): TaskSource {
  const api = useApi();
  const spaceKey = (extra.space_id ?? []).join(",");
  return useMemo(
    () => ({
      scope: `my:${extra.scope ?? "all"}:${spaceKey}`,
      list: (query) => api.myTasks.list(orgId, myQuery(query, extra)),
      counts: (query) => api.myTasks.counts(orgId, myQuery(query, extra)),
      stats: (query, tz) =>
        api.myTasks.stats(orgId, { ...myQuery(query, extra), tz }),
      manualSort: false,
    }),
    // extra 按值比较：scope 与空间列表不变时复用同一个来源
    [api, orgId, extra.scope, spaceKey],
  );
}

/** 一列（或一个分组、或平铺列表）的任务，keyset 游标分页。 */
export function useTaskPages(
  orgId: string,
  source: TaskSource,
  params: TaskListQuery,
  column: string | null,
  enabled = true,
) {
  return useInfiniteQuery({
    queryKey: keys.taskList(orgId, source.scope, params, column),
    queryFn: ({ pageParam }) =>
      source.list({
        ...params,
        ...(column ? { status_id: [column] } : {}),
        limit: COLUMN_PAGE_SIZE,
        cursor: pageParam || undefined,
      }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled,
  });
}

export function useTaskCounts(
  orgId: string,
  source: TaskSource,
  params: TaskListQuery,
) {
  return useQuery({
    queryKey: keys.taskCounts(orgId, source.scope, params),
    queryFn: () => source.counts(params),
  });
}

/** 统计视图：服务端聚合，随任务变更一起失效。 */
export function useTaskStats(
  orgId: string,
  source: TaskSource,
  params: TaskListQuery,
  enabled = true,
) {
  const tz = browserTimeZone();
  return useQuery({
    queryKey: keys.taskStats(orgId, source.scope, params, tz),
    queryFn: () => source.stats(params, tz),
    enabled,
  });
}

/** 浏览器所在时区的 IANA 名称；取不到时交给服务端按 UTC 处理。 */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/** 收藏 / 取消收藏：乐观更新详情与列表里的 favorited，失败回滚并提示。 */
export function useToggleFavorite(orgId: string) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const patch = (taskId: string, favorited: boolean) => {
    queryClient.setQueriesData<TaskDetail>(
      { queryKey: keys.taskDetails(orgId) },
      (data) => (data?.id === taskId ? { ...data, favorited } : data),
    );
    queryClient.setQueriesData<TaskPages>(
      { queryKey: keys.taskLists(orgId) },
      (data) =>
        data && {
          ...data,
          pages: data.pages.map((page) => ({
            ...page,
            tasks: page.tasks.map((task) =>
              task.id === taskId ? { ...task, favorited } : task,
            ),
          })),
        },
    );
  };
  return useMutation({
    mutationFn: ({
      task,
      favorite,
    }: {
      task: Pick<Task, "id" | "space_id">;
      favorite: boolean;
    }) =>
      favorite
        ? api.tasks.favorite(orgId, task.space_id, task.id)
        : api.tasks.unfavorite(orgId, task.space_id, task.id),
    onMutate: ({ task, favorite }) => {
      patch(task.id, favorite);
    },
    onError: (_error, { task, favorite }) => {
      patch(task.id, !favorite);
      toast(t("tasks.favoriteFailed"), { tone: "error" });
    },
    // 「我的任务 · 收藏」范围的列表与计数以服务端为准
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) }),
  });
}

// ---------- 配置与人员 ----------

export function useStatuses(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.taskStatuses(orgId),
    queryFn: () => api.taskStatuses.list(orgId),
  });
}

export function useSpaceTypes(orgId: string, spaceId: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: keys.spaceTaskTypes(orgId, spaceId ?? ""),
    queryFn: () => api.tasks.types(orgId, spaceId!),
    enabled: Boolean(spaceId),
    select: (types) =>
      types.filter((type) => type.enabled && !type.archived_at),
  });
}

/** 任务标签选择器：有空间时 = 组织级 + 该空间标签，否则只有组织级 */
export function usePickerLabels(orgId: string, spaceId?: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.labels(
      orgId,
      spaceId ? `picker:${spaceId}` : "organization",
    ),
    queryFn: () =>
      spaceId
        ? api.labels.list(orgId, "picker", spaceId)
        : api.labels.list(orgId, "organization"),
  });
}

export function useRelationTypes(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.relationTypes(orgId),
    queryFn: () => api.relationTypes.list(orgId),
  });
}

export interface Person {
  id: string;
  email: string;
  /** 全名；没有设置时为 undefined，界面回退到邮箱 */
  name?: string;
  avatar_url?: string | null;
}

/** 人员显示名：全名优先，没有全名时用邮箱。 */
export function personName(person: Pick<Person, "name" | "email">): string {
  return person.name || person.email;
}

const MEMBER_PAGES = 5;

/** 组织 ACTIVE 成员（最多 500 人；无权限时为空列表）。 */
export function useOrgPeople(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.members(orgId),
    queryFn: async () => {
      const people: Person[] = [];
      let cursor: string | undefined;
      for (let i = 0; i < MEMBER_PAGES; i++) {
        const page = await api.members.list(orgId, cursor);
        for (const m of page.members)
          if (m.status === "ACTIVE")
            people.push({
              id: m.user_id,
              email: m.email,
              name: m.full_name || undefined,
              avatar_url: m.avatar_url,
            });
        if (!page.next_cursor) break;
        cursor = page.next_cursor;
      }
      return people;
    },
    staleTime: 5 * 60_000,
  });
}

/** 能被指派的人员：私有空间用空间成员，共享空间用组织成员。 */
export function usePeople(
  orgId: string,
  space: { id: string; visibility: string } | undefined,
) {
  const api = useApi();
  const privateSpace = space?.visibility === "private";
  const org = useOrgPeople(orgId);
  const members = useQuery({
    queryKey: keys.spaceMembers(orgId, space?.id ?? ""),
    queryFn: async () =>
      (await api.spaceMembers.list(orgId, space!.id))
        .filter((m) => m.status === "ACTIVE")
        .map((m) => ({ id: m.user_id, email: m.email })),
    enabled: privateSpace,
    staleTime: 5 * 60_000,
  });
  return privateSpace ? (members.data ?? []) : (org.data ?? []);
}

export function useSpaceAgents(orgId: string, spaceId: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: keys.spaceAgents(orgId, spaceId ?? ""),
    queryFn: () => api.spaceAgents.list(orgId, spaceId!),
    enabled: Boolean(spaceId),
    staleTime: 5 * 60_000,
  });
}

export function useAgentDirectory(orgId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: keys.agentDirectory(orgId),
    queryFn: () => api.agentDirectory.list(orgId),
    staleTime: 5 * 60_000,
    enabled,
  });
}

export function useTaskByDisplayId(orgId: string, displayId: string | null) {
  const api = useApi();
  return useQuery({
    queryKey: keys.taskDetail(orgId, displayId ?? ""),
    queryFn: () => api.tasks.byDisplayId(orgId, displayId!),
    enabled: Boolean(displayId),
  });
}

// ---------- 缓存工具 ----------

function isListKey(key: QueryKey): boolean {
  return key[3] === "list";
}

/** 在所有列表缓存里查找任务的最新版本（详情缓存优先）。 */
export function latestTask(
  queryClient: QueryClient,
  orgId: string,
  taskId: string,
): Task | undefined {
  let found: Task | undefined;
  const consider = (task: Task | undefined) => {
    if (task?.id === taskId && (!found || task.version > found.version))
      found = task;
  };
  // 只看列表与详情缓存；计数、时间线等子资源缓存的形状不同
  for (const [, data] of queryClient.getQueriesData<TaskPages>({
    queryKey: keys.taskLists(orgId),
  }))
    for (const page of data?.pages ?? [])
      for (const task of page.tasks) consider(task);
  for (const [, data] of queryClient.getQueriesData<TaskDetail>({
    queryKey: keys.taskDetails(orgId),
  }))
    consider(data);
  return found;
}

/**
 * 乐观地把任务写进缓存：平铺列表原地替换；状态列缓存按新状态移入 / 移出，
 * index 为目标列（不含该任务）中的落点，缺省时放在列首。计数同步加减。
 */
export function applyTaskToCache(
  queryClient: QueryClient,
  orgId: string,
  next: Task,
  index?: number,
) {
  const touchedScopes = new Map<string, string>();
  for (const [key, data] of queryClient.getQueriesData<TaskPages>({
    queryKey: keys.taskLists(orgId),
  })) {
    if (!data || !isListKey(key)) continue;
    const scope = String(key[4]);
    const column = (key[6] as string | null) ?? null;
    const prev = data.pages
      .flatMap((p) => p.tasks)
      .find((t) => t.id === next.id);
    if (prev && !touchedScopes.has(scope))
      touchedScopes.set(scope, prev.status_id);
    let pages = data.pages.map((page) => ({
      ...page,
      tasks:
        column && column !== next.status_id
          ? page.tasks.filter((t) => t.id !== next.id)
          : page.tasks.map((t) => (t.id === next.id ? next : t)),
    }));
    if (column && column === next.status_id) {
      const flat = pages
        .flatMap((p) => p.tasks)
        .filter((t) => t.id !== next.id);
      const wasHere = data.pages.some((p) =>
        p.tasks.some((t) => t.id === next.id),
      );
      if (index !== undefined || !wasHere) {
        const at = Math.max(0, Math.min(index ?? 0, flat.length));
        flat.splice(at, 0, next);
        // 重新按原页大小切分，保持游标页数不变
        let offset = 0;
        pages = pages.map((page, i) => {
          const size =
            i === pages.length - 1
              ? flat.length - offset
              : data.pages[i]!.tasks.length;
          const tasks = flat.slice(offset, offset + size);
          offset += size;
          return { ...page, tasks };
        });
      }
    }
    queryClient.setQueryData<TaskPages>(key, { ...data, pages });
  }
  // 计数：只调整包含该任务的范围
  for (const [key, data] of queryClient.getQueriesData<TaskCounts>({
    queryKey: [...keys.tasks(orgId), "counts"],
  })) {
    if (!data) continue;
    const from = touchedScopes.get(String(key[4]));
    if (!from || from === next.status_id) continue;
    const by = new Map(data.by_status.map((s) => [s.status_id, s.count]));
    by.set(from, Math.max(0, (by.get(from) ?? 0) - 1));
    by.set(next.status_id, (by.get(next.status_id) ?? 0) + 1);
    queryClient.setQueryData<TaskCounts>(key, {
      ...data,
      by_status: [...by]
        .filter(([, count]) => count > 0)
        .map(([status_id, count]) => ({ status_id, count })),
    });
  }
  queryClient.setQueriesData<TaskDetail>(
    { queryKey: keys.taskDetails(orgId) },
    (detail) =>
      detail && detail.id === next.id ? { ...detail, ...next } : detail,
  );
}

/** 从所有列表缓存移除任务（删除后）。 */
export function removeTaskFromCache(
  queryClient: QueryClient,
  orgId: string,
  taskId: string,
) {
  queryClient.setQueriesData<TaskPages>(
    { queryKey: keys.taskLists(orgId) },
    (data) =>
      data && {
        ...data,
        pages: data.pages.map((page) => ({
          ...page,
          tasks: page.tasks.filter((t) => t.id !== taskId),
        })),
      },
  );
}

/** 任务写接口的错误提示文案 */
export function taskErrorMessage(t: Translate, error: unknown): string {
  if (!isApiError(error)) return t("tasks.errors.failed");
  switch (error.code) {
    case "TRIAGE_STATUS_MISSING":
      return t("spaces.triage.errors.statusMissing");
    case "THREAD_RESOLVED":
      return t("tasks.detail.threadResolved");
    case "VERSION_CONFLICT":
      return t("tasks.errors.versionConflict");
    case "LABEL_GROUP_EXCLUSIVE":
      return t("tasks.errors.labelGroupExclusive");
    case "INVALID_LABEL":
      return t("tasks.errors.invalidLabel");
    case "ASSIGNEE_NOT_ALLOWED":
      return t("tasks.errors.assigneeNotAllowed");
    case "INVALID_POSITION":
      return t("tasks.errors.invalidPosition");
    case "IDEMPOTENCY_CONFLICT":
      return t("tasks.errors.idempotencyConflict");
    case "TASK_HIERARCHY_CYCLE":
      return t("tasks.errors.hierarchyCycle");
    case "TASK_HIERARCHY_TOO_DEEP":
      return t("tasks.errors.hierarchyTooDeep");
    case "RUN_ACTIVE":
      return t("tasks.errors.runActive");
    case "INVALID_DATE_RANGE":
      return t("tasks.errors.dateRange");
    case "LIMIT_REACHED":
      return t("tasks.errors.limitReached");
    case "NAME_TAKEN":
      return t("tasks.errors.nameTaken");
    case "RELATION_EXISTS":
      return t("tasks.errors.relationExists");
    case "LINK_EXISTS":
      return t("tasks.errors.linkExists");
    // 停用空间只读、估时校验。
    case "SPACE_RETIRED":
      return t("spaces.errors.retired");
    case "ESTIMATION_DISABLED":
      return t("spaces.errors.estimationDisabled");
    case "INVALID_ESTIMATE":
      return t("spaces.errors.invalidEstimate");
    case "NETWORK_ERROR":
    case "TIMEOUT":
      return t("tasks.errors.uncertain");
    default:
      return error.status === 403
        ? t("tasks.errors.forbidden")
        : t("tasks.errors.failed");
  }
}

export interface UpdateTaskVars {
  task: Task;
  patch: Omit<UpdateTaskInput, "version">;
  /** 乐观显示的字段（如状态名、处理人列表），缺省只合并 patch 里与 Task 同名的标量 */
  optimistic?: Partial<Task>;
  /** 看板移动：目标列中的落点 */
  index?: number;
}

/**
 * 更新任务：乐观写缓存，失败回滚并提示；409 VERSION_CONFLICT 时刷新。
 * 同组织的任务写操作串行执行，发送时取缓存里的最新 version。
 */
export function useUpdateTask(orgId: string) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  return useMutation({
    scope: { id: `task-write:${orgId}` },
    mutationFn: ({ task, patch }: UpdateTaskVars) => {
      const version =
        latestTask(queryClient, orgId, task.id)?.version ?? task.version;
      return api.tasks.update(orgId, task.space_id, task.id, {
        ...patch,
        version,
      });
    },
    onMutate: async ({ task, patch, optimistic, index }) => {
      await queryClient.cancelQueries({ queryKey: keys.tasks(orgId) });
      const snapshot = queryClient.getQueriesData({
        queryKey: keys.tasks(orgId),
      });
      const scalar: Partial<Task> = {};
      for (const field of [
        "title",
        "description",
        "priority",
        "start_date",
        "due_date",
        "status_id",
      ] as const)
        if (field in patch)
          (scalar as Record<string, unknown>)[field] = patch[field];
      const current = latestTask(queryClient, orgId, task.id) ?? task;
      applyTaskToCache(
        queryClient,
        orgId,
        { ...current, ...scalar, ...optimistic },
        index,
      );
      return { snapshot };
    },
    onError: (error, _vars, context) => {
      for (const [key, data] of context?.snapshot ?? [])
        queryClient.setQueryData(key, data);
      toast(taskErrorMessage(t, error), { tone: "error" });
      // 版本冲突或业务拒绝（如状态不在工作流里）后以服务端为准刷新
      if (isApiError(error))
        void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
    },
    onSuccess: (saved, { patch }) => {
      applyTaskToCache(queryClient, orgId, saved);
      void queryClient.invalidateQueries({
        queryKey: [...keys.tasks(orgId), "counts"],
      });
      if ("parent_task_id" in patch)
        void queryClient.invalidateQueries({ queryKey: keys.taskLists(orgId) });
      void queryClient.invalidateQueries({
        queryKey: [...keys.tasks(orgId), "sub", saved.id, "timeline"],
      });
    },
  });
}

export function useCreateTask(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (vars: {
      spaceId: string;
      input: CreateTaskInput;
      idempotencyKey: string;
    }) =>
      api.tasks.create(orgId, vars.spaceId, vars.input, vars.idempotencyKey),
    onSuccess: (task) => {
      if (task.triage)
        void queryClient.invalidateQueries({
          queryKey: keys.triage(orgId, task.space_id),
        });
      void queryClient.invalidateQueries({ queryKey: keys.taskLists(orgId) });
      void queryClient.invalidateQueries({
        queryKey: [...keys.tasks(orgId), "counts"],
      });
      const agentIds = new Set(
        task.assignees.filter((a) => a.kind === "AGENT").map((a) => a.id),
      );
      if (task.executor_agent_id) agentIds.add(task.executor_agent_id);
      for (const agentId of agentIds)
        void queryClient.invalidateQueries({
          queryKey: keys.agentSub(orgId, agentId, "stats"),
        });
      if (task.parent_task_id) {
        void queryClient.invalidateQueries({
          queryKey: keys.taskDetails(orgId),
        });
        // 完整表单建子任务：父任务详情里的子任务列表也要刷新。
        void queryClient.invalidateQueries({
          queryKey: keys.taskSub(orgId, task.parent_task_id, "subtasks"),
        });
      }
    },
  });
}

export function useDeleteTask(orgId: string) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  return useMutation({
    scope: { id: `task-write:${orgId}` },
    // 返回删除后的版本：删除成功时服务端把 version 加 1（见契约 deleteTask），「撤销」据此调恢复接口
    mutationFn: async (task: Task) => {
      const version =
        latestTask(queryClient, orgId, task.id)?.version ?? task.version;
      await api.tasks.remove(orgId, task.space_id, task.id, version);
      return version + 1;
    },
    onSuccess: (deletedVersion, task) => {
      removeTaskFromCache(queryClient, orgId, task.id);
      // 删除提示带「撤销」：调恢复接口（不依赖详情抽屉，抽屉关闭后仍可用）。
      const undo = () =>
        restoreDeletedTask(api, queryClient, orgId, {
          spaceId: task.space_id,
          taskId: task.id,
          version: deletedVersion,
        }).then(
          (restored) =>
            toast(
              t("tasks.trash.restored", {
                id: restored.display_id.toUpperCase(),
              }),
            ),
          (error: unknown) =>
            toast(
              isApiError(error, "VERSION_CONFLICT")
                ? t("tasks.trash.conflict")
                : taskErrorMessage(t, error),
              { tone: "error" },
            ),
        );
      toast(t("tasks.detail.deleted"), {
        action: { label: t("tasks.trash.undo"), onClick: () => void undo() },
        duration: UNDO_TOAST_MS,
      });
      void queryClient.invalidateQueries({ queryKey: keys.taskLists(orgId) });
      void queryClient.invalidateQueries({
        queryKey: [...keys.tasks(orgId), "counts"],
      });
    },
    onError: (error) => {
      toast(taskErrorMessage(t, error), { tone: "error" });
      if (isApiError(error, "VERSION_CONFLICT"))
        void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
    },
  });
}
