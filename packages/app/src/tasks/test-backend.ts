// 任务相关集成测试的假后端：按路径返回固定数据，记录所有请求。

import { vi } from "vitest";

import { createApi, HttpClient, type Task } from "@openfox/api";

export const ORG = "o1";
export const SPACE = "s1";

export function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

export const statuses = [
  status("st1", "待处理", "TODO", 0, true),
  status("st2", "进行中", "IN_PROGRESS", 0),
  status("st3", "已完成", "DONE", 0),
];

function status(
  id: string,
  name: string,
  category: string,
  position: number,
  isDefault = false,
) {
  return {
    id,
    name,
    description: "",
    category,
    position,
    version: 1,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    is_default: isDefault,
    collapsed_by_default: false,
    task_count: 0,
    type_usage: [],
  };
}

export const taskType = {
  organization_id: ORG,
  space_id: SPACE,
  id: "t1",
  name: "任务",
  description: "",
  enabled: true,
  is_default: true,
  version: 1,
  created_at: "2026-10-01T00:00:00Z",
  statuses: statuses.map((s, i) => ({
    status_id: s.id,
    name: s.name,
    category: s.category,
    position: i,
    default: i === 0,
  })),
};

export function makeTask(
  seq: number,
  statusId: string,
  extra: Partial<Task> = {},
): Task {
  const s = statuses.find((x) => x.id === statusId)!;
  return {
    organization_id: ORG,
    space_id: SPACE,
    id: `task-${seq}`,
    display_id: `open-${seq}`,
    sequence: seq,
    type_id: "t1",
    type_name: "任务",
    status_id: s.id,
    status_name: s.name,
    status_category: s.category as Task["status_category"],
    title: `任务标题 ${seq}`,
    description: "",
    acceptance: "",
    priority: "NONE",
    version: 1,
    created_at: "2026-10-20T00:00:00Z",
    updated_at: "2026-10-20T00:00:00Z",
    labels: [],
    start_date: null,
    due_date: null,
    parent_task_id: null,
    parent: null,
    rank: `h${seq}`,
    assignees: [],
    label_items: [],
    subtask_count: 0,
    subtask_done_count: 0,
    created_by: "u1",
    type_icon: null,
    status_color: null,
    deleted: false,
    iteration_id: null,
    project_id: null,
    triage: null,
    duplicate_of: null,
    ...extra,
  };
}

export interface Call {
  method: string;
  path: string;
  query: URLSearchParams;
  body: unknown;
  headers: Headers;
}

export type Override = (call: Call) => Response | undefined;

