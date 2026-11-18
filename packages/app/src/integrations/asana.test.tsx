import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  apiError,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";
import { asanaAuthorizeURL } from "./asana-api";

const base = "/v1/organizations/o1/integrations/asana";
const endpoint = `${base}/oauth/sessions/s1`;
const completePath =
  "/integrations/oauth/complete?provider=asana&organization=o1&session=s1&status=ready";
const connection = {
  id: "c1",
  provider: "asana",
  scope: "ORGANIZATION",
  external_id: "w1",
  display_name: "Design",
  status: "ACTIVE",
  version: 3,
  created_at: ts,
  updated_at: ts,
  asana_delegating_user: "Alex",
  asana_delegating_user_id: "remote-1",
};
function detail(manage = true, configured = true): Route {
  return (call) =>
    call.path === base
      ? json(200, {
          provider: {
            id: "asana",
            name: "Asana",
            category: "project",
            availability: "available",
            template: "A",
            configured,
            bindable: configured,
            capabilities: ["import"],
            summary: { connections: 1, status: "ok" },
          },
          can_manage: manage,
          connections: [connection],
          personal_connections: [],
          sync_links: [],
        })
      : call.path.endsWith("/imports")
        ? json(200, {
            imports: [],
            next_cursor: "",
            total: 0,
            can_manage: manage,
          })
        : undefined;
}
const selecting = (purpose = "organization") => ({
  id: "s1",
  purpose,
  status: "SELECTING",
  error_code: "",
  expires_at: "2099-01-01T00:00:00Z",
  identity: {
    gid: "remote-1",
    name: "Alex",
    email: "lin@openfox.dev",
    workspaces: [
      { gid: "w1", name: "Design" },
      { gid: "w2", name: "Engineering" },
    ],
  },
  affected_workspaces: [],
});

