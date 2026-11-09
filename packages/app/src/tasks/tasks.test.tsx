import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import { entry } from "../agents/fixtures";
import { backend, json, ORG, SPACE, type Override } from "./test-backend";

function renderAt(path: string, override?: Override) {
  const server = backend(override);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

const spaceTasks = `/o/${ORG}/spaces/${SPACE}/tasks`;

beforeEach(() => {
  // 跳过首登向导
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("空间任务看板", () => {
  it("按工作流状态渲染列与计数，空列收进「折叠的列」", async () => {
    renderAt(spaceTasks);
    const todo = await screen.findByRole("region", { name: "待处理 2" });
    expect(await within(todo).findByText("OPEN-1")).toBeInTheDocument();
    expect(await within(todo).findByText("任务标题 2")).toBeInTheDocument();
    const doing = screen.getByRole("region", { name: "进行中 1" });
    expect(await within(doing).findByText("任务标题 3")).toBeInTheDocument();
    const collapsed = screen.getByRole("region", { name: "折叠的列" });
    expect(collapsed).toHaveTextContent("已完成");
    // 每列单独分页请求，带 status_id 与 limit=50
    expect(screen.getByRole("heading", { name: "任务" })).toBeInTheDocument();
  });

  it("卡片菜单移动到其他状态：PATCH 带 status_id 与 version，失败时回滚并提示", async () => {
    const user = userEvent.setup();
    let fail = true;
    const server = renderAt(spaceTasks, (call) =>
      call.method === "PATCH" && fail
        ? json(409, {
            error: { code: "VERSION_CONFLICT", message: "x", request_id: "r" },
          })
        : undefined,
    );
    const todo = await screen.findByRole("region", { name: "待处理 2" });
    await within(todo).findByText("任务标题 1");
    const card = within(todo).getByRole("button", {
      name: /OPEN-1 任务标题 1/,
    });
    await user.click(within(card).getByRole("button", { name: "任务操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "移动到" }));
    await user.click(
      await screen.findByRole("menuitemradio", { name: "进行中" }),
    );

    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/tasks/task-1")[0]!.body).toEqual({
      status_id: "st2",
      version: 1,
    });
    expect(
      await screen.findByText("任务已被其他人修改，已刷新为最新内容"),
    ).toBeInTheDocument();
    // 回滚：卡片仍在「待处理」
    await waitFor(() =>
      expect(
        within(screen.getByRole("region", { name: "待处理 2" })).getByText(
          "任务标题 1",
        ),
      ).toBeInTheDocument(),
    );

    fail = false;
    const again = within(
      screen.getByRole("region", { name: "待处理 2" }),
    ).getByRole("button", {
      name: /OPEN-1 任务标题 1/,
    });
    await user.click(within(again).getByRole("button", { name: "任务操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "移动到" }));
    await user.click(
      await screen.findByRole("menuitemradio", { name: "进行中" }),
    );
    const doing = await screen.findByRole("region", { name: "进行中 2" });
    expect(await within(doing).findByText("任务标题 1")).toBeInTheDocument();
  });

  it("筛选：选优先级后切换为「不是」，请求带 priority_not", async () => {
    const user = userEvent.setup();
    const server = renderAt(spaceTasks);
    await screen.findByRole("region", { name: "待处理 2" });
    await user.click(screen.getByRole("button", { name: "筛选" }));
    await user.click(await screen.findByRole("menuitem", { name: "优先级" }));
    await user.click(await screen.findByRole("option", { name: "高" }));
    await user.keyboard("{Escape}");
    const chip = await screen.findByRole("group", { name: "优先级" });
    expect(chip).toHaveTextContent("是");
    await waitFor(() =>
      expect(
        server.calls.some(
          (c) =>
            c.path.endsWith("/tasks/counts") &&
            c.query.getAll("priority").includes("HIGH"),
        ),
      ).toBe(true),
    );
    await user.click(within(chip).getByRole("button", { name: "切换运算符" }));
    await waitFor(() =>
      expect(
        server.calls.some(
          (c) =>
            c.path.endsWith("/tasks/counts") &&
            c.query.getAll("priority_not").includes("HIGH") &&
            !c.query.has("priority"),
        ),
      ).toBe(true),
    );
    expect(screen.getByText("3 个匹配任务")).toBeInTheDocument();
  });
});

describe("新建任务", () => {
  it("可将任务创建给待接受邀请，只提交邀请 ID", async () => {
    const user = userEvent.setup();
    const server = renderAt(spaceTasks, (call) =>
      call.path.endsWith("/invites")
        ? json(200, {
            invitations: [
              {
                id: "iv",
                email: "future@example.test",
                role: "MEMBER",
                status: "PENDING",
                expires_at: "2099-01-01",
              },
              {
                id: "guest",
                email: "guest@example.test",
                role: "GUEST",
                status: "PENDING",
                expires_at: "2099-01-01",
              },
            ],
          })
        : undefined,
    );
    await screen.findByRole("region", { name: "待处理 2" });
    await user.keyboard("c");
    const dialog = await screen.findByRole("dialog", { name: "新建任务" });
    await user.click(within(dialog).getByRole("button", { name: /^处理人/ }));
    const candidate = await screen.findByRole("option", {
      name: /future@example.test/,
    });
    expect(candidate).toHaveTextContent("待接受");
    expect(screen.queryByText("guest@example.test")).toBeNull();
    await user.click(candidate);
    await user.type(
      within(dialog).getByRole("textbox", { name: "标题" }),
      "待接受的处理人",
    );
    await user.click(within(dialog).getByRole("button", { name: "创 建" }));
    await waitFor(() =>
      expect(
        server.find("POST", `/spaces/${SPACE}/tasks`)[0]?.body,
      ).toMatchObject({ assignees: [{ kind: "INVITATION", id: "iv" }] }),
    );
  });

  it("快捷键 C 打开；提交带默认状态、优先级 NONE 与幂等键；继续创建保留弹窗并换新键", async () => {
    const user = userEvent.setup();
    const server = renderAt(spaceTasks);
    await screen.findByRole("region", { name: "待处理 2" });
    await user.keyboard("c");
    const dialog = await screen.findByRole("dialog", { name: "新建任务" });
    const submit = within(dialog).getByRole("button", { name: "创 建" });
    expect(submit).toBeDisabled();
    await user.type(
      within(dialog).getByRole("textbox", { name: "标题" }),
      "修复登录",
    );
    await user.click(within(dialog).getByRole("switch", { name: "继续创建" }));
    await user.click(submit);

    await waitFor(() =>
      expect(server.find("POST", `/spaces/${SPACE}/tasks`)).toHaveLength(1),
    );
    const first = server.find("POST", `/spaces/${SPACE}/tasks`)[0]!;
    expect(first.body).toEqual({
      type_id: "t1",
      title: "修复登录",
      priority: "NONE",
      status_id: "st1",
    });
    const key1 = first.headers.get("Idempotency-Key");
    expect(key1).toMatch(/^[a-f0-9]{32}$/);
    expect(await within(dialog).findByText("已创建 1 个")).toBeInTheDocument();
    expect(within(dialog).getByRole("textbox", { name: "标题" })).toHaveValue(
      "",
    );
    expect(await screen.findByText("已创建 OPEN-13")).toBeInTheDocument();

    await user.type(
      within(dialog).getByRole("textbox", { name: "标题" }),
      "第二个",
    );
    await user.click(within(dialog).getByRole("button", { name: "创 建" }));
    await waitFor(() =>
      expect(server.find("POST", `/spaces/${SPACE}/tasks`)).toHaveLength(2),
    );
    const key2 = server
      .find("POST", `/spaces/${SPACE}/tasks`)[1]!
      .headers.get("Idempotency-Key");
    expect(key2).toMatch(/^[a-f0-9]{32}$/);
    expect(key2).not.toBe(key1);
  });

  it("有内容时关闭需要确认", async () => {
    const user = userEvent.setup();
    renderAt(spaceTasks);
    await screen.findByRole("region", { name: "待处理 2" });
    await user.keyboard("c");
    const dialog = await screen.findByRole("dialog", { name: "新建任务" });
    await user.type(
      within(dialog).getByRole("textbox", { name: "标题" }),
      "草稿",
    );
    await user.click(within(dialog).getByRole("button", { name: "关闭" }));
    expect(
      await screen.findByRole("dialog", { name: "放弃这个任务？" }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "放弃" }));
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "新建任务" }),
      ).not.toBeInTheDocument(),
    );
  });
});

describe("任务详情", () => {
  it("?task= 打开抽屉；改标题带 version；评论 @ 提及带 mentions 与 client_comment_id", async () => {
    const user = userEvent.setup();
    const server = renderAt(`${spaceTasks}?task=OPEN-1`);
    const title = await screen.findByRole("textbox", { name: "任务标题" });
    await user.clear(title);
    await user.type(title, "新的标题{Enter}");
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/tasks/task-1")[0]!.body).toEqual({
      title: "新的标题",
      version: 1,
    });

    const box = screen.getByRole("textbox", { name: "评论..." });
    // 菜单打开后继续在评论框里输入关键字，选择后关键字被替换而不是保留
    await user.type(box, "请看 @wa");
    await user.click(
      await screen.findByRole("option", { name: /wang@openfox\.dev/ }),
    );
    await user.type(screen.getByRole("textbox", { name: "评论..." }), "谢谢");
    await user.click(screen.getByRole("button", { name: "发送评论" }));
    await waitFor(() =>
      expect(server.find("POST", "/comments")).toHaveLength(1),
    );
    const body = server.find("POST", "/comments")[0]!.body as {
      body: string;
      client_comment_id: string;
      mentions: unknown;
    };
    expect(body.body).toBe("请看 @wang@openfox.dev 谢谢");
    expect(body.mentions).toEqual([{ user_id: "u2" }]);
    expect(body.client_comment_id).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("评论框支持斜杠菜单和任务空间内 Agent 提及", async () => {
    const user = userEvent.setup();
    const server = renderAt(`${spaceTasks}?task=OPEN-1`, (call) =>
      call.path === `/v1/organizations/${ORG}/agent-directory`
        ? json(200, {
            entries: [entry("a1", { name: "研发助手", space_id: SPACE })],
            archived_count: 0,
            truncated: false,
          })
        : undefined,
    );
    const box = await screen.findByRole("textbox", { name: "评论..." });
    await user.type(box, "/待办");
    const slash = await screen.findByRole("listbox", { name: "斜杠菜单" });
    await user.click(within(slash).getByRole("option", { name: "待办列表" }));
    expect(box).toHaveValue("- [ ] ");

    await user.type(box, "@研发");
    await user.click(await screen.findByRole("option", { name: /研发助手/ }));
    await user.click(screen.getByRole("button", { name: "发送评论" }));
    await waitFor(() =>
      expect(server.find("POST", "/comments")).toHaveLength(1),
    );
    expect(server.find("POST", "/comments")[0]!.body).toMatchObject({
      body: "- [ ] @研发助手",
      mentions: [{ agent_id: "a1" }],
    });
  });
});

describe("我的任务", () => {
  it("切换范围后请求带 scope", async () => {
    const user = userEvent.setup();
    const server = renderAt(`/o/${ORG}/my-tasks`);
    await screen.findByRole("region", { name: "待处理 2" });
    expect(
      server.calls.some(
        (c) =>
          c.path.endsWith("/my-tasks/counts") && c.query.get("scope") === "all",
      ),
    ).toBe(true);
    await user.click(screen.getByRole("radio", { name: "我创建的" }));
    await waitFor(() =>
      expect(
        server.calls.some(
          (c) =>
            c.path.endsWith("/my-tasks") && c.query.get("scope") === "created",
        ),
      ).toBe(true),
    );
  });

  it("?label= 预置标签筛选", async () => {
    const server = renderAt(`/o/${ORG}/my-tasks?label=l9`);
    await screen.findByRole("region", { name: "待处理 2" });
    expect(
      server.calls.some(
        (c) =>
          c.path.endsWith("/my-tasks/counts") &&
          c.query.getAll("label_id").includes("l9"),
      ),
    ).toBe(true);
  });
});