/** 默认数据：空间 s1 有 3 个任务（st1 两个、st2 一个）。 */
export function backend(override?: Override) {
  const calls: Call[] = [];
  let tasks = [makeTask(1, "st1"), makeTask(2, "st1"), makeTask(3, "st2")];
  const handler = (url: string, init: RequestInit): Response => {
    const parsed = new URL(url, "http://localhost");
    const path = parsed.pathname.replace(/^\/api/, "");
    const call: Call = {
      method: init.method ?? "GET",
      path,
      query: parsed.searchParams,
      body: !init.body
        ? undefined
        : init.body instanceof FormData
          ? init.body
          : (JSON.parse(String(init.body)) as unknown),
      headers: new Headers(init.headers),
    };
    calls.push(call);
    const custom = override?.(call);
    if (custom) return custom;
    const o = `/v1/organizations/${ORG}`;
    const sp = `${o}/spaces/${SPACE}`;
    if (path === "/v1/me")
      return json(200, { id: "u1", email: "lin@openfox.dev" });
    if (path === "/v1/me/organizations")
      return json(200, {
        organizations: [
          {
            id: ORG,
            slug: "lin",
            name: "林的团队",
            role: "OWNER",
            created_at: "2026-10-01T00:00:00Z",
          },
        ],
      });
    if (path === `${o}/spaces`)
      return json(200, {
        spaces: [
          {
            id: SPACE,
            prefix: "open",
            name: "默认空间",
            visibility: "shared",
            version: 1,
            created_at: "2026-10-01T00:00:00Z",
            features: {
              views: false,
              projects: false,
              iterations: false,
              triage: false,
            },
          },
        ],
      });
    if (path === `${o}/conversations`)
      return json(200, { conversations: [], next_cursor: null });
    if (path === `${o}/inbox/stats`)
      return json(200, {
        counts: {
          inbox: 2,
          inbox_unread: 2,
          inbox_important_unread: 1,
          inbox_other_unread: 0,
          later: 0,
          cleared: 0,
        },
      });
    if (path === `${o}/task-statuses`) return json(200, { statuses });
    if (path === `${sp}/task-types`)
      return json(200, { task_types: [taskType] });
    if (path === `${o}/task-types`)
      return json(200, {
        spaces: [
          {
            space_id: SPACE,
            space_name: "默认空间",
            space_prefix: "open",
            task_types: [{ id: "t1", name: "任务", icon: null, field_ids: [] }],
          },
        ],
      });
    if (path === `${o}/labels`) return json(200, { labels: [] });
    if (path === `${o}/task-relation-types`)
      return json(200, { relation_types: [] });
    if (path === `${o}/members`)
      return json(200, {
        members: [
          {
            user_id: "u1",
            email: "lin@openfox.dev",
            role: "OWNER",
            status: "ACTIVE",
            authorization_version: 1,
            created_at: "2026-10-01T00:00:00Z",
          },
          {
            user_id: "u2",
            email: "wang@openfox.dev",
            role: "MEMBER",
            status: "ACTIVE",
            authorization_version: 1,
            created_at: "2026-10-01T00:00:00Z",
          },
        ],
        next_cursor: "",
      });
    if (path === `${sp}/agents`) return json(200, { agents: [] });
    if (path === `${o}/agent-directory`)
      return json(200, { entries: [], archived_count: 0, truncated: false });
    if (path === `${sp}/tasks/counts` || path === `${o}/my-tasks/counts`) {
      const by = new Map<string, number>();
      for (const t of tasks)
        by.set(t.status_id, (by.get(t.status_id) ?? 0) + 1);
      return json(200, {
        total: tasks.length,
        by_status: [...by].map(([status_id, count]) => ({ status_id, count })),
      });
    }
    if (
      (path === `${sp}/tasks` || path === `${o}/my-tasks`) &&
      call.method === "GET"
    ) {
      const ids = call.query.getAll("status_id");
      const parent = call.query.get("parent_task_id");
      const list = tasks.filter(
        (t) =>
          (ids.length === 0 || ids.includes(t.status_id)) &&
          (!parent || t.parent_task_id === parent),
      );
      return json(200, { tasks: list, next_cursor: "" });
    }
    if (path === `${sp}/tasks` && call.method === "POST") {
      const body = call.body as { title: string };
      const created = makeTask(tasks.length + 10, "st1", { title: body.title });
      tasks = [...tasks, created];
      return json(201, created);
    }
    const patch = /\/spaces\/s1\/tasks\/(task-\d+)$/.exec(path);
    if (patch && call.method === "PATCH") {
      const current = tasks.find((t) => t.id === patch[1])!;
      const body = call.body as { status_id?: string; title?: string };
      const s = statuses.find(
        (x) => x.id === (body.status_id ?? current.status_id),
      )!;
      const next = {
        ...current,
        ...(body.title ? { title: body.title } : {}),
        status_id: s.id,
        status_name: s.name,
        status_category: s.category as Task["status_category"],
        version: current.version + 1,
      };
      tasks = tasks.map((t) => (t.id === next.id ? next : t));
      return json(200, next);
    }
    const byDisplay = /\/tasks\/by-display-id\/([^/]+)$/.exec(path);
    if (byDisplay) {
      const found = tasks.find(
        (t) =>
          t.display_id.toUpperCase() ===
          decodeURIComponent(byDisplay[1]!).toUpperCase(),
      );
      return found
        ? json(200, { ...found, description_reactions: [] })
        : json(404, {
            error: { code: "NOT_FOUND", message: "x", request_id: "r" },
          });
    }
    if (path.endsWith("/timeline"))
      return json(200, { items: [], next_cursor: null });
    if (path.endsWith("/followers"))
      return json(200, { followers: [], following: false });
    if (path.endsWith("/relations")) return json(200, { relations: [] });
    if (path.endsWith("/links")) return json(200, { links: [] });
    if (path.endsWith("/fields")) return json(200, { fields: [] });
    if (path.endsWith("/comments") && call.method === "POST")
      return json(201, {
        organization_id: ORG,
        space_id: SPACE,
        task_id: "task-1",
        id: "c1",
        author_user_id: "u1",
        body: (call.body as { body: string }).body,
        created_at: "2026-10-28T00:00:00Z",
      });
    if (path === `${o}/inbox`)
      return json(200, {
        groups: [],
        counts: {
          inbox: 0,
          inbox_unread: 0,
          inbox_important_unread: 0,
          inbox_other_unread: 0,
          later: 0,
          cleared: 0,
        },
      });
    return json(404, {
      error: { code: "NOT_FOUND", message: "x", request_id: "r" },
    });
  };
  const transport = vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
    Promise.resolve(handler(String(input), init ?? {})),
  );
  return {
    api: createApi(
      new HttpClient("/api", transport as unknown as typeof fetch),
    ),
    calls,
    find: (method: string, pathEnd: string) =>
      calls.filter((c) => c.method === method && c.path.endsWith(pathEnd)),
  };
}
