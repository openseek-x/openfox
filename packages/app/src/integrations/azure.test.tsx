import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { json, renderSettings, ts, type Route } from "../settings/testing";
import {
  azureAuthorizeURL,
  nextAzurePage,
  type AzureProjectsPage,
} from "./azure-api";
const tenant = "11111111-1111-4111-8111-111111111111",
  p1 = "22222222-2222-4222-8222-222222222222",
  p2 = "33333333-3333-4333-8333-333333333333",
  repo = "44444444-4444-4444-8444-444444444444",
  account = "55555555-5555-4555-8555-555555555555";
const base = "/v1/organizations/o1/integrations/azure_devops",
  conn = `${base}/connections/c1`,
  endpoint = `${base}/oauth/sessions/s1`,
  page = "/o/o1/settings/integrations/azure_devops";
const connection = {
  id: "c1",
  provider: "azure_devops",
  scope: "ORGANIZATION",
  external_id: account,
  display_name: "Shared Org",
  status: "ACTIVE",
  version: 3,
  created_at: ts,
  updated_at: ts,
  azure_delegating_user: "Alex",
  azure_delegating_user_id: repo,
  azure_tenant_id: tenant,
};
const settings = {
  link_pull_requests: true,
  link_commits: false,
  pr_backlink_private: true,
  pr_backlink_public: false,
  issue_backlink_private: false,
  issue_backlink_public: false,
  branch_format: "username/identifier-title",
};
const cfg = {
  version: 4,
  connection_version: 3,
  grant_generation: 2,
  repositories: [
    {
      project_id: p1,
      project_name: "First",
      repository_id: repo,
      repository_name: "First repo",
    },
  ],
  hooks: [],
};
function baseRoute(status = "ACTIVE", configured = true): Route {
  return (c) =>
    c.path === base
      ? json(200, {
          provider: {
            id: "azure_devops",
            name: "Azure DevOps",
            category: "project",
            availability: "coming_soon",
            template: "B",
            configured,
            bindable: configured,
            capabilities: ["import"],
            summary: { connections: 1, status: "ok" },
          },
          can_manage: true,
          connections: [{ ...connection, status }],
          personal_connections: [],
          sync_links: [],
        })
      : c.path.endsWith("/imports")
        ? json(200, {
            imports: [],
            total: 0,
            next_cursor: "",
            can_manage: true,
          })
        : c.path === `${base}/settings`
          ? json(200, {
              provider: "azure_devops",
              settings,
              version: 1,
              can_manage: true,
            })
          : c.path === conn + "/repositories"
            ? json(200, cfg)
            : c.path === conn + "/work"
              ? json(200, {
                  work: [],
                  backlinks: [],
                  next_work_after: "",
                  next_backlink_after: "",
                })
              : undefined;
}
function selecting(purpose = "organization") {
  return {
    id: "s1",
    purpose,
    status: "SELECTING",
    expires_at: "2099-01-01T00:00:00Z",
    error_code: "",
    tenant_id: tenant,
    account_id: account,
    identity: { id: repo, display_name: "Alex" },
    accounts: [
      { id: account, name: "Shared Org" },
      { id: p2, name: "Second Org" },
    ],
    affected_accounts: [],
  };
}
beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());
it("requires exact account selection and sharing consent; personal completion sends no credentials", async () => {
  const calls = renderSettings(
    "/integrations/oauth/complete?provider=azure_devops&organization=o1&session=s1",
    (c) =>
      c.path === endpoint
        ? json(200, selecting())
        : c.path === endpoint + "/complete"
          ? json(200, connection)
          : undefined,
  );
  const confirm = await screen.findByRole("button", { name: "确认共享此组织" });
  expect(confirm).toBeDisabled();
  await userEvent.click(screen.getByLabelText("Azure DevOps 组织"));
  await userEvent.click(screen.getByRole("option", { name: "Second Org" }));
  expect(confirm).toBeDisabled();
  await userEvent.click(screen.getByRole("checkbox"));
  await userEvent.click(confirm);
  await waitFor(() =>
    expect(calls.find((c) => c.path === endpoint + "/complete")?.body).toEqual({
      account_id: p2,
    }),
  );
});
it("pins reauthorization to its account and names every affected connection", async () => {
  renderSettings(
    "/integrations/oauth/complete?provider=azure_devops&organization=o1&session=s1",
    (c) =>
      c.path === endpoint
        ? json(200, {
            ...selecting("reauthorize"),
            affected_accounts: [
              { id: account, name: "Shared Org" },
              { id: p2, name: "Second Org" },
            ],
          })
        : undefined,
  );
  const select = await screen.findByLabelText("Azure DevOps 组织");
  expect(select).toBeDisabled();
  expect(select).toHaveTextContent("Shared Org");
  expect(screen.getAllByRole("listitem")).toHaveLength(2);
  expect(screen.getByText(/个人身份须重新验证/)).toBeVisible();
});
it("personal identity completion does not select another account or retain a token", async () => {
  const calls = renderSettings(
    "/integrations/oauth/complete?provider=azure_devops&organization=o1&session=s1",
    (c) =>
      c.path === endpoint
        ? json(200, selecting("personal"))
        : c.path === endpoint + "/complete"
          ? json(200, connection)
          : undefined,
  );
  await userEvent.click(
    await screen.findByRole("button", { name: "确认个人身份" }),
  );
  await waitFor(() =>
    expect(calls.find((c) => c.path === endpoint + "/complete")?.body).toEqual(
      {},
    ),
  );
  expect(screen.queryByRole("combobox")).toBeNull();
  expect(screen.getByText(/不会保存个人访问令牌/)).toBeVisible();
});
it("requires tenant UUID before organization authorization", async () => {
  const calls = renderSettings(page, (c) =>
    c.path === base + "/oauth/start"
      ? json(200, {
          session_id: "s1",
          authorize_url: `https://login.microsoftonline.com/${tenant}/oauth2/v2.0/authorize?state=x`,
          expires_at: "2099-01-01T00:00:00Z",
        })
      : baseRoute()(c),
  );
  vi.spyOn(window, "open").mockReturnValue(null);
  const button = await screen.findByRole("button", {
    name: "连接 Azure DevOps",
  });
  expect(button).toBeDisabled();
  await userEvent.type(
    screen.getByLabelText("Microsoft Entra 租户 ID"),
    tenant,
  );
  await userEvent.click(button);
  await waitFor(() =>
    expect(calls.find((c) => c.path === base + "/oauth/start")?.body).toEqual({
      purpose: "organization",
      tenant_id: tenant,
    }),
  );
  expect(screen.getByRole("link", { name: "打开确认页" })).toHaveAttribute(
    "href",
    expect.stringContaining("provider=azure_devops"),
  );
});
it("replaces the full multi-project repository set with the original CAS version", async () => {
  const calls = renderSettings(page, (c) =>
    c.path === conn + "/repositories" && c.method === "PUT"
      ? json(200, { ...cfg, version: 5 })
      : c.path === "/v1/organizations/o1/imports/azure_devops/projects"
        ? json(200, { projects: [{ id: p2, name: "Second" }], next_offset: "" })
        : c.path === conn + "/repositories/available"
          ? json(200, {
              repositories: [
                {
                  id: p2,
                  name: "Second repo",
                  project: { id: p2, name: "Second" },
                },
              ],
            })
          : baseRoute()(c),
  );
  await userEvent.click(
    await screen.findByRole("button", { name: "仓库与 Hook 状态" }),
  );
  const dialog = screen.getByRole("dialog");
  await userEvent.click(await within(dialog).findByLabelText("项目"));
  await userEvent.click(screen.getByRole("option", { name: "Second" }));
  await userEvent.click(
    await screen.findByLabelText(new RegExp("Second repo")),
  );
  await userEvent.click(within(dialog).getByRole("button", { name: "保存" }));
  await waitFor(() =>
    expect(
      calls.find((c) => c.method === "PUT" && c.path === conn + "/repositories")
        ?.body,
    ).toEqual({
      version: 4,
      connection_version: 3,
      grant_generation: 2,
      repositories: [
        { project_id: p1, repository_id: repo },
        { project_id: p2, repository_id: p2 },
      ],
    }),
  );
});
it("retains disconnected cleanup status and never starts new hooks from that view", async () => {
  renderSettings(page, (c) =>
    c.path === conn + "/repositories"
      ? json(200, {
          ...cfg,
          hooks: [
            {
              id: "h",
              project_id: p1,
              event_type: "git.push",
              state: "NEEDS_ATTENTION",
              desired: false,
              generation: 2,
              manual_cleanup: true,
              subscription_id: repo,
              last_error: "cleanup_expired",
            },
          ],
        })
      : baseRoute("DISCONNECTED")(c),
  );
  await userEvent.click(
    await screen.findByRole("button", { name: "仓库与 Hook 状态" }),
  );
  expect(await screen.findByText("需手工清理远端 Hook")).toBeVisible();
  expect(screen.getByText(repo)).toBeVisible();
  expect(
    within(screen.getByRole("dialog")).queryByRole("button", { name: "保存" }),
  ).toBeNull();
});
it("requires explicit duplicate-risk confirmation for unknown backlink resend", async () => {
  const calls = renderSettings(page, (c) =>
    c.path === conn + "/work"
      ? json(200, {
          work: [
            {
              id: "w",
              kind: "WORK_ITEM",
              project_id: p1,
              repository_id: "",
              object_id: 7,
              state: "FAILED",
              last_error: "source_unavailable",
            },
          ],
          backlinks: [
            {
              id: "b1",
              link_id: "l1",
              project_id: p1,
              repository_id: repo,
              pr_number: 1,
              state: "UNKNOWN",
              last_error: "comment_result_unknown",
            },
          ],
          next_work_after: "",
          next_backlink_after: "",
        })
      : c.path === conn + "/backlinks/b1/retry"
        ? json(200, { scheduled: true })
        : baseRoute()(c),
  );
  await userEvent.click(
    await screen.findByRole("button", { name: "仓库与 Hook 状态" }),
  );
  expect(await screen.findByText(/Azure Boards #7/)).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: "确认后再次发送" }));
  const buttons = screen.getAllByRole("button", { name: "确认后再次发送" });
  expect(buttons.at(-1)).toBeDisabled();
  await userEvent.click(screen.getByLabelText(/我了解可能产生重复评论/));
  await userEvent.click(buttons.at(-1)!);
  await waitFor(() =>
    expect(
      calls.find((c) => c.path === conn + "/backlinks/b1/retry")?.body,
    ).toEqual({
      action: "resend",
      confirm: "SEND_AGAIN_MAY_DUPLICATE",
      connection_version: 3,
      grant_generation: 2,
    }),
  );
});
it("allows only the fixed tenant OAuth host and bounded nonrepeating project cursors", () => {
  expect(
    azureAuthorizeURL(
      `https://login.microsoftonline.com/${tenant}/oauth2/v2.0/authorize`,
    ),
  ).toBe(true);
  for (const u of [
    `https://evil.invalid/${tenant}/oauth2/v2.0/authorize`,
    `https://login.microsoftonline.com/common/oauth2/v2.0/authorize`,
    `https://login.microsoftonline.com/${tenant}/oauth2/v2.0/authorize#x`,
  ])
    expect(azureAuthorizeURL(u)).toBe(false);
  const p = { projects: [], next_offset: "n" } as unknown as AzureProjectsPage;
  expect(nextAzurePage(p, [p], [""])).toBe("n");
  expect(nextAzurePage(p, [p], ["n"])).toBeUndefined();
  expect(nextAzurePage(p, Array(50).fill(p), [""])).toBeUndefined();
});

it("saves dedicated Azure sync mappings by fetched version and requests history separately", async () => {
  const mappings = {
    version: 9,
    space_id: "space1",
    types: [],
    statuses: [],
    priorities: [],
    analysis: { types: [], statuses: [], priority_fields: [] },
  };
  const calls = renderSettings(page, (c) =>
    c.path === "/v1/organizations/o1/imports"
      ? json(200, {
          imports: [
            {
              id: "j1",
              source: "azure_devops_api",
              space_name: "Local",
              source_label: "Boards",
              sync: {
                state: "ACTIVE",
                direction: "INBOUND",
                version: 7,
                error_code: "TYPE_MAPPING",
                checking: false,
              },
            },
          ],
          total: 1,
          next_cursor: "",
          can_manage: true,
        })
      : c.path === "/v1/organizations/o1/imports/j1/azure-mappings"
        ? json(200, c.method === "PUT" ? {} : mappings)
        : c.path === "/v1/organizations/o1/imports/j1/azure-history"
          ? json(200, {})
          : baseRoute()(c),
  );
  await userEvent.click(
    await screen.findByRole("button", { name: "类型、状态与优先级映射" }),
  );
  await userEvent.click(await screen.findByRole("button", { name: "保存" }));
  await waitFor(() =>
    expect(
      calls.find((c) => c.method === "PUT" && c.path.endsWith("azure-mappings"))
        ?.body,
    ).toEqual({ version: 9, types: [], statuses: [], priorities: [] }),
  );
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(screen.queryByRole("button", { name: "映射状态" })).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "补导已有工作项" }));
  await userEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", {
      name: "补导已有工作项",
    }),
  );
  await waitFor(() =>
    expect(calls.find((c) => c.path.endsWith("azure-history"))?.body).toEqual({
      version: 7,
    }),
  );
});
it("starts personal OAuth against a selected parent without asking for a tenant or token", async () => {
  vi.spyOn(window, "open").mockReturnValue(null);
  const calls = renderSettings(
    "/o/o1/settings/account-binding",
    (c) =>
      c.path === "/v1/organizations/o1/me/account-bindings"
        ? json(200, {
            providers: [
              {
                id: "azure_devops",
                name: "Azure DevOps",
                availability: "available",
                configured: true,
                bindable: true,
                organization_ready: true,
                binding: null,
                instances: [
                  {
                    connection_id: "c1",
                    display_name: "Shared Org",
                    external_id: account,
                    host: "Shared Org",
                    binding: null,
                  },
                ],
              },
            ],
          })
        : c.path === base + "/oauth/start"
          ? json(200, {
              session_id: "s1",
              authorize_url: `https://login.microsoftonline.com/${tenant}/oauth2/v2.0/authorize`,
              expires_at: "2099-01-01T00:00:00Z",
            })
          : c.path === endpoint
            ? json(200, { ...selecting("personal"), status: "PENDING" })
            : undefined,
    "MEMBER",
  );
  const row = await screen.findByTestId("binding-azure_devops");
  await userEvent.click(within(row).getByRole("button", { name: "绑定" }));
  await waitFor(() =>
    expect(calls.find((c) => c.path === base + "/oauth/start")?.body).toEqual({
      purpose: "personal",
      connection_id: "c1",
      version: 0,
    }),
  );
  expect(screen.queryByLabelText("Microsoft Entra 租户 ID")).toBeNull();
});

it("keeps the configured workflow visible when Entra secrets are missing without allowing authorization", async () => {
  const calls = renderSettings(page, baseRoute("ACTIVE", false));
  expect(await screen.findByText(/两个独立的 Entra 应用/)).toBeVisible();
  expect(
    screen.getByRole("button", { name: "连接 Azure DevOps" }),
  ).toBeDisabled();
  expect(
    screen.getByRole("link", { name: /从 Azure DevOps 导入/ }),
  ).toBeVisible();
  expect(calls.some((c) => c.path.endsWith("oauth/start"))).toBe(false);
});