describe("Asana authorization", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });
  it("requires workspace selection and explicit organizational sharing consent", async () => {
    const calls = renderSettings(completePath, (c) =>
      c.path === endpoint
        ? json(200, selecting())
        : c.path === `${endpoint}/complete`
          ? json(200, connection)
          : undefined,
    );
    const button = await screen.findByRole("button", {
      name: "确认共享此 workspace",
    });
    expect(button).toBeDisabled();
    const user = userEvent.setup();
    await user.click(screen.getByLabelText("Asana workspace"));
    await user.click(screen.getByRole("option", { name: "Engineering" }));
    expect(button).toBeDisabled();
    await user.click(screen.getByRole("checkbox"));
    await user.click(button);
    expect(await screen.findByText("已连接")).toBeVisible();
    expect(calls.find((c) => c.path === `${endpoint}/complete`)?.body).toEqual({
      workspace_id: "w2",
    });
  });
  it("shows every affected workspace and the re-verification consequence", async () => {
    renderSettings(completePath, (c) =>
      c.path === endpoint
        ? json(200, {
            ...selecting("reauthorize"),
            affected_workspaces: ["Design", "Engineering"],
          })
        : undefined,
    );
    expect(await screen.findByText(/原个人身份证明将失效/)).toBeVisible();
    expect(screen.getAllByRole("listitem").map((x) => x.textContent)).toEqual([
      "Design",
      "Engineering",
    ]);
  });
  it("locks reauthorization to the original workspace while still requiring sharing consent", async () => {
    const calls = renderSettings(completePath, (c) =>
      c.path === endpoint
        ? json(200, { ...selecting("reauthorize"), workspace_id: "w2" })
        : c.path === `${endpoint}/complete`
          ? json(200, connection)
          : undefined,
    );
    const picker = await screen.findByLabelText("Asana workspace");
    expect(picker).toBeDisabled();
    expect(picker).toHaveTextContent("Engineering");
    const button = screen.getByRole("button", { name: "确认共享此 workspace" });
    expect(button).toBeDisabled();
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(button);
    await waitFor(() =>
      expect(
        calls.find((c) => c.path === `${endpoint}/complete`)?.body,
      ).toEqual({ workspace_id: "w2" }),
    );
  });
  it("keeps project synchronization inbound-only and sends check-now with the link version", async () => {
    const calls = renderSettings("/o/o1/settings/integrations/asana", (c) => {
      if (c.path === "/v1/organizations/o1/imports")
        return json(200, {
          total: 1,
          next_cursor: "",
          imports: [
            {
              id: "j1",
              source: "asana_api",
              file_name: "Tasks",
              space_name: "Local",
              sync: {
                state: "ACTIVE",
                direction: "INBOUND",
                version: 7,
                checking: false,
                error_code: "",
                last_success: ts,
              },
            },
          ],
        });
      if (c.path === "/v1/organizations/o1/imports/j1/sync")
        return json(200, {});
      return detail()(c);
    });
    expect(await screen.findByLabelText("同步方向")).toBeDisabled();
    expect(screen.getByLabelText("同步方向")).toHaveTextContent(
      "Asana → OpenFox",
    );
    expect(screen.queryByText("Asana ↔ OpenFox")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "立即检测" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        version: 7,
        check_now: true,
      }),
    );
  });
  it("confirms only the returned personal identity, without token inputs or a workspace body", async () => {
    const calls = renderSettings(
      completePath,
      (c) =>
        c.path === endpoint
          ? json(200, {
              ...selecting("personal"),
              identity: {
                gid: "large-9007199254740993",
                name: "Alex",
                email: "lin@openfox.dev",
              },
            })
          : c.path === `${endpoint}/complete`
            ? json(200, connection)
            : undefined,
      "MEMBER",
    );
    expect(await screen.findByText("Alex · lin@openfox.dev")).toBeVisible();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    await userEvent.click(
      screen.getByRole("button", { name: "确认绑定此身份" }),
    );
    expect(await screen.findByText("已连接")).toBeVisible();
    expect(calls.find((c) => c.path === `${endpoint}/complete`)?.body).toEqual(
      {},
    );
  });
  it.each(["EXPIRED", "FAILED"])(
    "does not trust ready URL for a %s session",
    async (status) => {
      const calls = renderSettings(
        completePath,
        (c) =>
          c.path === endpoint
            ? json(200, {
                ...selecting("personal"),
                status,
                error_code: "account_mismatch",
              })
            : undefined,
        "MEMBER",
      );
      expect(
        await screen.findByText(
          status === "EXPIRED" ? /会话已过期/ : /本人及 workspace/,
        ),
      ).toBeVisible();
      expect(
        screen.queryByRole("button", { name: "确认绑定此身份" }),
      ).toBeNull();
      expect(calls.some((c) => c.method === "POST")).toBe(false);
    },
  );
  it("preserves a failed confirmation and does not notify opener of success", async () => {
    const postMessage = vi.fn();
    vi.stubGlobal("opener", { postMessage });
    renderSettings(completePath, (c) =>
      c.path === endpoint
        ? json(200, selecting("personal"))
        : c.path === `${endpoint}/complete`
          ? apiError(409, "VERSION_CONFLICT")
          : undefined,
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "确认绑定此身份" }),
    );
    expect(
      await screen.findByText(/已被.*修改|发生变化|刷新.*重试/),
    ).toBeVisible();
    expect(postMessage).not.toHaveBeenCalled();
  });
  it("renders an already completed session without replaying the write", async () => {
    const calls = renderSettings(completePath, (c) =>
      c.path === endpoint
        ? json(200, {
            ...selecting(),
            status: "COMPLETED",
            identity: undefined,
            binding_id: "c1",
          })
        : undefined,
    );
    expect(await screen.findByText("已连接")).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });
  it("keeps explanation available without configured apps and hides manager actions for members", async () => {
    renderSettings(
      "/o/o1/settings/integrations/asana",
      detail(false, false),
      "MEMBER",
    );
    expect(await screen.findByText(/OAuth 尚未配置/)).toBeVisible();
    expect(
      screen.getByRole("button", { name: "连接 Asana workspace" }),
    ).toBeDisabled();
    expect(screen.queryByRole("button", { name: "重新授权" })).toBeNull();
    expect(screen.getByText("委托授权者：Alex（remote-1）")).toBeVisible();
    expect(screen.getByText(/持续同步仅从 Asana/)).toBeVisible();
  });
  it("reauthorizes the selected parent with its version and offers a popup fallback", async () => {
    vi.spyOn(window, "open").mockReturnValue(null);
    const calls = renderSettings("/o/o1/settings/integrations/asana", (c) =>
      c.path === `${base}/oauth/start`
        ? json(200, {
            session_id: "s1",
            authorize_url:
              "https://app.asana.com/-/oauth_authorize?state=synthetic",
            expires_at: "2099-01-01T00:00:00Z",
          })
        : c.path === endpoint
          ? json(200, { ...selecting(), status: "PENDING" })
          : detail()(c),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "重新授权" }),
    );
    expect(
      await screen.findByRole("link", { name: "在新标签页打开授权页" }),
    ).toHaveAttribute(
      "href",
      "https://app.asana.com/-/oauth_authorize?state=synthetic",
    );
    expect(calls.find((c) => c.path === `${base}/oauth/start`)?.body).toEqual({
      purpose: "reauthorize",
      connection_id: "c1",
      version: 3,
    });
  });
  it("uses the chosen parent and existing personal binding CAS for account re-verification", async () => {
    vi.spyOn(window, "open").mockReturnValue(null);
    const calls = renderSettings(
      "/o/o1/settings/account-binding",
      (c) => {
        if (c.path.endsWith("/me/account-bindings"))
          return json(200, {
            email: "lin@openfox.dev",
            jira_sites: [],
            providers: [
              {
                id: "asana",
                name: "Asana",
                method: "oauth",
                availability: "available",
                organization_ready: true,
                binding: {
                  connection_id: "b1",
                  display_name: "Alex",
                  version: 4,
                },
                instances: [
                  {
                    connection_id: "c1",
                    host: "Design",
                    binding: {
                      connection_id: "b1",
                      display_name: "Alex",
                      version: 4,
                    },
                  },
                ],
              },
            ],
          });
        if (c.path === `${base}/oauth/start`)
          return json(200, {
            session_id: "s1",
            authorize_url: "https://app.asana.com/-/oauth_authorize",
            expires_at: "2099-01-01T00:00:00Z",
          });
        return undefined;
      },
      "MEMBER",
    );
    const row = await screen.findByTestId("binding-asana");
    await userEvent.click(within(row).getByRole("button"));
    await userEvent.click(
      screen.getByRole("menuitem", { name: "重新验证 Design" }),
    );
    await waitFor(() =>
      expect(calls.find((c) => c.path === `${base}/oauth/start`)?.body).toEqual(
        { purpose: "personal", connection_id: "c1", version: 4 },
      ),
    );
  });
  it("allows only fixed-host authorize URLs", () => {
    expect(
      asanaAuthorizeURL("https://app.asana.com/-/oauth_authorize?state=x"),
    ).toBe(true);
    for (const url of [
      "javascript:alert(1)",
      "https://app.asana.com.evil.test/-/oauth_authorize",
      "http://app.asana.com/-/oauth_authorize",
      "https://user@app.asana.com/-/oauth_authorize",
      "https://app.asana.com/api/1.0/users/me",
    ])
      expect(asanaAuthorizeURL(url)).toBe(false);
  });
});
