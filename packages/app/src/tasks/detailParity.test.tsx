import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { Task, TaskFollower } from "@openfox/api";
import { App, createQueryClient } from "../App";
import { agent, entry } from "../agents/fixtures";
import {
  backend,
  json,
  makeTask,
  ORG,
  SPACE,
  taskType,
  type Override,
} from "./test-backend";

const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;
const relationTypes = ["BLOCKS", "CLONES", "DUPLICATES", "RELATES"].map(
  (key) => ({
    id: `builtin:${key}`,
    key,
    builtin: true,
    symmetric: key === "RELATES",
    name: "",
    inward_label: "",
    outward_label: "",
  }),
);

function setup(override?: Override) {
  const server = backend(
    (call) =>
      override?.(call) ??
      (call.path === `${o}/task-relation-types`
        ? json(200, { relation_types: relationTypes })
        : undefined),
  );
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[`/o/${ORG}/tasks/OPEN-1`]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

beforeEach(() =>
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({})),
);
afterEach(() => {
  localStorage.clear();
  sessionStorage.clear();
});

async function openRelations(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByRole("textbox", { name: "任务标题" });
  await user.click(screen.getByRole("button", { name: "任务描述：更多操作" }));
  const menu = await screen.findByRole("menu", { name: "任务描述：更多操作" });
  expect(
    within(menu)
      .getAllByRole("menuitem")
      .map((item) => item.textContent),
  ).toEqual(["添加链接", "添加关联任务"]);
  await user.click(
    within(menu).getByRole("menuitem", { name: "添加关联任务" }),
  );
  return screen.findByRole("dialog", { name: "选择要关联的任务" });
}

