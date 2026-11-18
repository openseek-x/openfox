import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { apiError, json, renderSettings, ts } from "../settings/testing";

const path = "/v1/organizations/o1/me/account-bindings";
const parent = "10000000-0000-0000-0000-000000000001";
const bindingID = "10000000-0000-0000-0000-000000000002";
const binding = {
  connection_id: bindingID,
  display_name: "Test",
  detail: "exact.nick",
  avatar_url: "",
  created_at: ts,
  version: 3,
  tapd_identity: {
    nick: "exact.nick",
    company_id: "123",
    projects: ["101"],
    verified_at: ts,
  },
};

beforeEach(() => localStorage.clear());

describe("TAPD identity", () => {
  it("retains a failed draft, clears the successful token, and refreshes scoped identity", async () => {
    let current: typeof binding | null = null;
    let attempts = 0;
    const calls = renderSettings("/o/o1/settings/account-binding", (call) => {
      if (call.path === path && call.method === "GET")
        return json(200, {
          email: "lin@openfox.dev",
          jira_sites: [],
          providers: [
            {
              id: "tapd",
              name: "TAPD",
              availability: "available",
              method: "form",
              organization_ready: true,
              binding: current,
              instances: [
                {
                  connection_id: parent,
                  display_name: "Basic parent",
                  host: "123",
                  identity_supported: true,
                  binding: current,
                },
              ],
            },
          ],
        });
      if (call.path === `${path}/tapd` && call.method === "PUT") {
        if (++attempts === 1) return apiError(422, "TAPD_EMAIL_MISMATCH");
        current = binding;
        return json(200, { id: bindingID, version: 3 });
      }
      if (
        call.path === `${path}/tapd/projects/101` &&
        call.method === "DELETE"
      ) {
        current = null;
        return json(200, { id: bindingID, status: "DISCONNECTED", version: 4 });
      }
      return undefined;
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "绑定" }));
    const dialog = within(
      await screen.findByRole("dialog", { name: "验证 TAPD 个人身份" }),
    );
    const token = dialog.getByLabelText("用户态 OAuth Access Token");
    const project = dialog.getByLabelText("TAPD 项目 ID");
    await user.type(project, "101");
    await user.type(token, "disposable-token");
    await user.click(dialog.getByRole("button", { name: "验证并添加项目" }));
    expect(await dialog.findByRole("alert")).toHaveTextContent("邮箱");
    expect(token).toHaveValue("disposable-token");
    expect(project).toHaveValue("101");
    await user.click(dialog.getByRole("button", { name: "验证并添加项目" }));
    await waitFor(() => expect(token).toHaveValue(""));
    expect(await dialog.findByText("已验证昵称：exact.nick")).toBeVisible();
    expect(JSON.stringify(localStorage)).not.toContain("disposable-token");
    await user.click(dialog.getByRole("button", { name: "移除项目 101" }));
    await waitFor(() =>
      expect(dialog.queryByText("已验证昵称：exact.nick")).toBeNull(),
    );
    expect(calls.find((c) => c.method === "DELETE")?.body).toEqual({
      binding_id: bindingID,
      version: 3,
    });
  });

  it("explains the Basic parent requirement when only an organization Token exists", async () => {
    renderSettings("/o/o1/settings/account-binding", (call) =>
      call.path === path
        ? json(200, {
            email: "lin@openfox.dev",
            jira_sites: [],
            providers: [
              {
                id: "tapd",
                name: "TAPD",
                availability: "available",
                method: "form",
                organization_ready: false,
                binding: null,
                instances: [
                  {
                    connection_id: parent,
                    display_name: "Token parent",
                    host: "",
                    identity_supported: false,
                    binding: null,
                  },
                ],
              },
            ],
          })
        : undefined,
    );
    expect(
      await screen.findByText(
        "请先让管理员连接可读取项目成员邮箱的 TAPD Basic 企业 API",
      ),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: "绑定" })).toBeNull();
  });
});
