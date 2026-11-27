import type { Page } from "@playwright/test";
import {
  agent,
  entry,
  device,
  crew,
  TS,
} from "../../../../packages/app/src/agents/fixtures";
import {
  makeTask,
  statuses,
  taskType,
} from "../../../../packages/app/src/tasks/taskFixtures";
import {
  summary,
  message,
} from "../../../../packages/app/src/conversations/messageFixtures";

// 青禾为原处理人和队长，白榆为历史回复与评论作者。每例使用独立的浏览器上下文。
export const first = "aaaaaaaa-0000-4000-8000-000000000001";
export const second = "bbbbbbbb-0000-4000-8000-000000000002";
const refs = [
  agent(first, {
    name: "青禾",
    description: "甲的独立简介",
    avatar: { kind: "mood", color: "#b91c1c", value: "·_·" },
  }),
  agent(second, {
    name: "白榆",
    description: "乙的独立简介",
    avatar: { kind: "mood", color: "#2563eb", value: "^o^" },
  }),
];
const task = makeTask(1, "st1", {
  assignees: [{ kind: "AGENT", id: first, name: "青禾" }],
});
const conversation = summary("c1", {
  agent_id: first,
  agent: { id: first, name: "青禾", title: "", online: true, avatar: null },
});
export const detail = "/o/o1/tasks/OPEN-1";
export const tasksPath = "/o/o1/spaces/s1/tasks?view=";
type Flags = {
  denied?: boolean;
  truncated?: boolean;
  grouped?: boolean;
  role?: string;
  crewCase?: "current" | "former" | "exclusive";
};
type Write = { path: string; method: string; body: Record<string, unknown> };
export async function mockProfile(page: Page, flags: Flags = {}) {
  const writes: Write[] = [];
  function members() {
    return refs
      .filter((a) => a.id === first || flags.crewCase !== "former")
      .map((a) => ({
        kind: "AGENT" as const,
        id: a.id,
        name: a.name,
        title: null,
        exclusive: a.id === second && flags.crewCase === "exclusive",
        active_tasks: [],
        online: true,
      }));
  }
  const team = crew("k1", {
    name: "测试小队",
    leader: { id: first, name: "青禾", title: "" },
    members: members(),
  });
  const session = {
    ...conversation,
    ...(flags.crewCase ? { crew_id: "k1", crew_name: "测试小队" } : {}),
  };
  await page.addInitScript((grouped) => {
    localStorage.setItem("openfox:onboarding:o1", "{}");
    if (grouped)
      localStorage.setItem(
        "openfox:tasks-display:o1:space:s1",
        JSON.stringify({ boardColumns: "assignee" }),
      );
  }, !!flags.grouped);
  await page.route("**/api/**", async (route) => {
    const request = route.request(),
      url = new URL(request.url()),
      path = url.pathname,
      method = request.method();
    let body: unknown = {},
      status = 200;
    if (!["GET", "HEAD"].includes(method))
      writes.push({ path, method, body: request.postDataJSON() });
    const space = {
      id: "s1",
      prefix: "OPEN",
      name: "测试空间",
      visibility: "shared",
      status: "active",
      features: {},
      my_role: flags.role ?? "MEMBER",
      version: 1,
    };
    const profile = refs.find((a) => path.endsWith("/agents/" + a.id));
    if (path.endsWith("/me")) body = { id: "u1", email: "test@example.test" };
    else if (path.endsWith("/me/organizations"))
      body = {
        organizations: [
          {
            id: "o1",
            slug: "test",
            name: "测试组织",
            role: "MEMBER",
          },
        ],
      };
    else if (path.endsWith("/preferences"))
      body = {
        locale: "zh-CN",
        theme: "light",
        density: "comfortable",
        command_shortcuts: {},
      };
    else if (path.endsWith("/agent-directory"))
      body = {
        entries: [
          ...refs
            .filter(
              (a) =>
                (flags.crewCase !== "exclusive" || a.id !== second) &&
                (!flags.truncated ||
                  a.id === first ||
                  url.searchParams.get("agent_id") === a.id) &&
                (!url.searchParams.has("agent_id") ||
                  url.searchParams.get("agent_id") === a.id),
            )
            .map((a) =>
              entry(a.id, {
                name: a.name,
                space_id: a.space_id,
                avatar: a.avatar,
              }),
            ),
          ...(flags.crewCase && !url.searchParams.has("agent_id")
            ? [
                entry("k1", {
                  type: "crew",
                  name: "测试小队",
                  members: members().map((m) => ({
                    id: m.id,
                    name: m.name,
                    kind: "CODE",
                  })),
                }),
              ]
            : []),
        ],
        archived_count: 0,
        truncated: !!flags.truncated && !url.searchParams.has("agent_id"),
      };
    else if (profile) {
      body = flags.denied
        ? {
            error: { code: "NOT_FOUND", message: "not found", request_id: "r" },
          }
        : profile;
      if (flags.denied) status = 404;
    } else if (path.endsWith("/crews/k1"))
      body = { ...team, members: members() };
    else if (path.endsWith("/spaces")) body = { spaces: [space] };
    else if (path.endsWith("/spaces/s1")) body = space;
    else if (path.endsWith("/members"))
      body = {
        members: [
          {
            user_id: "u1",
            email: "test@example.test",
            role: "MEMBER",
            status: "ACTIVE",
          },
        ],
      };
    else if (path.endsWith("/devices"))
      body = { devices: [device("d1", { reserved_slots: 1 })] };
    else if (path.endsWith("/agents")) body = { agents: refs };
    else if (path.endsWith("/spaces/s1/task-types"))
      body = { task_types: [taskType] };
    else if (path.endsWith("/task-types"))
      body = {
        spaces: [
          {
            space_id: "s1",
            space_name: space.name,
            space_prefix: "OPEN",
            types: [taskType],
          },
        ],
      };
    else if (path.endsWith("/task-statuses")) body = { statuses };
    else if (path.endsWith("/tasks/counts"))
      body = { total: 1, by_status: [{ status_id: "st1", count: 1 }] };
    else if (path.endsWith("/tasks") && method === "POST")
      body = {
        ...task,
        ...request.postDataJSON(),
        title: "新任务",
        id: "task-2",
        display_id: "open-2",
      };
    else if (path.endsWith("/tasks"))
      body = {
        tasks:
          !url.searchParams.has("status_id") ||
          url.searchParams.getAll("status_id").includes("st1")
            ? [task]
            : [],
        next_cursor: null,
      };
    else if (
      path.includes("/tasks/by-display-id/") ||
      path.endsWith("/tasks/task-1")
    )
      body = { ...task, description_reactions: [] };
    else if (path.endsWith("/timeline"))
      body = {
        items: [
          {
            type: "comment",
            comment: {
              id: "com",
              author_kind: "AGENT",
              author_name: "白榆",
              author_agent_id: second,
              body: "评审记录 @[青禾](openfox://mention/agent/" + first + ")",
              created_at: TS,
              reactions: [],
              reply_count: 0,
            },
          },
          {
            type: "activity",
            activity: {
              id: "act",
              kind: "task.via_agent",
              safe_diff: { agent_id: first },
              actor_name: "测试成员",
              created_at: TS,
            },
          },
        ],
        next_cursor: null,
      };
    else if (path.endsWith("/conversations") && method === "POST") {
      status = 201;
      body = summary(request.postDataJSON().id, {
        agent_id: request.postDataJSON().agent_id,
        agent: {
          id: second,
          name: "白榆",
          title: "",
          online: true,
          avatar: null,
        },
      });
    } else if (path.endsWith("/messages") && method === "POST") {
      status = 201;
      body = message(1, {
        body: request.postDataJSON().body,
        conversation_id: path.split("/").at(-2),
      });
    } else if (path.endsWith("/conversations/c1/messages"))
      body = {
        messages: [message(2, { author_agent_id: second, body: "独立回复" })],
        next_cursor: 0,
      };
    else if (path.endsWith("/conversations/c1")) body = session;
    else if (path.endsWith("/conversations"))
      body = {
        conversations: url.searchParams.has("task_id") ? [] : [session],
        next_cursor: null,
      };
    else if (path.endsWith("/direct-chats")) body = { chats: [] };
    else if (path.endsWith("/inbox"))
      body = {
        counts: { inbox: 1, inbox_unread: 0, later: 0, cleared: 0 },
        groups: [
          {
            id: "g1",
            notification_id: "n",
            kind: "TASK_COMMENT",
            task_id: "task-1",
            task_display_id: "open-1",
            task_title: "通知中的任务",
            task_status_category: "TODO",
            space_id: "s1",
            actor_kind: "AGENT",
            actor_agent_id: second,
            actor_name: "白榆",
            count: 1,
            unread: false,
            latest_at: TS,
            safe_summary: "评论提醒",
          },
        ],
      };
    else if (path.endsWith("/inbox/stats"))
      body = { counts: { inbox: 1, inbox_unread: 0, later: 0, cleared: 0 } };
    else {
      const key = path.split("/").at(-1)!;
      if (
        [
          "favorites",
          "relations",
          "links",
          "timeline",
          "repos",
          "labels",
          "followers",
          "fields",
          "invitations",
          "attachments",
          "views",
          "direct-chats",
        ].includes(key)
      )
        body = { [key]: [], next_cursor: null };
      else {
        status = 404;
        body = {
          error: {
            code: "NOT_FOUND",
            message: "not in fixture",
            request_id: "r",
          },
        };
      }
    }
    await route.fulfill({ status, json: body });
  });
  return writes;
}
