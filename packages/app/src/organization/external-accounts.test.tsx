import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import {
  backend,
  json,
  ORG,
  type Call,
  type Override,
} from "../tasks/test-backend";

// 成员页「外部账号」页签。

const o = `/v1/organizations/${ORG}`;
const TS = "2026-10-20T08:00:00Z";
const page = `/o/${ORG}/settings/members?tab=external`;

const account = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  provider: "jira",
  external_user_id: `acc-${id}`,
  display_name: `用户${id}`,
  email: `${id}@jira.example.com`,
  source_label: "acme.atlassian.net",
  linked_user: null,
  link_method: "",
  suggested_user_id: null,
  pending_invitation: false,
  task_count: 2,
  created_at: TS,
  version: 1,
  ...extra,
});

const listBody = (
  accounts: unknown[],
  extra: Record<string, unknown> = {},
) => ({
  accounts,
  next_cursor: "",
  total: accounts.length,
  unlinked: accounts.length,
  providers: ["jira"],
  sources: ["acme.atlassian.net", "Jira CSV"],
  can_manage: true,
  ...extra,
});

const member = (id: string, email: string, role = "MEMBER") => ({
  user_id: id,
  email,
  role,
  status: "ACTIVE",
  authorization_version: 1,
  created_at: TS,
  spaces: [],
});

