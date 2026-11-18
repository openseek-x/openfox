import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createBrowserRouter,
  RouterProvider,
  MemoryRouter,
} from "react-router-dom";
import { createApi, HttpClient } from "@openfox/api";

import { App, createQueryClient } from "../App";
import { backend as taskBackend } from "../tasks/test-backend";

import { apiError, json, renderSettings } from "../settings/testing";
import { ORG, chatBase, summary, message } from "../conversations/fixtures";

// 阶段 12g · 分享快照：创建对话框、管理列表、公开查看页。

afterEach(() => {
  localStorage.clear();
  window.history.replaceState(null, "", "/");
});

const SHARE = `${ORG}/share-snapshots`;
const TS = "2026-11-12T08:00:00Z";

const meta = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  kind: "CONVERSATION",
  scope: "ORGANIZATION",
  title: `快照 ${id}`,
  item_count: 2,
  created_by: "u1",
  creator_name: "林",
  created_at: TS,
  expires_at: null,
  revoked_at: null,
  status: "ACTIVE",
  ...extra,
});

const settingsBody = (allow: boolean) => ({
  id: "o1",
  name: "林的团队",
  slug: "lin",
  description: "",
  icon_kind: null,
  icon: null,
  icon_url: null,
  default_locale: "zh-CN",
  priority_levels: 5,
  allow_public_sharing: allow,
  version: 3,
  my_role: "OWNER",
  can_manage: true,
  can_delete: true,
  leave_blocked: null,
});

function openChat(
  allowPublic: boolean,
  route: Parameters<typeof renderSettings>[1] = () => undefined,
  role: "OWNER" | "MEMBER" = "OWNER",
) {
  return renderSettings(
    "/o/o1/chat/c1",
    chatBase((call) => {
      const custom = route(call);
      if (custom) return custom;
      if (call.method === "GET" && call.path === `${ORG}/settings`)
        return json(200, settingsBody(allowPublic));
      if (call.path === `${ORG}/conversations/c1`)
        return json(200, summary("c1"));
      if (call.path === `${ORG}/conversations/c1/messages`)
        return json(200, { messages: [], next_cursor: 0 });
      return undefined;
    }),
    role,
  );
}

async function openDialog(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "更多操作" }));
  await user.click(await screen.findByRole("menuitem", { name: "分享快照…" }));
  return screen.findByRole("dialog");
}

