import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { apiError, json, renderSettings, ts } from "../settings/testing";

const list = "/v1/organizations/o1/me/account-bindings";
const endpoint = `${list}/shortcut`;
const parent = "10000000-0000-0000-0000-000000000001";
const otherParent = "10000000-0000-0000-0000-000000000002";
const personal = "20000000-0000-0000-0000-000000000001";
const binding = (comments: boolean, version = 3) => ({
  connection_id: personal,
  display_name: "Lin",
  detail: "workspace-1",
  avatar_url: "",
  created_at: ts,
  version,
  shortcut_identity: {
    comments_enabled: comments,
    ...(comments ? { comments_authorized_at: ts } : {}),
  },
});
type Binding = ReturnType<typeof binding>;
const view = (
  current: Binding | null = null,
  multiple = false,
  ready = true,
) => ({
  email: "lin@openfox.dev",
  jira_sites: [],
  providers: [
    {
      id: "shortcut",
      name: "Shortcut",
      availability: "available",
      method: "token",
      organization_ready: ready,
      binding: current,
      instances: ready
        ? [
            {
              connection_id: parent,
              display_name: "Workspace One",
              host: "workspace-1",
              binding: current,
            },
            ...(multiple
              ? [
                  {
                    connection_id: otherParent,
                    display_name: "Workspace Two",
                    host: "workspace-2",
                    binding: null,
                  },
                ]
              : []),
          ]
        : [],
    },
  ],
});
const modal = () =>
  within(screen.getByRole("dialog", { name: "Shortcut 个人身份与评论授权" }));
const manage = async (user: ReturnType<typeof userEvent.setup>) => {
  await user.click(await screen.findByRole("button", { name: "已连接" }));
  await user.click(
    screen.getByRole("menuitem", { name: "管理 Workspace One 身份与评论" }),
  );
};

beforeEach(() => localStorage.clear());