function renderAt(external: Override) {
  const server = backend((call) => {
    const custom = external(call);
    if (custom) return custom;
    if (call.path === `${o}/settings` && call.method === "GET")
      return json(200, {
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
      });
    if (call.path === `${o}/members` && call.method === "GET")
      return json(200, {
        members: [
          member("u1", "lin@openfox.dev", "OWNER"),
          member("u2", "wang@openfox.dev"),
        ],
        next_cursor: "",
      });
    if (call.path === `${o}/invites`) return json(200, { invitations: [] });
    return undefined;
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[page]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

async function pickMember(
  user: ReturnType<typeof userEvent.setup>,
  name: string,
  option: string,
) {
  await user.click(screen.getByRole("button", { name }));
  await user.click(await screen.findByRole("option", { name: option }));
}

describe("外部账号页签", () => {
  it("空态沿用说明文案；加载失败显示重试", async () => {
    const user = userEvent.setup();
    let fail = true;
    renderAt((call) => {
      if (call.path === `${o}/external-accounts`) {
        if (fail) return json(500, { error: { code: "X", message: "x" } });
        return json(200, listBody([]));
      }
      return undefined;
    });
    expect(await screen.findByText("加载失败")).toBeVisible();
    fail = false;
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(
      await screen.findByText(/还没有外部账号。导入或同步任务管理平台/),
    ).toBeVisible();
  });

  it("列表：来源、关联状态、建议成员；筛选参数带到请求里", async () => {
    const user = userEvent.setup();
    const calls: Call[] = [];
    renderAt((call) => {
      if (call.path === `${o}/external-accounts`) {
        calls.push(call);
        return json(
          200,
          listBody(
            [
              account("a1", { suggested_user_id: "u2" }),
              account("a2", {
                linked_user: {
                  id: "u1",
                  email: "lin@openfox.dev",
                  name: "lin",
                },
                link_method: "email",
              }),
              account("a3", { pending_invitation: true }),
              account("a4", {
                linked_user: {
                  id: "u1",
                  email: "lin@openfox.dev",
                  name: "lin",
                },
                link_method: "verified",
              }),
            ],
            { total: 3, unlinked: 2 },
          ),
        );
      }
      return undefined;
    });
    const row = await screen.findByTestId("external-a1");
    expect(within(row).getByText("acme.atlassian.net")).toBeVisible();
    expect(within(row).getByText("可自动关联")).toBeVisible();
    expect(
      within(screen.getByTestId("external-a2")).getByText("邮箱匹配"),
    ).toBeVisible();
    expect(
      within(screen.getByTestId("external-a3")).getByText("已邀请"),
    ).toBeVisible();
    expect(
      within(screen.getByTestId("external-a4")).getByText("本人验证"),
    ).toBeVisible();
    expect(screen.getByText("共 3 个账号，2 个未关联")).toBeVisible();
    expect(calls[0]!.query.get("limit")).toBe("50");
    expect(calls[0]!.query.get("provider")).toBeNull();

    await user.click(screen.getByRole("button", { name: "按来源筛选" }));
    await user.click(await screen.findByRole("option", { name: "Jira CSV" }));
    await user.click(screen.getByRole("button", { name: "按关联状态筛选" }));
    await user.click(await screen.findByRole("option", { name: "未关联" }));
    await waitFor(() => {
      const last = calls.at(-1)!;
      expect(last.query.get("source")).toBe("Jira CSV");
      expect(last.query.get("link")).toBe("unlinked");
    });
  });

  it("建议成员默认突出显示但不自动保存；选择后出现保存条并批量提交", async () => {
    const user = userEvent.setup();
    const saves: Call[] = [];
    renderAt((call) => {
      if (call.path === `${o}/external-accounts` && call.method === "GET")
        return json(
          200,
          listBody([
            account("a1", { suggested_user_id: "u2", version: 4 }),
            account("a2"),
          ]),
        );
      if (call.path === `${o}/external-accounts/links`) {
        saves.push(call);
        return json(200, { accounts: [], reassigned_tasks: 5 });
      }
      return undefined;
    });
    await screen.findByTestId("external-a1");
    expect(
      screen.getByRole("button", { name: "用户a1 的内部成员" }),
    ).toHaveTextContent("建议：wang@openfox.dev");
    expect(screen.queryByText(/项未保存关联/)).toBeNull();

    await pickMember(user, "用户a1 的内部成员", "wang@openfox.dev");
    await pickMember(user, "用户a2 的内部成员", "lin@openfox.dev");
    expect(await screen.findByText("2 项未保存关联")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "保存更改" }));
    await waitFor(() => expect(saves).toHaveLength(1));
    expect(saves[0]!.body).toEqual({
      changes: [
        { id: "a1", version: 4, user_id: "u2" },
        { id: "a2", version: 1, user_id: "u1" },
      ],
    });
    expect(
      await screen.findByText("已保存，5 个任务已转给对应成员"),
    ).toBeVisible();
    await waitFor(() => expect(screen.queryByText(/项未保存关联/)).toBeNull());
  });

  it("取消关联：已关联账号的下拉最后一项是「取消关联」，提交 user_id 为 null", async () => {
    const user = userEvent.setup();
    const saves: Call[] = [];
    renderAt((call) => {
      if (call.path === `${o}/external-accounts` && call.method === "GET")
        return json(
          200,
          listBody([
            account("a1", {
              linked_user: { id: "u1", email: "lin@openfox.dev", name: "lin" },
              link_method: "manual",
            }),
          ]),
        );
      if (call.path === `${o}/external-accounts/links`) {
        saves.push(call);
        return json(200, { accounts: [], reassigned_tasks: 0 });
      }
      return undefined;
    });
    await screen.findByTestId("external-a1");
    await pickMember(user, "用户a1 的内部成员", "取消关联");
    await user.click(await screen.findByRole("button", { name: "保存更改" }));
    await waitFor(() => expect(saves).toHaveLength(1));
    expect(saves[0]!.body).toEqual({
      changes: [{ id: "a1", version: 1, user_id: null }],
    });
  });

  it("409 版本冲突：提示并刷新，没有冲突的改动保留", async () => {
    const user = userEvent.setup();
    let gets = 0;
    renderAt((call) => {
      if (call.path === `${o}/external-accounts` && call.method === "GET") {
        gets++;
        return json(200, listBody([account("a1"), account("a2")]));
      }
      if (call.path === `${o}/external-accounts/links`)
        return json(409, {
          error: {
            code: "VERSION_CONFLICT",
            message: "x",
            request_id: "r",
            details: { ids: ["a1"] },
          },
        });
      return undefined;
    });
    await screen.findByTestId("external-a1");
    await pickMember(user, "用户a1 的内部成员", "wang@openfox.dev");
    await pickMember(user, "用户a2 的内部成员", "lin@openfox.dev");
    await user.click(await screen.findByRole("button", { name: "保存更改" }));
    expect(
      await screen.findByText("部分账号已被他人修改，已刷新"),
    ).toBeVisible();
    await waitFor(() => expect(gets).toBeGreaterThan(1));
    expect(await screen.findByText("1 项未保存关联")).toBeVisible();
    expect(
      screen.getByRole("button", { name: "用户a2 的内部成员" }),
    ).toHaveTextContent("lin@openfox.dev");
  });

  it("有未保存改动时切换筛选或离开页签要先确认", async () => {
    const user = userEvent.setup();
    const calls: Call[] = [];
    renderAt((call) => {
      if (call.path === `${o}/external-accounts` && call.method === "GET") {
        calls.push(call);
        return json(200, listBody([account("a1")]));
      }
      return undefined;
    });
    await screen.findByTestId("external-a1");
    await pickMember(user, "用户a1 的内部成员", "wang@openfox.dev");
    await user.click(screen.getByRole("button", { name: "按关联状态筛选" }));
    await user.click(await screen.findByRole("option", { name: "已关联" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("放弃未保存的更改？")).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "继续编辑" }));
    expect(screen.getByText("1 项未保存关联")).toBeVisible();
    expect(calls.every((c) => c.query.get("link") === null)).toBe(true);

    await user.click(screen.getByRole("tab", { name: /成员/ }));
    const leave = await screen.findByRole("dialog");
    await user.click(within(leave).getByRole("button", { name: "放弃更改" }));
    await waitFor(() => expect(screen.queryByTestId("external-a1")).toBeNull());
  });

  it("非管理员：内部成员列只读，没有保存条", async () => {
    renderAt((call) => {
      if (call.path === `${o}/external-accounts` && call.method === "GET")
        return json(
          200,
          listBody(
            [
              account("a1", {
                linked_user: { id: "u1", email: "lin@openfox.dev", name: "林" },
                link_method: "manual",
              }),
              account("a2"),
            ],
            { can_manage: false },
          ),
        );
      return undefined;
    });
    const row = await screen.findByTestId("external-a1");
    expect(within(row).getByText("林")).toBeVisible();
    expect(
      screen.queryByRole("button", { name: "用户a1 的内部成员" }),
    ).toBeNull();
    expect(
      within(screen.getByTestId("external-a2")).getByText("—"),
    ).toBeVisible();
  });
});