describe("分享对话框", () => {
  it("默认仅组织、30 天；创建后显示可恢复链接，令牌在 URL 片段里", async () => {
    const user = userEvent.setup();
    const calls = openChat(true, (call) =>
      call.method === "POST" && call.path === SHARE
        ? json(201, { ...meta("n1"), token: "tok-abc_123" })
        : undefined,
    );
    const dialog = await openDialog(user);
    await user.click(within(dialog).getByRole("button", { name: "创建链接" }));
    const input = await screen.findByLabelText("分享链接");
    expect((input as HTMLInputElement).value).toBe(
      `${window.location.origin}/share#tok-abc_123`,
    );
    const post = calls.find(
      (call) => call.method === "POST" && call.path === SHARE,
    )!;
    expect(post.body).toMatchObject({
      kind: "CONVERSATION",
      scope: "ORGANIZATION",
      conversation_id: "c1",
      expiry: "30d",
    });
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    expect(screen.getByText(/可在分享管理中恢复/)).toBeInTheDocument();
  });

  it("组织未开启公开分享时，选公开会禁用创建并说明原因", async () => {
    const user = userEvent.setup();
    const calls = openChat(false);
    const dialog = await openDialog(user);
    await user.click(within(dialog).getByRole("radio", { name: /任何人/ }));
    expect(within(dialog).getByText(/未开启公开分享/)).toBeInTheDocument();
    const create = within(dialog).getByRole("button", { name: "创建链接" });
    expect(create).toBeDisabled();
    expect(
      calls.some((call) => call.method === "POST" && call.path === SHARE),
    ).toBe(false);
  });

  it("脱敏命中：阻止创建并列出位置，不显示命中原文", async () => {
    const user = userEvent.setup();
    openChat(true, (call) =>
      call.method === "POST" && call.path === SHARE
        ? json(422, {
            error: {
              code: "SENSITIVE_CONTENT_DETECTED",
              message: "x",
              request_id: "r",
              details: {
                findings: [
                  { rule: "github_token", item: 2, line: 3, column: 5 },
                  { rule: "private_key_block", item: 0, line: 1, column: 1 },
                ],
              },
            },
          })
        : undefined,
    );
    const dialog = await openDialog(user);
    await user.click(within(dialog).getByRole("button", { name: "创建链接" }));
    expect(await screen.findByText(/已阻止创建/)).toBeInTheDocument();
    expect(
      screen.getByText("第 2 条，第 3 行第 5 列：GitHub 令牌"),
    ).toBeInTheDocument();
    expect(screen.getByText("标题，第 1 行第 1 列：私钥")).toBeInTheDocument();
    expect(screen.queryByLabelText("分享链接")).toBeNull();
  });

  it("服务端返回 403 公开分享关闭时显示对应提示", async () => {
    const user = userEvent.setup();
    openChat(true, (call) =>
      call.method === "POST" && call.path === SHARE
        ? apiError(403, "PUBLIC_SHARING_DISABLED")
        : undefined,
    );
    const dialog = await openDialog(user);
    await user.click(within(dialog).getByRole("radio", { name: /任何人/ }));
    await user.click(within(dialog).getByRole("button", { name: "创建链接" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "本组织未开启公开分享",
    );
  });
});

describe("分享管理列表", () => {
  it("显示状态，只对有效项提供撤销；确认后调用撤销并刷新", async () => {
    const user = userEvent.setup();
    let revoked = false;
    const calls = openChat(true, (call) => {
      if (call.method === "GET" && call.path === SHARE)
        return json(200, {
          share_snapshots: [
            meta("a", { status: revoked ? "REVOKED" : "ACTIVE" }),
            meta("b", { status: "EXPIRED", scope: "PUBLIC" }),
          ],
        });
      if (call.method === "DELETE" && call.path === `${SHARE}/a`) {
        revoked = true;
        return json(200, meta("a", { status: "REVOKED" }));
      }
      return undefined;
    });
    const dialog = await openDialog(user);
    await user.click(
      within(dialog).getByRole("button", { name: "管理分享链接" }),
    );
    const list = await screen.findByRole("dialog", { name: "分享链接" });
    expect(await within(list).findByText("快照 a")).toBeInTheDocument();
    expect(within(list).getByText("已过期")).toBeInTheDocument();
    expect(
      within(list).queryByRole("button", { name: "撤销 快照 b" }),
    ).toBeNull();
    await user.click(within(list).getByRole("button", { name: "撤销 快照 a" }));
    await user.click(
      within(
        await screen.findByRole("dialog", { name: "撤销这个分享链接？" }),
      ).getByRole("button", {
        name: "撤销",
      }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (call) => call.method === "DELETE" && call.path === `${SHARE}/a`,
        ),
      ).toBe(true),
    );
    expect(await within(list).findByText("已撤销")).toBeInTheDocument();
  });
});

describe("公开查看页", () => {
  const view = {
    id: "s1",
    kind: "CONVERSATION",
    scope: "PUBLIC",
    title: "发布评审",
    items: [
      {
        author: "林",
        author_kind: "USER",
        body: "<script>alert(1)</script> 看 https://example.com/a 和 javascript:alert(1)",
        at: TS,
      },
      {
        author: "熊猫",
        author_kind: "AGENT",
        body: "收到",
        at: TS,
        reply_to: 1,
      },
    ],
    creator_name: "林",
    organization_name: "林的团队",
    created_at: TS,
    expires_at: null,
  };

  it("匿名可读：令牌放请求体，正文按文本渲染，外链带 nofollow，不请求登录态接口", async () => {
    window.history.replaceState(null, "", "/share#tok-xyz");
    const calls = renderSettings("/share#tok-xyz", (call) =>
      call.method === "POST" && call.path === "/v1/share-snapshots/view"
        ? json(200, view)
        : undefined,
    );
    expect(await screen.findByText("发布评审")).toBeInTheDocument();
    const post = calls.find(
      (call) => call.path === "/v1/share-snapshots/view",
    )!;
    expect(post.body).toEqual({ token: "tok-xyz" });
    expect(post.path).not.toContain("tok-xyz");
    // 脚本标签是文本，不是元素
    expect(document.querySelector("main script")).toBeNull();
    expect(
      screen.getByText(/<script>alert\(1\)<\/script>/),
    ).toBeInTheDocument();
    const link = screen.getByRole("link", { name: "https://example.com/a" });
    expect(link).toHaveAttribute("rel", "noopener noreferrer nofollow");
    expect(link).toHaveAttribute("target", "_blank");
    expect(screen.queryByRole("link", { name: /javascript:/ })).toBeNull();
    expect(screen.getByText("Agent")).toBeInTheDocument();
    expect(screen.getByText("回复第 1 条")).toBeInTheDocument();
    // 不依赖登录态
    expect(calls.some((call) => call.path === "/v1/me")).toBe(false);
  });

  it("404 统一显示不可用，并给出登录入口", async () => {
    window.history.replaceState(null, "", "/share#gone");
    renderSettings("/share#gone", (call) =>
      call.path === "/v1/share-snapshots/view"
        ? apiError(404, "NOT_FOUND")
        : undefined,
    );
    expect(await screen.findByText("该内容不可用")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "登录后查看" }),
    ).toBeInTheDocument();
  });

  function browserShare(
    token: string,
    response: (token: string) => Promise<Response>,
  ) {
    window.history.replaceState(null, "", `/share#${token}`);
    const requested: string[] = [];
    const transport = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) => {
        const { token } = JSON.parse(String(init?.body)) as { token: string };
        requested.push(token);
        return response(token);
      },
    );
    const queryClient = createQueryClient();
    const router = createBrowserRouter([
      {
        path: "*",
        element: (
          <App
            api={createApi(new HttpClient("/api", transport))}
            queryClient={queryClient}
            locale="zh-CN"
          />
        ),
      },
    ]);
    const mounted = render(<RouterProvider router={router} />);
    return {
      requested,
      router,
      queryClient,
      dispose: () => {
        mounted.unmount();
        router.dispose();
        queryClient.clear();
      },
    };
  }

  it("同一浏览器路由从失效片段切到新令牌会重新读取，清空片段隐藏旧正文", async () => {
    const app = browserShare("gone", async (token) =>
      token === "gone"
        ? apiError(404, "NOT_FOUND")
        : json(200, { ...view, title: "第二条快照" }),
    );
    try {
      await screen.findByText("该内容不可用");
      // Native fragment navigation keeps the same route element mounted.
      await act(async () => {
        window.location.hash = "#new-token";
      });
      expect(await screen.findByText("第二条快照")).toBeInTheDocument();
      expect(app.requested).toEqual(["gone", "new-token"]);
      await act(async () => {
        await app.router.navigate("/share");
      });
      expect(await screen.findByText("该内容不可用")).toBeInTheDocument();
      expect(screen.queryByText("第二条快照")).toBeNull();
      expect(app.requested).toEqual(["gone", "new-token"]);
    } finally {
      app.dispose();
    }
  });

  it("切换令牌后前一个请求晚返回不能覆盖新快照", async () => {
    let finishOld!: (response: Response) => void;
    const old = new Promise<Response>((resolve) => {
      finishOld = resolve;
    });
    const app = browserShare("slow-token", async (token) =>
      token === "slow-token" ? old : json(200, { ...view, title: "当前快照" }),
    );
    try {
      await waitFor(() => expect(app.requested).toEqual(["slow-token"]));
      await act(async () => {
        await app.router.navigate("/share#current-token");
      });
      expect(await screen.findByText("当前快照")).toBeInTheDocument();
      expect(
        app.queryClient.isFetching({ queryKey: ["share-view", "slow-token"] }),
      ).toBe(1);
      await act(async () => {
        finishOld(json(200, { ...view, title: "迟到旧快照" }));
        await old;
      });
      await waitFor(() =>
        expect(
          app.queryClient.isFetching({
            queryKey: ["share-view", "slow-token"],
          }),
        ).toBe(0),
      );
      expect(screen.getByText("当前快照")).toBeInTheDocument();
      expect(screen.queryByText("迟到旧快照")).toBeNull();
      expect(app.requested).toEqual(["slow-token", "current-token"]);
    } finally {
      app.dispose();
    }
  });

  it("没有令牌时不发请求，直接显示不可用", async () => {
    window.history.replaceState(null, "", "/share");
    const calls = renderSettings("/share", () => undefined);
    expect(await screen.findByText("该内容不可用")).toBeInTheDocument();
    expect(calls.some((call) => call.path === "/v1/share-snapshots/view")).toBe(
      false,
    );
  });
});

