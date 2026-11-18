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
import { bitbucketAuthorizeURL } from "./BitbucketComplete";

const base = "/v1/organizations/o1/integrations/bitbucket";
const endpoint = `${base}/oauth/sessions/s1`;
const completePath =
  "/integrations/oauth/complete?provider=bitbucket&organization=o1&session=s1&status=ready";
const connection = {
  id: "c1",
  provider: "bitbucket",
  scope: "ORGANIZATION",
  external_id: "w1",
  display_name: "Design",
  status: "ACTIVE",
  version: 3,
  created_at: ts,
  updated_at: ts,
  bitbucket_delegating_user: "Alex",
  bitbucket_delegating_user_id: "sender-1",
};
const selecting = (purpose = "organization") => ({
  id: "s1",
  purpose,
  status: "SELECTING",
  error_code: "",
  expires_at: "2099-01-01T00:00:00Z",
  identity: { uuid: "sender-1", display_name: "Alex", nickname: "alex" },
  workspaces: [
    { uuid: "w1", name: "Design", slug: "design" },
    { uuid: "w2", name: "Engineering", slug: "engineering" },
  ],
  workspace_id: purpose === "organization" ? undefined : "w1",
  parent_version: 3,
  subject_changed: false,
});
function detail(
  manage = true,
  configured = true,
  connections = [connection],
): Route {
  return (c) =>
    c.path === base
      ? json(200, {
          provider: {
            id: "bitbucket",
            name: "Bitbucket",
            category: "code",
            availability: "coming_soon",
            template: "B",
            configured,
            bindable: configured,
            capabilities: ["pr_link", "personal_account"],
            summary: { connections: connections.length, status: "ok" },
          },
          can_manage: manage,
          connections,
          personal_connections: [],
          sync_links: [],
        })
      : undefined;
}
describe("Bitbucket Cloud authorization", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });
  it("requires exactly one workspace and explicit organization consent", async () => {
    const calls = renderSettings(completePath, (c) =>
      c.path === endpoint
        ? json(200, selecting())
        : c.path === `${endpoint}/complete`
          ? json(200, connection)
          : undefined,
    );
    const button = await screen.findByRole("button", { name: "确认组织授权" });
    expect(button).toBeDisabled();
    const user = userEvent.setup();
    await user.click(screen.getByLabelText("Bitbucket workspace"));
    await user.click(
      screen.getByRole("option", { name: "Engineering · engineering" }),
    );
    expect(button).toBeDisabled();
    expect(screen.getByText(/批准 PR、管理 PR task/)).toBeVisible();
    await user.click(screen.getByRole("checkbox"));
    await user.click(button);
    expect(await screen.findByText(/授权确认已完成/)).toBeVisible();
    expect(calls.find((c) => c.path === `${endpoint}/complete`)?.body).toEqual({
      workspace_id: "w2",
    });
  });
  it("requires both changed-sender acknowledgement and organization consent while keeping the workspace", async () => {
    const calls = renderSettings(completePath, (c) =>
      c.path === endpoint
        ? json(200, {
            ...selecting("reauthorize"),
            subject_changed: true,
            previous_subject: {
              uuid: "old-sender",
              display_name: "Sam",
              nickname: "sam",
            },
          })
        : c.path === `${endpoint}/complete`
          ? json(200, connection)
          : undefined,
    );
    const button = await screen.findByRole("button", { name: "确认组织授权" });
    expect(screen.getByText("原发送者：Sam · old-sender")).toBeVisible();
    expect(screen.getByText("新发送者：Alex · sender-1")).toBeVisible();
    expect(screen.getByLabelText("Bitbucket workspace")).toBeDisabled();
    const user = userEvent.setup();
    await user.click(screen.getByLabelText(/我同意将此 workspace/));
    expect(button).toBeDisabled();
    await user.click(screen.getByLabelText(/我明确同意更换/));
    await user.click(button);
    await waitFor(() =>
      expect(
        calls.find((c) => c.path === `${endpoint}/complete`)?.body,
      ).toEqual({ workspace_id: "w1" }),
    );
  });
  it("confirms personal UUID with an empty body and explains snapshot limits", async () => {
    const calls = renderSettings(
      completePath,
      (c) =>
        c.path === endpoint
          ? json(200, { ...selecting("personal"), workspaces: [] })
          : c.path === `${endpoint}/complete`
            ? json(200, connection)
            : undefined,
      "MEMBER",
    );
    const button = await screen.findByRole("button", {
      name: "确认绑定本人身份",
    });
    expect(button).toBeDisabled();
    expect(screen.getByText(/不代表持续 workspace 成员资格/)).toBeVisible();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(button);
    await waitFor(() =>
      expect(
        calls.find((c) => c.path === `${endpoint}/complete`)?.body,
      ).toEqual({}),
    );
  });
  it.each(["EXPIRED", "FAILED"])(
    "does not trust ready URL for %s",
    async (status) => {
      const calls = renderSettings(completePath, (c) =>
        c.path === endpoint ? json(200, { ...selecting(), status }) : undefined,
      );
      expect(
        await screen.findByText(
          status === "EXPIRED" ? /本次授权已过期/ : /授权未完成、已被消费/,
        ),
      ).toBeVisible();
      expect(screen.queryByRole("button", { name: "确认组织授权" })).toBeNull();
      expect(calls.some((c) => c.method === "POST")).toBe(false);
    },
  );
  it("rejects local expiry during SELECTING", async () => {
    renderSettings(completePath, (c) =>
      c.path === endpoint
        ? json(200, { ...selecting(), expires_at: ts })
        : undefined,
    );
    expect(await screen.findByText(/本次授权已过期/)).toBeVisible();
    expect(screen.queryByRole("button", { name: "确认组织授权" })).toBeNull();
  });
  it("recovers completed confirmation without replaying a POST or claiming current ACTIVE status", async () => {
    const calls = renderSettings(completePath, (c) =>
      c.path === endpoint
        ? json(200, { ...selecting(), status: "COMPLETED", binding_id: "c1" })
        : undefined,
    );
    expect(
      await screen.findByText(/当前连接状态请返回设置页查看/),
    ).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });
  it("does not report success when permission changes during confirmation", async () => {
    const postMessage = vi.fn();
    vi.stubGlobal("opener", { postMessage });
    renderSettings(completePath, (c) =>
      c.path === endpoint
        ? json(200, { ...selecting("personal"), workspaces: [] })
        : c.path === `${endpoint}/complete`
          ? apiError(403, "BITBUCKET_PERMISSION_DENIED")
          : undefined,
    );
    const button = await screen.findByRole("button", {
      name: "确认绑定本人身份",
    });
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(button);
    expect(
      await screen.findByText(/没有此操作所需的组织或 workspace/),
    ).toBeVisible();
    expect(postMessage).not.toHaveBeenCalled();
  });
  it("disables binding when unconfigured and hides manager actions", async () => {
    renderSettings(
      "/o/o1/settings/integrations/bitbucket",
      detail(false, false, []),
      "MEMBER",
    );
    expect(await screen.findByText(/服务端尚未配置 Bitbucket/)).toBeVisible();
    expect(
      screen.getByRole("button", { name: "授权 Bitbucket workspace" }),
    ).toBeDisabled();
    expect(screen.queryByRole("button", { name: "重新授权" })).toBeNull();
  });
  it("shows current sender and sends parent CAS for reauthorization with popup fallback", async () => {
    vi.spyOn(window, "open").mockReturnValue(null);
    const calls = renderSettings(
      "/o/o1/settings/integrations/bitbucket",
      (c) =>
        c.path === `${base}/oauth/start`
          ? json(200, {
              session_id: "s1",
              authorize_url:
                "https://bitbucket.org/site/oauth2/authorize?state=fixture",
              expires_at: "2099-01-01T00:00:00Z",
            })
          : c.path === endpoint
            ? json(200, { ...selecting(), status: "PENDING" })
            : detail()(c),
    );
    expect(
      await screen.findByText("组织发送者：Alex · sender-1"),
    ).toBeVisible();
    expect(
      screen.queryByRole("button", { name: "授权 Bitbucket workspace" }),
    ).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "重新授权" }));
    expect(
      await screen.findByRole("link", { name: "在新标签页打开授权页" }),
    ).toHaveAttribute(
      "href",
      "https://bitbucket.org/site/oauth2/authorize?state=fixture",
    );
    expect(calls.find((c) => c.path === `${base}/oauth/start`)?.body).toEqual({
      purpose: "reauthorize",
      connection_id: "c1",
      version: 3,
    });
  });
  it("starts first personal association for the exact parent with binding version zero", async () => {
    vi.spyOn(window, "open").mockReturnValue(null);
    const calls = renderSettings(
      "/o/o1/settings/account-binding",
      (c) =>
        c.path.endsWith("/me/account-bindings")
          ? json(200, {
              providers: [
                {
                  id: "bitbucket",
                  name: "Bitbucket",
                  method: "oauth",
                  availability: "available",
                  organization_ready: true,
                  instances: [
                    {
                      connection_id: "c1",
                      host: "Design",
                      version: 3,
                      display_name: "Design",
                    },
                  ],
                },
              ],
            })
          : c.path === `${base}/oauth/start`
            ? json(200, {
                session_id: "s1",
                authorize_url:
                  "https://bitbucket.org/site/oauth2/authorize?state=fixture",
                expires_at: "2099-01-01T00:00:00Z",
              })
            : c.path === endpoint
              ? json(200, { ...selecting("personal"), status: "PENDING" })
              : undefined,
      "MEMBER",
    );
    const row = await screen.findByTestId("binding-bitbucket");
    await userEvent.click(within(row).getByRole("button", { name: "绑定" }));
    await waitFor(() =>
      expect(calls.find((c) => c.path === `${base}/oauth/start`)?.body).toEqual(
        { purpose: "personal", connection_id: "c1", version: 0 },
      ),
    );
  });
  it("keeps an existing identity visible and unlinkable while missing personal configuration disables revalidation", async () => {
    const binding = {
      connection_id: "p1",
      display_name: "Verified person",
      detail: "workspace-uuid",
      avatar_url: "",
      created_at: ts,
      version: 1,
    };
    const calls = renderSettings(
      "/o/o1/settings/account-binding",
      (c) =>
        c.path.endsWith("/me/account-bindings")
          ? json(200, {
              email: "person@example.test",
              jira_sites: [],
              providers: [
                {
                  id: "bitbucket",
                  name: "Bitbucket",
                  method: "oauth",
                  availability: "available",
                  organization_ready: false,
                  binding,
                  instances: [
                    {
                      connection_id: "c1",
                      host: "workspace-uuid",
                      display_name: "Design",
                      binding,
                    },
                  ],
                },
              ],
            })
          : undefined,
      "MEMBER",
    );
    const row = await screen.findByTestId("binding-bitbucket");
    expect(within(row).getByText(/Verified person/)).toBeVisible();
    await userEvent.click(within(row).getByRole("button", { name: "已连接" }));
    expect(screen.getByRole("menuitem", { name: /重新验证/ })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    await userEvent.click(screen.getByRole("menuitem", { name: /重新验证/ }));
    expect(screen.getByRole("menuitem", { name: /断开/ })).not.toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });
  it("accepts only the exact HTTPS authorization endpoint", () => {
    expect(
      bitbucketAuthorizeURL(
        "https://bitbucket.org/site/oauth2/authorize?state=x",
      ),
    ).toBe(true);
    for (const url of [
      "http://bitbucket.org/site/oauth2/authorize",
      "https://bitbucket.org.evil.test/site/oauth2/authorize",
      "https://user:secret@bitbucket.org/site/oauth2/authorize",
      "https://api.bitbucket.org/site/oauth2/authorize",
      "https://bitbucket.org:8443/site/oauth2/authorize",
      "https://bitbucket.org/site/oauth2/access_token",
      "https://bitbucket.org/site/oauth2/authorize#secret",
    ])
      expect(bitbucketAuthorizeURL(url)).toBe(false);
  });
});
