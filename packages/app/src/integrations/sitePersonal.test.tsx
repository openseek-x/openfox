import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { findCall, json, renderSettings, ts } from "../settings/testing";

const base = "/v1/organizations/o1/integrations";

beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

it.each([
  { status: "ACTIVE", version: 7 },
  { status: "NEEDS_ATTENTION", version: 7 },
  { status: "DISCONNECTED", version: 0 },
])(
  "binds a $status personal account with version $version",
  async ({ status, version }) => {
    const user = userEvent.setup();
    const connection = {
      id: "c1",
      provider: "jira",
      scope: "ORGANIZATION",
      display_name: "Jira QA",
      instance_url: "https://jira.example.test",
      external_id: "jira-qa",
      status: "ACTIVE",
      version: 1,
      created_at: ts,
      updated_at: ts,
      created_by_email: "lin@openfox.dev",
    };
    const calls = renderSettings("/o/o1/settings/integrations/jira", (call) => {
      if (call.method === "GET" && call.path === `${base}/jira`)
        return json(200, {
          provider: {
            id: "jira",
            name: "Jira",
            template: "A",
            availability: "available",
            capabilities: ["import", "personal_account"],
            bindable: false,
          },
          can_manage: true,
          connections: [connection],
          personal_connections: [
            {
              ...connection,
              id: "p1",
              scope: "PERSONAL",
              parent_connection_id: "c1",
              account_kind: "sync_comments",
              status,
              version: 7,
            },
          ],
          sync_links: [],
        });
      if (call.method === "PUT" && call.path === `${base}/site-personal`)
        return json(200, {});
      if (call.method === "GET" && call.path.includes("project-sync"))
        return json(200, { jobs: [], next_cursor: "" });
      return undefined;
    });
    await user.click(
      await screen.findByRole("button", { name: "绑定我的账号" }),
    );
    const dialog = screen.getByRole("dialog");
    await user.type(
      within(dialog).getByLabelText("令牌"),
      "fixture-personal-token",
    );
    await user.click(
      within(dialog).getByRole("button", { name: "绑定我的账号" }),
    );
    await waitFor(() =>
      expect(findCall(calls, "PUT", `${base}/site-personal`)?.body).toEqual({
        connection_id: "c1",
        version,
        jira: { Kind: "pat", Secret: "fixture-personal-token" },
      }),
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  },
);

it("binds a personal ZenTao account for comment sync", async () => {
  const user = userEvent.setup();
  const connection = {
    id: "c1",
    provider: "zentao",
    scope: "ORGANIZATION",
    display_name: "ZenTao QA",
    instance_url: "https://zentao.example.test",
    external_id: "zentao-qa",
    status: "ACTIVE",
    version: 1,
    created_at: ts,
    updated_at: ts,
    created_by_email: "lin@openfox.dev",
  };
  const calls = renderSettings("/o/o1/settings/integrations/zentao", (call) => {
    if (call.method === "GET" && call.path === `${base}/zentao`)
      return json(200, {
        provider: {
          id: "zentao",
          name: "禅道",
          template: "A",
          availability: "available",
          capabilities: ["import", "personal_account"],
          bindable: false,
        },
        can_manage: true,
        connections: [connection],
        personal_connections: [],
        sync_links: [],
      });
    if (call.method === "PUT" && call.path === `${base}/site-personal`)
      return json(200, {});
    if (call.method === "GET" && call.path.includes("project-sync"))
      return json(200, { jobs: [], next_cursor: "" });
    return undefined;
  });
  await user.click(
    await screen.findByRole("button", { name: "绑定我的账号" }),
  );
  const dialog = screen.getByRole("dialog");
  await user.type(within(dialog).getByLabelText("账号或邮箱"), "lin");
  await user.type(within(dialog).getByLabelText("密码"), "fixture-password");
  await user.click(
    within(dialog).getByRole("button", { name: "绑定我的账号" }),
  );
  await waitFor(() =>
    expect(findCall(calls, "PUT", `${base}/site-personal`)?.body).toEqual({
      connection_id: "c1",
      version: 0,
      zentao: { account: "lin", password: "fixture-password" },
    }),
  );
});