describe("任务指派入口", () => {
  it.each([null, []])(
    "工作流状态为 %j 时仍能打开任务详情和评论框",
    async (statuses) => {
      setup((call) =>
        call.path === `${sp}/task-types`
          ? json(200, { task_types: [{ ...taskType, statuses }] })
          : undefined,
      );
      expect(
        await screen.findByRole("textbox", { name: "任务标题" }),
      ).toHaveValue("任务标题 1");
      expect(await screen.findByPlaceholderText("评论...")).toBeVisible();
    },
  );

  it("普通评论渲染 Markdown，任务链接不进入代码和链接标签，HTML 不执行", async () => {
    setup((call) =>
      call.path.endsWith("/timeline")
        ? json(200, {
            items: [
              {
                type: "comment",
                comment: {
                  id: "markdown-comment",
                  author_kind: "AGENT",
                  author_agent_id: "a1",
                  author_name: "熊猫",
                  body: "**检查结果** OPEN-1\n\n- 第一项\n- 第二项\n\n```text\nOPEN-1\n<script>alert(1)</script>\n```\n\n`OPEN-1` [OPEN-1](https://example.test/OPEN-1)\n\n[危险](javascript:alert)\n<img src=x onerror=alert(1)>",
                  created_at: "2026-11-07T06:00:00Z",
                  reactions: [],
                  reply_count: 0,
                },
              },
            ],
            next_cursor: null,
          })
        : undefined,
    );
    expect(
      await screen.findByText("检查结果", { selector: "strong" }),
    ).toBeVisible();
    expect(screen.getByText("第一项", { selector: "li" })).toBeVisible();
    const code = screen.getByText(/<script>alert\(1\)<\/script>/, {
      selector: "code",
    });
    expect(code.closest("pre")).not.toBeNull();
    expect(code.querySelector("a")).toBeNull();
    const links = screen.getAllByRole("link", { name: "OPEN-1" });
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      `/o/${ORG}/tasks/OPEN-1`,
      "https://example.test/OPEN-1",
    ]);
    expect(links.every((link) => !link.querySelector("a"))).toBe(true);
    expect(
      screen.queryByRole("link", { name: "危险" }),
    ).not.toBeInTheDocument();
    expect(document.querySelector("img[src=x]")).toBeNull();
  });

  it("指派失败恢复原处理人", async () => {
    const user = userEvent.setup();
    const server = setup((call) =>
      call.method === "PATCH" && call.path.endsWith("/tasks/task-1")
        ? json(400, {
            error: {
              code: "ASSIGNEE_NOT_ALLOWED",
              message: "not allowed",
              request_id: "r",
            },
          })
        : undefined,
    );
    await user.click(await screen.findByRole("button", { name: "处理人" }));
    await user.click(
      await screen.findByRole("option", { name: /wang@openfox.dev/ }),
    );
    expect(
      await screen.findByText("不能指派给这个成员或 Agent"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "处理人" })).toHaveTextContent(
        "无处理人",
      ),
    );
    expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1);
  });

  it("同一入口选择人员、Agent 和小队；同值不写入，清除人员保留 Agent，清除 Agent 保留人员", async () => {
    const user = userEvent.setup();
    let task = makeTask(1, "st1", {
      assignees: [
        { kind: "HUMAN", id: "u1", name: "lin@openfox.dev" },
        { kind: "AGENT", id: "a1", name: "熊猫" },
      ],
    });
    const server = setup((call) => {
      if (call.path === `${o}/tasks/by-display-id/OPEN-1`)
        return json(200, { ...task, description_reactions: [] });
      if (call.path === `${sp}/agents`)
        return json(200, {
          agents: [
            agent("a1", { name: "熊猫" }),
            agent("a2", { name: "不可单独指派", allow_assign: false }),
          ],
        });
      if (call.path === `${o}/agent-directory`)
        return json(200, {
          entries: [
            entry("team1", {
              type: "crew",
              name: "研发小队",
              space_id: SPACE,
            }),
          ],
          archived_count: 0,
          truncated: false,
        });
      if (call.method === "PATCH" && call.path === `${sp}/tasks/task-1`) {
        const refs = (call.body as { assignees: Task["assignees"] }).assignees;
        task = {
          ...task,
          version: task.version + 1,
          assignees: refs.map((ref) => ({
            ...ref,
            name:
              ref.id === "a1"
                ? "熊猫"
                : ref.id === "u1"
                  ? "lin@openfox.dev"
                  : "wang@openfox.dev",
          })),
        };
        return json(200, task);
      }
      return undefined;
    });
    const open = async () => {
      await user.click(await screen.findByRole("button", { name: "处理人" }));
      return screen.findByRole("listbox", { name: "处理人" });
    };
    let menu = await open();
    expect(menu).toHaveAttribute("aria-multiselectable", "true");
    expect(
      within(menu).getAllByRole("option", { selected: true }),
    ).toHaveLength(2);
    expect(
      within(menu).getByRole("option", { name: /研发小队/ }),
    ).toBeInTheDocument();
    expect(
      within(menu).queryByRole("option", { name: /不可单独指派/ }),
    ).not.toBeInTheDocument();
    await user.click(
      within(menu).getByRole("option", { name: /lin@openfox.dev/ }),
    );
    expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(0);
    menu = await open();
    await user.click(within(menu).getByRole("option", { name: "无处理人" }));
    await waitFor(() =>
      expect(task.assignees).toEqual([
        { kind: "AGENT", id: "a1", name: "熊猫" },
      ]),
    );
    menu = await open();
    await user.click(
      within(menu).getByRole("option", { name: /wang@openfox.dev/ }),
    );
    await waitFor(() => expect(task.assignees).toHaveLength(2));
    menu = await open();
    await user.click(
      within(menu).getByRole("option", { name: "不指派 Agent" }),
    );
    await waitFor(() =>
      expect(task.assignees).toEqual([
        { kind: "HUMAN", id: "u2", name: "wang@openfox.dev" },
      ]),
    );
    expect(
      screen.queryByRole("listbox", { name: "处理人" }),
    ).not.toBeInTheDocument();
  });
});

