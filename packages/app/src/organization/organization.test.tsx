import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App, createQueryClient } from "../App";
import { backend, json, ORG, type Override } from "../tasks/test-backend";

// 组织设置、成员页、邀请链接、加入页、优先级 4/5 级。

const o = `/v1/organizations/${ORG}`;

const settings = (extra: Record<string, unknown> = {}) => ({
  id: ORG,
  name: "林的团队",
  slug: "lin-team",
  description: "",
  icon_kind: null,
  icon: null,
  icon_url: null,
  default_locale: "zh-CN",
  priority_levels: 5,
  version: 3,
  my_role: "OWNER",
  can_manage: true,
  can_delete: true,
  leave_blocked: null,
  ...extra,
});

const member = (
  id: string,
  email: string,
  role: string,
  created: string,
  spaces: object[] = [],
) => ({
  user_id: id,
  email,
  role,
  status: "ACTIVE",
  authorization_version: 1,
  created_at: created,
  spaces,
});

const error = (status: number, code: string) =>
  json(status, { error: { code, message: code, request_id: "r" } });

function renderAt(
  path: string,
  opts: { settings?: Record<string, unknown>; override?: Override } = {},
) {
  const current = { value: settings(opts.settings) };
  const server = backend((call) => {
    const custom = opts.override?.(call);
    if (custom) return custom;
    if (call.path === `${o}/settings` && call.method === "GET")
      return json(200, current.value);
    if (call.path === `${o}/settings` && call.method === "PATCH") {
      const body = call.body as Record<string, unknown>;
      current.value = {
        ...current.value,
        ...body,
        version: current.value.version + 1,
      };
      return json(200, current.value);
    }
    if (call.path === `${o}/settings/lowest-priority-count`)
      return json(200, { count: 12 });
    if (call.path === `${o}/members` && call.method === "GET")
      return json(200, {
        members: [
          member("u2", "wang@openfox.dev", "MEMBER", "2026-10-25T00:00:00Z", [
            {
              id: "s1",
              name: "默认空间",
              prefix: "OPEN",
              icon: null,
              visibility: "shared",
            },
          ]),
          member("u1", "lin@openfox.dev", "ADMIN", "2026-10-22T00:00:00Z"),
          member("u3", "zhou@openfox.dev", "OWNER", "2026-10-23T00:00:00Z"),
        ],
        next_cursor: "",
      });
    if (call.path.startsWith(`${o}/members/`))
      return json(200, member("u2", "wang@openfox.dev", "GUEST", "x"));
    if (call.path === `${o}/invites` && call.method === "GET")
      return json(200, {
        invitations: [
          {
            id: "i1",
            organization_id: ORG,
            email: "alex@openfox.dev",
            role: "MEMBER",
            status: "PENDING",
            issued_at: "2026-10-29T00:00:00Z",
            expires_at: "2026-11-05T00:00:00Z",
            space_ids: [],
            delivery_status: "SENT",
            last_sent_at: "2026-10-29T00:00:00Z",
          },
          {
            id: "i2",
            organization_id: ORG,
            email: "ops@openfox.dev",
            role: "GUEST",
            status: "PENDING",
            issued_at: "2026-10-29T00:00:00Z",
            expires_at: "2026-11-05T00:00:00Z",
            space_ids: [],
            delivery_status: "FAILED",
            last_sent_at: "2026-10-29T00:00:00Z",
          },
        ],
      });
    if (/\/invites\/i\d\/(resend|revoke)$/.test(call.path))
      return json(200, { id: "i1", delivery_status: "SENT" });
    if (call.method === "DELETE" && call.path === o)
      return new Response(null, { status: 204 });
    if (call.path === `${o}/leave`) return new Response(null, { status: 204 });
    return undefined;
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

const page = `/o/${ORG}/settings/organization`;
const membersPage = `/o/${ORG}/settings/members`;

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("组织设置", () => {
  it("名称失焦保存（带 version），描述在弹窗里编辑", async () => {
    const user = userEvent.setup();
    const server = renderAt(page);
    const name = await screen.findByRole("textbox", { name: "组织名称" });
    await user.clear(name);
    await user.type(name, "林的研发团队");
    await user.tab();
    await waitFor(() =>
      expect(server.find("PATCH", "/settings")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/settings")[0]!.body).toEqual({
      name: "林的研发团队",
      version: 3,
    });
    expect(await screen.findByText("已保存组织设置")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "编辑描述" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByRole("textbox"), "做研发");
    await user.click(within(dialog).getByRole("button", { name: "保存更改" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/settings")[1]?.body).toEqual({
        description: "做研发",
        version: 4,
      }),
    );
  });

  it("slug 格式不对时提示且不提交；合法时提交；被占用时提示换一个", async () => {
    const user = userEvent.setup();
    const server = renderAt(page, {
      override: (call) =>
        call.method === "PATCH" &&
        (call.body as { slug?: string }).slug === "taken-one"
          ? error(409, "SLUG_TAKEN")
          : undefined,
    });
    const slug = await screen.findByRole("textbox", { name: "组织 URL" });
    await user.clear(slug);
    await user.type(slug, "Lin_Team");
    expect(
      screen.getByText("只能包含小写字母、数字和连字符"),
    ).toBeInTheDocument();
    await user.tab();
    expect(server.find("PATCH", "/settings")).toHaveLength(0);
    await user.clear(slug);
    await user.type(slug, "taken-one");
    await user.tab();
    expect(await screen.findByText("请换一个组织 URL")).toBeInTheDocument();
  });

  it("5 → 4 级：有「最低」任务时先确认，再提交", async () => {
    const user = userEvent.setup();
    const server = renderAt(page);
    await user.click(
      await screen.findByRole("button", { name: "任务优先级级别" }),
    );
    await user.click(await screen.findByRole("option", { name: "4 级" }));
    expect(
      await screen.findByText(/当前有 12 个“最低”优先级的任务将被改为“低”/),
    ).toBeInTheDocument();
    expect(server.find("PATCH", "/settings")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/settings")[0]?.body).toEqual({
        priority_levels: 4,
        version: 3,
      }),
    );
  });

  it("成员视角只显示离开组织；唯一所有者不能离开", async () => {
    renderAt(page, {
      settings: { can_manage: false, can_delete: false, my_role: "MEMBER" },
    });
    expect(
      await screen.findByText("从该组织中移除你自己。"),
    ).toBeInTheDocument();
    expect(screen.queryByText("图标和名称")).not.toBeInTheDocument();
    expect(screen.queryByText("删除组织")).not.toBeInTheDocument();
  });

  it("唯一所有者的离开按钮禁用并说明原因", async () => {
    renderAt(page, { settings: { leave_blocked: "sole_owner" } });
    expect(
      await screen.findByText(/你是唯一的所有者。请先把另一位成员升级为所有者/),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "离开组织" })).toBeDisabled();
  });

  it("删除组织：输入必须与组织名完全一致", async () => {
    const user = userEvent.setup();
    const server = renderAt(page);
    await user.click(await screen.findByRole("button", { name: "删除组织" }));
    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "删除组织" });
    const input = within(dialog).getByRole("textbox");
    await user.type(input, "林的团队 ");
    expect(submit).toBeDisabled();
    await user.clear(input);
    await user.type(input, "林的团队{Enter}");
    await waitFor(() =>
      expect(server.find("DELETE", o)[0]?.body).toEqual({
        confirm_name: "林的团队",
      }),
    );
  });
});