describe("组织设置里的公开分享开关", () => {
  it("切换开关提交 allow_public_sharing", async () => {
    const user = userEvent.setup();
    const calls = renderSettings("/o/o1/settings/organization", (call) => {
      if (call.path === `${ORG}/settings` && call.method === "GET")
        return json(200, settingsBody(false));
      if (call.path === `${ORG}/settings` && call.method === "PATCH")
        return json(200, { ...settingsBody(true), version: 4 });
      return undefined;
    });
    await user.click(
      await screen.findByRole("switch", { name: "允许公开分享" }),
    );
    await waitFor(() =>
      expect(
        calls.find(
          (call) => call.method === "PATCH" && call.path === `${ORG}/settings`,
        )?.body,
      ).toEqual({ allow_public_sharing: true, version: 3 }),
    );
  });
});

describe("单条消息与链接恢复", () => {
  it("用户和 Agent 消息均可单独分享，取消不创建；网络重试保持键，改参数换键", async () => {
    const user = userEvent.setup();
    let attempts = 0;
    const calls = openChat(true, (call) => {
      if (call.path === `${ORG}/conversations/c1/messages`)
        return json(200, {
          messages: [message(1), message(2)],
          next_cursor: 0,
        });
      if (call.method === "POST" && call.path === SHARE) {
        attempts++;
        return attempts < 3
          ? apiError(503, "UNAVAILABLE")
          : json(201, { ...meta("one"), token: "one-token" });
      }
      return undefined;
    });
    const actions = await screen.findAllByRole("button", {
      name: "分享这条消息",
    });
    expect(actions).toHaveLength(2);
    await user.click(actions[0]!);
    let dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/不含引用消息/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    expect(calls.some((c) => c.method === "POST" && c.path === SHARE)).toBe(
      false,
    );
    await user.click(actions[1]!);
    dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "创建链接" }));
    await screen.findByRole("alert");
    await user.click(within(dialog).getByRole("button", { name: "创建链接" }));
    await waitFor(() => expect(attempts).toBe(2));
    await user.type(within(dialog).getByLabelText("标题（可选）"), "new title");
    await user.click(within(dialog).getByRole("button", { name: "创建链接" }));
    await screen.findByLabelText("分享链接");
    const posts = calls.filter((c) => c.method === "POST" && c.path === SHARE);
    expect(posts[0]!.body).toMatchObject({
      message_id: "m2",
      conversation_id: "c1",
    });
    expect(posts[0]!.headers.get("Idempotency-Key")).toBe(
      posts[1]!.headers.get("Idempotency-Key"),
    );
    expect(posts[2]!.headers.get("Idempotency-Key")).not.toBe(
      posts[0]!.headers.get("Idempotency-Key"),
    );
  });
  it("管理列表只恢复创建者链接并展示可手动复制的地址", async () => {
    const user = userEvent.setup();
    const calls = openChat(true, (call) => {
      if (call.path === SHARE)
        return json(200, {
          share_snapshots: [
            meta("own"),
            meta("other", { created_by: "u2" }),
            meta("expired", { status: "EXPIRED" }),
          ],
        });
      if (call.path === `${SHARE}/own/link`)
        return json(200, { ...meta("own"), token: "recover-token" });
      return undefined;
    });
    const dialog = await openDialog(user);
    await user.click(
      within(dialog).getByRole("button", { name: "管理分享链接" }),
    );
    const list = await screen.findByRole("dialog", { name: "分享链接" });
    const copies = await within(list).findAllByRole("button", {
      name: "复制链接",
    });
    expect(copies).toHaveLength(1);
    await user.click(copies[0]!);
    expect(
      await screen.findByRole("textbox", { name: "分享链接" }),
    ).toHaveValue(`${window.location.origin}/share#recover-token`);
    expect(calls.filter((c) => c.path.endsWith("/link"))).toHaveLength(1);
  });
});