describe("任务关注者多选", () => {
  it("人员和 Agent 即点即存，弹层保持打开，自身关注按钮同步，关闭重开保持结果", async () => {
    const user = userEvent.setup();
    let list: TaskFollower[] = [];
    const server = setup((call) => {
      if (call.path === `${sp}/agents`)
        return json(200, { agents: [agent("a1", { name: "熊猫" })] });
      if (call.path.endsWith("/followers"))
        return json(200, {
          followers: list,
          following: list.some(
            (item) => item.kind === "HUMAN" && item.id === "u1",
          ),
        });
      const match = /\/followers\/(human|agent)\/(\w+)$/.exec(call.path);
      if (match) {
        const item: TaskFollower = {
          kind: match[1] === "human" ? "HUMAN" : "AGENT",
          id: match[2]!,
          name: match[1] === "human" ? "lin@openfox.dev" : "熊猫",
        };
        list =
          call.method === "PUT"
            ? [...list, item]
            : list.filter((f) => f.kind !== item.kind || f.id !== item.id);
        return new Response(null, { status: 204 });
      }
      return undefined;
    });
    await user.click(await screen.findByRole("button", { name: "更改追踪者" }));
    let menu = await screen.findByRole("listbox", { name: "关注者" });
    await user.click(
      within(menu).getByRole("option", { name: /lin@openfox.dev/ }),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "取消关注" })).toBeEnabled(),
    );
    expect(menu).toBeInTheDocument();
    await user.click(within(menu).getByRole("option", { name: /熊猫/ }));
    await waitFor(() => expect(list).toHaveLength(2));
    await waitFor(() =>
      expect(
        within(menu).getByRole("option", { name: /熊猫/ }),
      ).not.toHaveAttribute("aria-disabled", "true"),
    );
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "更改追踪者" }));
    menu = await screen.findByRole("listbox", { name: "关注者" });
    expect(within(menu).getByRole("option", { name: /熊猫/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await user.click(
      within(menu).getByRole("option", { name: /lin@openfox.dev/ }),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "关注" })).toBeEnabled(),
    );
    expect(list).toEqual([{ kind: "AGENT", id: "a1", name: "熊猫" }]);
    expect(server.find("DELETE", "/followers/human/u1")).toHaveLength(1);
  });

  it("保存失败回滚选择并保留弹层，可重新尝试", async () => {
    const user = userEvent.setup();
    setup((call) =>
      call.method === "PUT" && call.path.endsWith("/followers/human/u1")
        ? json(403, {
            error: { code: "FORBIDDEN", message: "denied", request_id: "r" },
          })
        : undefined,
    );
    await user.click(await screen.findByRole("button", { name: "更改追踪者" }));
    const menu = await screen.findByRole("listbox", { name: "关注者" });
    await user.click(
      within(menu).getByRole("option", { name: /lin@openfox.dev/ }),
    );
    expect(await screen.findByText("你没有权限执行此操作")).toBeInTheDocument();
    await waitFor(() =>
      expect(
        within(menu).getByRole("option", { name: /lin@openfox.dev/ }),
      ).toHaveAttribute("aria-selected", "false"),
    );
    expect(menu).toBeInTheDocument();
  });
});