describe("成员页", () => {
  it("所有者在前、其次是自己；空间浮层；待接受邀请显示发送状态", async () => {
    const user = userEvent.setup();
    renderAt(membersPage);
    await screen.findByText("zhou@openfox.dev");
    const table = screen.getByRole("table", { name: "成员" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(
      rows.map((row) => within(row).getAllByRole("cell")[0]!.textContent),
    ).toEqual([
      expect.stringContaining("zhou@openfox.dev"),
      expect.stringContaining("lin@openfox.dev"),
      expect.stringContaining("wang@openfox.dev"),
    ]);
    await user.click(
      within(rows[2]!).getByRole("button", { name: "1 个空间" }),
    );
    expect(await screen.findByText("默认空间")).toBeInTheDocument();
    expect(screen.getByText("待接受邀请（2）")).toBeInTheDocument();
    expect(screen.getByText("发送失败，可重试")).toBeInTheDocument();
  });

  it("改角色、移除成员、撤销邀请", async () => {
    const user = userEvent.setup();
    const server = renderAt(membersPage);
    await screen.findByText("zhou@openfox.dev");
    const table = screen.getByRole("table", { name: "成员" });
    const wang = within(table)
      .getAllByRole("row")
      .find((row) => row.textContent?.includes("wang@"))!;
    await user.click(within(wang).getByRole("button", { name: "成员操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "更改角色" }));
    await user.click(
      await screen.findByRole("menuitemradio", { name: /访客/ }),
    );
    await waitFor(() =>
      expect(server.find("PATCH", "/members/u2")[0]?.body).toEqual({
        role: "GUEST",
      }),
    );
    expect(await screen.findByText("角色已更新")).toBeInTheDocument();

    await user.click(within(wang).getByRole("button", { name: "成员操作" }));
    await user.click(
      await screen.findByRole("menuitem", { name: "从组织中移除" }),
    );
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "确认",
      }),
    );
    await waitFor(() =>
      expect(server.find("PATCH", "/members/u2")[1]?.body).toEqual({
        status: "REVOKED",
      }),
    );

    await user.click(screen.getAllByRole("button", { name: "撤销邀请" })[0]!);
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "确认",
      }),
    );
    await waitFor(() =>
      expect(server.find("POST", "/invites/i1/revoke")).toHaveLength(1),
    );
  });

  it("管理员看不到其他管理员与所有者的改角色 / 移除，自己那行只剩管理空间", async () => {
    const user = userEvent.setup();
    renderAt(membersPage, {
      settings: { my_role: "ADMIN", can_delete: false },
      override: (call) =>
        call.path === "/v1/me"
          ? json(200, { id: "u1", email: "lin@openfox.dev" })
          : undefined,
    });
    await screen.findByText("zhou@openfox.dev");
    const table = screen.getByRole("table", { name: "成员" });
    const self = within(table)
      .getAllByRole("row")
      .find((row) => row.textContent?.includes("lin@"))!;
    await user.click(within(self).getByRole("button", { name: "成员操作" }));
    const menu = await screen.findByRole("menu");
    expect(
      within(menu)
        .getAllByRole("menuitem")
        .map((i) => i.textContent),
    ).toEqual(["管理空间…"]);
  });

  it("非管理员被重定向到个人资料", async () => {
    renderAt(membersPage, {
      settings: { can_manage: false, my_role: "MEMBER" },
    });
    // 个人资料页已实现，重定向后显示全名输入框
    expect(
      await screen.findByRole("textbox", { name: "全名" }),
    ).toBeInTheDocument();
  });
});