describe("既有任务评论分享与可恢复链接整合", () => {
  it("保留 COMMENT 来源，网络失败重试复用同一幂等键并返回可恢复链接", async () => {
    const user = userEvent.setup();
    let attempts = 0;
    const server = taskBackend((call) => {
      if (call.path === `${ORG}/settings`) return json(200, settingsBody(true));
      if (call.path.endsWith("/timeline"))
        return json(200, {
          items: [
            {
              type: "comment",
              comment: {
                organization_id: "o1",
                space_id: "s1",
                task_id: "task-1",
                id: "comment-1",
                author_kind: "USER",
                author_user_id: "u1",
                author_name: "林",
                body: "可分享的评论线程",
                reactions: [],
                mentions: [],
                created_at: TS,
              },
            },
          ],
          next_cursor: null,
        });
      if (call.path === SHARE && call.method === "POST") {
        attempts++;
        return attempts === 1
          ? apiError(503, "UNAVAILABLE")
          : json(201, {
              ...meta("comment-share", { kind: "COMMENT" }),
              token: "comment-token",
            });
      }
      return undefined;
    });
    localStorage.setItem("openfox:onboarding:o1", "{}");
    const queryClient = createQueryClient();
    render(
      <MemoryRouter initialEntries={["/o/o1/spaces/s1/tasks?task=OPEN-1"]}>
        <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      </MemoryRouter>,
    );
    await screen.findByText("可分享的评论线程");
    await user.click(screen.getByRole("button", { name: "评论操作" }));
    await user.click(
      await screen.findByRole("menuitem", { name: "分享快照…" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "分享快照" });
    await user.click(within(dialog).getByRole("button", { name: "创建链接" }));
    await within(dialog).findByRole("alert");
    await user.click(within(dialog).getByRole("button", { name: "创建链接" }));
    expect(await within(dialog).findByLabelText("分享链接")).toHaveValue(
      `${window.location.origin}/share#comment-token`,
    );
    const posts = server.find("POST", "/share-snapshots");
    expect(posts).toHaveLength(2);
    expect(posts[0]!.body).toEqual({
      kind: "COMMENT",
      scope: "ORGANIZATION",
      expiry: "30d",
      space_id: "s1",
      task_id: "task-1",
      comment_id: "comment-1",
    });
    expect(posts[0]!.headers.get("Idempotency-Key")).toBeTruthy();
    expect(posts[1]!.headers.get("Idempotency-Key")).toBe(
      posts[0]!.headers.get("Idempotency-Key"),
    );
    expect(within(dialog).getByText(/可在分享管理中恢复/)).toBeVisible();
  });
});

it("限组织快照登录入口不把令牌放入URL，验证后仍返回原快照", async () => {
  const user = userEvent.setup();
  let authenticated = false;
  const token = "private-return-token";
  const calls = renderSettings(`/share#${token}`, (call) => {
    if (call.path === "/v1/share-snapshots/view")
      return authenticated
        ? json(200, {
            title: "登录后的快照",
            creator_name: "林",
            organization_name: "团队",
            created_at: TS,
            expires_at: null,
            scope: "ORGANIZATION",
            items: [],
          })
        : apiError(404, "NOT_FOUND");
    if (call.path === "/v1/auth/email/request")
      return json(202, { expires_at: TS });
    if (call.path === "/v1/auth/email/verify") {
      authenticated = true;
      return json(201, { user_id: "u1", expires_at: TS, new_user: false });
    }
    return undefined;
  });
  const signIn = await screen.findByRole("link", { name: "登录后查看" });
  expect(signIn).toHaveAttribute("href", "/login");
  await user.click(signIn);
  await user.type(
    await screen.findByLabelText("邮箱"),
    "share-test@example.test",
  );
  await user.click(screen.getByRole("button", { name: "继续" }));
  await user.type(await screen.findByLabelText("第 1 位"), "123456");
  expect(
    await screen.findByRole("heading", { name: "登录后的快照" }),
  ).toBeVisible();
  expect(
    calls
      .filter((call) => call.path === "/v1/share-snapshots/view")
      .map((call) => call.body),
  ).toEqual([{ token }, { token }]);
  expect(
    calls.every((call) => !`${call.path}?${call.query}`.includes(token)),
  ).toBe(true);
});