describe("关联任务", () => {
  it("移除关联同时刷新动态，最后一条移除后隐藏空区块", async () => {
    const user = userEvent.setup();
    let linked = true;
    const server = setup((call) => {
      if (call.method === "DELETE" && call.path.endsWith("/relations/r1")) {
        linked = false;
        return new Response(null, { status: 204 });
      }
      if (call.path.endsWith("/tasks/task-1/relations"))
        return json(200, {
          relations: linked
            ? [
                {
                  id: "r1",
                  key: "BLOCKS",
                  type_id: "builtin:BLOCKS",
                  direction: "INWARD",
                  task: {
                    id: "task-2",
                    display_id: "OPEN-2",
                    title: "任务标题 2",
                    status_category: "TODO",
                  },
                },
              ]
            : [],
        });
      return undefined;
    });
    const region = await screen.findByRole("region", { name: "关联" });
    const before = server.find("GET", "/tasks/task-1/timeline").length;
    await user.click(within(region).getByRole("button", { name: "删除关联" }));
    await waitFor(() => expect(region).not.toBeInTheDocument());
    await waitFor(() =>
      expect(
        server.find("GET", "/tasks/task-1/timeline").length,
      ).toBeGreaterThan(before),
    );
  });

  it.each([
    "OPEN-2",
    "https://tasks.example.com/team/task/OPEN-2?view=chat",
    "https://openfox.test/o/o1/tasks/OPEN-2",
    "https://openfox.test/o/o1/spaces/s1/tasks?task=OPEN-2",
  ])("搜索 %s，用 Enter 保存受阻于关系", async (query) => {
    const user = userEvent.setup();
    const server = setup((call) =>
      call.method === "POST" && call.path.endsWith("/relations")
        ? json(201, {})
        : undefined,
    );
    const dialog = await openRelations(user);
    const type = within(dialog).getByRole("combobox", { name: "关系类型" });
    expect(type).toHaveValue("builtin:BLOCKS|INWARD");
    expect(
      within(type)
        .getAllByRole("option")
        .map((item) => item.textContent),
    ).toEqual([
      "是…的父任务",
      "是…的子任务",
      "受阻于…",
      "阻塞了…",
      "被…克隆",
      "克隆自…",
      "被…重复",
      "重复了…",
      "与…存在关联",
    ]);
    await user.type(
      within(dialog).getByRole("combobox", {
        name: "输入、搜索或粘贴任务链接…",
      }),
      query,
    );
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: /OPEN-2/ }),
      ).toBeEnabled(),
    );
    await user.keyboard("{Enter}");
    await waitFor(() => expect(dialog).not.toBeInTheDocument());
    expect(
      server.find("POST", "/tasks/task-1/relations")[0]!.body,
    ).toMatchObject({
      type_id: "builtin:BLOCKS",
      direction: "INWARD",
      target_task_id: "task-2",
    });
    expect(
      server
        .find("POST", "/tasks/task-1/relations")[0]!
        .headers.get("Idempotency-Key"),
    ).toBeTruthy();
  });

  it.each([
    ["parent", "task-2", "task-1"],
    ["child", "task-1", "task-2"],
  ])("%s 关系写入正确的子任务并刷新", async (choice, childId, parentId) => {
    const user = userEvent.setup();
    const server = setup();
    const dialog = await openRelations(user);
    await user.selectOptions(
      within(dialog).getByRole("combobox", { name: "关系类型" }),
      choice!,
    );
    await user.click(
      await within(dialog).findByRole("button", { name: /OPEN-2/ }),
    );
    await waitFor(() => expect(dialog).not.toBeInTheDocument());
    expect(server.find("PATCH", `/tasks/${childId}`)[0]!.body).toMatchObject({
      parent_task_id: parentId,
      version: 1,
    });
    expect(server.find("POST", "/tasks/task-1/relations")).toHaveLength(0);
    await waitFor(() =>
      expect(
        server.calls.some(
          (call) =>
            call.method === "GET" &&
            call.query.get("parent_task_id") === "task-1",
        ),
      ).toBe(true),
    );
  });

  it("失败保留输入和关系类型；Esc 取消无写入，重开恢复默认类型", async () => {
    const user = userEvent.setup();
    const server = setup((call) =>
      call.method === "POST" && call.path.endsWith("/relations")
        ? json(409, {
            error: {
              code: "TASK_RELATION_EXISTS",
              message: "exists",
              request_id: "r",
            },
          })
        : undefined,
    );
    const dialog = await openRelations(user);
    await user.selectOptions(
      within(dialog).getByRole("combobox", { name: "关系类型" }),
      "builtin:RELATES|OUTWARD",
    );
    await user.click(
      await within(dialog).findByRole("button", { name: /OPEN-2/ }),
    );
    await waitFor(() =>
      expect(server.find("POST", "/tasks/task-1/relations")).toHaveLength(1),
    );
    expect(dialog).toBeInTheDocument();
    expect(
      within(dialog).getByRole("combobox", { name: "关系类型" }),
    ).toHaveValue("builtin:RELATES|OUTWARD");
    await user.keyboard("{Escape}");
    const reopened = await openRelations(user);
    expect(
      within(reopened).getByRole("combobox", { name: "关系类型" }),
    ).toHaveValue("builtin:BLOCKS|INWARD");
    await user.keyboard("{Escape}");
    expect(server.find("POST", "/tasks/task-1/relations")).toHaveLength(1);
  });

  it("未知编号显示空结果，Enter 不提交", async () => {
    const user = userEvent.setup();
    const server = setup();
    const dialog = await openRelations(user);
    await user.type(
      within(dialog).getByRole("combobox", {
        name: "输入、搜索或粘贴任务链接…",
      }),
      "OPEN-999",
    );
    expect(await within(dialog).findByText("没有找到任务")).toBeInTheDocument();
    await user.keyboard("{Enter}");
    expect(server.find("POST", "/tasks/task-1/relations")).toHaveLength(0);
  });
});