describe("邀请链接", () => {
  const link = (extra: Record<string, unknown> = {}) => ({
    enabled: false,
    role: "MEMBER",
    space_ids: [],
    token: null,
    version: 0,
    updated_at: null,
    ...extra,
  });

  it("复制时链接未启用会先启用；重置要确认；管理员角色不可选", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    const server = renderAt(membersPage, {
      override: (call) => {
        if (call.path === `${o}/invite-link` && call.method === "GET")
          return json(200, link());
        if (call.path === `${o}/invite-link` && call.method === "PUT")
          return json(
            200,
            link({ ...(call.body as object), token: "tok", version: 1 }),
          );
        if (call.path === `${o}/invite-link/reset`)
          return json(200, link({ enabled: true, token: "tok2", version: 2 }));
        return undefined;
      },
    });
    await user.click(await screen.findByRole("button", { name: "邀请" }));
    const dialog = await screen.findByRole("dialog", { name: "邀请" });
    await user.click(within(dialog).getByRole("tab", { name: "通过链接" }));
    await user.click(
      await within(dialog).findByRole("button", { name: "复制链接" }),
    );
    await waitFor(() => expect(writeText).toHaveBeenCalled());
    expect(server.find("PUT", "/invite-link")[0]!.body).toEqual({
      enabled: true,
      role: "MEMBER",
      space_ids: [],
      version: 0,
    });
    expect(writeText.mock.calls[0]![0]).toMatch(/\/invite\/tok$/);
    expect(await screen.findByText("邀请链接已复制")).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "重置链接" }));
    await user.click(
      within(
        await screen.findByRole("dialog", { name: "重置邀请链接" }),
      ).getByRole("button", { name: "重置链接" }),
    );
    await waitFor(() =>
      expect(server.find("POST", "/invite-link/reset")[0]?.body).toEqual({
        version: 1,
      }),
    );

    await user.click(within(dialog).getByRole("button", { name: /^角色/ }));
    expect(
      await screen.findByRole("option", { name: /管理员/ }),
    ).toHaveAttribute("aria-disabled", "true");
  });

  it("没有密钥配置时提示不可用", async () => {
    const user = userEvent.setup();
    renderAt(membersPage, {
      override: (call) =>
        call.path === `${o}/invite-link`
          ? error(503, "SECRETS_UNAVAILABLE")
          : undefined,
    });
    await user.click(await screen.findByRole("button", { name: "邀请" }));
    await user.click(screen.getByRole("tab", { name: "通过链接" }));
    expect(
      await screen.findByText("服务器未配置密钥，暂时无法使用邀请链接。"),
    ).toBeInTheDocument();
  });
});