describe("Shortcut identity and explicit new-comment grant", () => {
  it("defaults to identity only, retains a failed draft, and clears it after success", async () => {
    let current: Binding | null = null;
    let attempts = 0;
    const calls = renderSettings(
      "/o/o1/settings/account-binding",
      (call) => {
        if (call.path === list) return json(200, view(current));
        if (call.path === endpoint && call.method === "PUT") {
          if (++attempts === 1) return apiError(422, "SHORTCUT_EMAIL_MISMATCH");
          current = binding(false);
          return json(200, { id: personal, version: 3 });
        }
        return undefined;
      },
      "MEMBER",
    );
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "绑定" }));
    expect(modal().getByRole("checkbox")).not.toBeChecked();
    expect(calls.filter((c) => c.path === endpoint)).toHaveLength(0);
    await user.type(
      modal().getByLabelText("Shortcut V3 个人 Token"),
      "one-time-token",
    );
    await user.click(modal().getByRole("button", { name: "验证并保存" }));
    expect(await modal().findByRole("alert")).toHaveTextContent("邮箱");
    expect(modal().getByLabelText("Shortcut V3 个人 Token")).toHaveValue(
      "one-time-token",
    );
    expect(calls.find((c) => c.path === endpoint)?.body).toEqual({
      connection_id: parent,
      version: 0,
      token: "one-time-token",
      comments_enabled: false,
    });
    await user.click(modal().getByRole("button", { name: "验证并保存" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await manage(user);
    expect(modal().getByLabelText("Shortcut V3 个人 Token")).toHaveValue("");
    expect(modal().getByRole("checkbox")).not.toBeChecked();
    expect(JSON.stringify(localStorage)).not.toContain("one-time-token");
  });

  it("requires fresh credentials and explicit opt-in before enabling comments", async () => {
    const calls = renderSettings(
      "/o/o1/settings/account-binding",
      (call) => {
        if (call.path === list) return json(200, view(binding(false)));
        if (call.path === endpoint)
          return json(200, { id: personal, version: 4 });
        return undefined;
      },
      "MEMBER",
    );
    const user = userEvent.setup();
    await manage(user);
    await user.click(
      modal().getByRole("checkbox", { name: "允许以本人身份同步新评论" }),
    );
    expect(modal().getByText(/将加密保存你的个人 Token/)).toBeVisible();
    expect(modal().getByRole("button", { name: "验证并保存" })).toBeDisabled();
    expect(calls.filter((c) => c.path === endpoint)).toHaveLength(0);
    await user.type(
      modal().getByLabelText("Shortcut V3 个人 Token"),
      "comment-token",
    );
    await user.click(modal().getByRole("button", { name: "验证并保存" }));
    await waitFor(() =>
      expect(calls.find((c) => c.path === endpoint)?.body).toEqual({
        connection_id: parent,
        version: 3,
        token: "comment-token",
        comments_enabled: true,
      }),
    );
  });

  it("can disable an existing grant without resubmitting a token", async () => {
    const calls = renderSettings(
      "/o/o1/settings/account-binding",
      (call) => {
        if (call.path === list) return json(200, view(binding(true)));
        if (call.path === endpoint)
          return json(200, { id: personal, version: 4 });
        return undefined;
      },
      "MEMBER",
    );
    const user = userEvent.setup();
    await manage(user);
    expect(modal().getByRole("checkbox")).toBeChecked();
    await user.click(modal().getByRole("checkbox"));
    expect(modal().getByText(/关闭评论授权可留空/)).toBeVisible();
    await user.click(modal().getByRole("button", { name: "关闭评论授权" }));
    await waitFor(() =>
      expect(calls.find((c) => c.path === endpoint)?.body).toEqual({
        connection_id: parent,
        version: 3,
        token: "",
        comments_enabled: false,
      }),
    );
  });

  it("switching workspaces clears credentials and resets optional comment access", async () => {
    const calls = renderSettings(
      "/o/o1/settings/account-binding",
      (call) =>
        call.path === list ? json(200, view(binding(true), true)) : undefined,
      "MEMBER",
    );
    const user = userEvent.setup();
    await manage(user);
    await user.type(
      modal().getByLabelText("Shortcut V3 个人 Token"),
      "workspace-one-token",
    );
    await user.click(
      modal().getByRole("button", { name: "Shortcut workspace" }),
    );
    await user.click(
      screen.getByRole("option", { name: "Workspace Two · workspace-2" }),
    );
    expect(modal().getByLabelText("Shortcut V3 个人 Token")).toHaveValue("");
    expect(modal().getByRole("checkbox")).not.toBeChecked();
    expect(modal().getByRole("button", { name: "验证并保存" })).toBeDisabled();
    expect(calls.filter((c) => c.path === endpoint)).toHaveLength(0);
  });

  it("keeps the original version after conflict and sends no automatic retry", async () => {
    let version = 3;
    const calls = renderSettings(
      "/o/o1/settings/account-binding",
      (call) => {
        if (call.path === list) return json(200, view(binding(false, version)));
        if (call.path === endpoint) {
          version = 8;
          return apiError(409, "VERSION_CONFLICT");
        }
        return undefined;
      },
      "MEMBER",
    );
    const user = userEvent.setup();
    await manage(user);
    await user.type(
      modal().getByLabelText("Shortcut V3 个人 Token"),
      "fresh-token",
    );
    await user.click(modal().getByRole("button", { name: "验证并保存" }));
    await modal().findByRole("alert");
    await waitFor(() =>
      expect(calls.filter((c) => c.path === list).length).toBeGreaterThan(1),
    );
    expect(calls.filter((c) => c.path === endpoint)).toHaveLength(1);
    await user.click(modal().getByRole("button", { name: "验证并保存" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.path === endpoint)).toHaveLength(2),
    );
    expect(
      calls
        .filter((c) => c.path === endpoint)
        .map((c) => (c.body as { version: number }).version),
    ).toEqual([3, 3]);
  });

  it("requires an active parent before offering identity binding", async () => {
    const ready = false;
    const calls = renderSettings(
      "/o/o1/settings/account-binding",
      (call) => {
        if (call.path === list)
          return json(200, view(ready ? binding(true) : null, false, ready));
        return undefined;
      },
      "MEMBER",
    );
    expect(
      await screen.findByText("请先让管理员连接 Shortcut workspace"),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: "绑定" })).toBeNull();
    expect(calls.filter((c) => c.path === endpoint)).toHaveLength(0);
  });

  it("disconnects only the selected binding after confirmation with its version", async () => {
    const disconnect = `/v1/organizations/o1/integrations/connections/${personal}/disconnect`;
    const calls = renderSettings(
      "/o/o1/settings/account-binding",
      (call) => {
        if (call.path === list) return json(200, view(binding(true)));
        if (call.path === disconnect && call.method === "POST")
          return json(200, {
            connection: { id: personal, status: "DISCONNECTED" },
            changed: true,
          });
        return undefined;
      },
      "MEMBER",
    );
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "已连接" }));
    await user.click(
      screen.getByRole("menuitem", { name: "断开 workspace-1" }),
    );
    expect(screen.getByText(/尚未开始发送的本人评论停止发送/)).toBeVisible();
    expect(calls.filter((c) => c.path === disconnect)).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "断开连接" }));
    await waitFor(() =>
      expect(calls.find((c) => c.path === disconnect)?.body).toEqual({
        version: 3,
      }),
    );
  });
});