describe("加入页与优先级", () => {
  it("凭链接预览并加入，成功后进入组织", async () => {
    const user = userEvent.setup();
    const server = renderAt("/invite/tok", {
      override: (call) => {
        if (call.path === "/v1/invite-links/preview")
          return json(200, {
            organization_id: ORG,
            name: "OpenSeek",
            icon_kind: null,
            icon: null,
            role: "GUEST",
            inviter_email: "inviter@example.com",
            member_count: 12,
            agent_count: 4,
            already_member: false,
          });
        if (call.path === "/v1/invite-links/join")
          return json(200, {
            organization_id: ORG,
            role: "GUEST",
            status: "JOINED",
          });
        return undefined;
      },
    });
    expect(
      await screen.findByRole("heading", { name: "加入 OpenSeek" }),
    ).toBeInTheDocument();
    expect(screen.getByText("12 位成员 · 4 个 Agent")).toBeInTheDocument();
    expect(screen.getByText("你将以访客身份加入。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "加入组织" }));
    await waitFor(() =>
      expect(server.find("POST", "/invite-links/join")[0]?.body).toEqual({
        token: "tok",
      }),
    );
  });

  it("无效链接显示提示", async () => {
    renderAt("/invite/bad", {
      override: (call) =>
        call.path === "/v1/invite-links/preview"
          ? error(404, "NOT_FOUND")
          : undefined,
    });
    expect(
      await screen.findByText("邀请链接无效或已停用。"),
    ).toBeInTheDocument();
  });

  it.each([
    [5, true],
    [4, false],
  ])("组织为 %i 级时，优先级候选里「最低」出现：%s", async (levels, shown) => {
    const user = userEvent.setup();
    renderAt(`/o/${ORG}/spaces/s1/tasks`, {
      settings: { priority_levels: levels },
    });
    await screen.findByText("默认空间");
    await user.keyboard("c");
    await user.click(await screen.findByRole("button", { name: /^优先级/ }));
    await screen.findByRole("option", { name: /紧急/ });
    expect(Boolean(screen.queryByRole("option", { name: /最低/ }))).toBe(shown);
  });
});
