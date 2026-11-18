import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { json, renderSettings, ts, apiError } from "../settings/testing";
const provider = {
  id: "zoho_bugtracker",
  name: "Zoho BugTracker",
  category: "project",
  availability: "available",
  template: "A",
  configured: true,
  bindable: true,
  capabilities: ["organization", "personal", "import"],
  summary: { connections: 1, status: "ok" },
};
const connection = {
  id: "c1",
  provider: "zoho_bugtracker",
  scope: "ORGANIZATION",
  external_id: "us:2063927",
  display_name: "Design",
  status: "ACTIVE",
  version: 3,
  created_at: ts,
  updated_at: ts,
};
const detail = {
  provider,
  can_manage: true,
  connections: [connection],
  personal_connections: [],
  sync_links: [],
};
const base = "/v1/organizations/o1/integrations/zoho_bugtracker/oauth";
const session = {
  id: "session",
  purpose: "organization",
  status: "SELECTING",
  error_code: "",
  expires_at: "2099-01-01T00:00:00Z",
  parent_version: 0,
  identity: {
    id: "2060758",
    name: "Admin",
    email: "verified@example.test",
    active: true,
    role: "admin",
  },
  portals: [{ id: "2063927", name: "Design", role: "admin" }],
};
beforeEach(() => localStorage.clear());
it("shows scope and lifecycle, disables unconfigured and non-manager creation, and has no sync controls", async () => {
  const calls = renderSettings(
    "/o/o1/settings/integrations/zoho_bugtracker",
    (c) =>
      c.path.endsWith("/integrations/zoho_bugtracker")
        ? json(200, {
            ...detail,
            provider: { ...provider, configured: false, bindable: false },
            can_manage: false,
          })
        : undefined,
    "MEMBER",
  );
  expect(await screen.findByText(/旧版 BugTracker REST/)).toBeVisible();
  expect(screen.getByRole("button", { name: "连接 Zoho 门户" })).toBeDisabled();
  expect(screen.queryByRole("button", { name: "重新授权" })).toBeNull();
  expect(screen.queryByRole("button", { name: /同步/ })).toBeNull();
  expect(calls.some((c) => c.method === "POST")).toBe(false);
});
it("starts exact reauthorization and rejects a foreign authorize URL", async () => {
  const open = vi.spyOn(window, "open").mockReturnValue(null);
  const calls = renderSettings(
    "/o/o1/settings/integrations/zoho_bugtracker",
    (c) =>
      c.path.endsWith("/integrations/zoho_bugtracker")
        ? json(200, detail)
        : c.path === base + "/start"
          ? json(200, {
              session_id: "session",
              authorize_url: "https://evil.test/oauth",
              expires_at: session.expires_at,
            })
          : undefined,
  );
  await userEvent.click(
    await screen.findByRole("button", { name: "重新授权" }),
  );
  expect(
    await screen.findByText("Zoho 授权或读取失败，请重新检查权限后重试。"),
  ).toBeVisible();
  expect(open).not.toHaveBeenCalled();
  expect(calls.find((c) => c.path === base + "/start")?.body).toEqual({
    purpose: "reauthorize",
    connection_id: "c1",
    version: 3,
  });
});
it("requires portal choice and organization consent before completing a persisted session", async () => {
  const calls = renderSettings(
    "/integrations/oauth/complete?provider=zoho_bugtracker&organization=o1&session=session&status=ok",
    (c) =>
      c.path === base + "/sessions/session"
        ? json(200, session)
        : c.path.endsWith("/complete")
          ? json(200, connection)
          : undefined,
  );
  const button = await screen.findByRole("button", { name: "确认连接门户" });
  expect(button).toBeDisabled();
  expect(calls.some((c) => c.method === "POST")).toBe(false);
  await userEvent.click(screen.getByLabelText("Zoho 门户"));
  await userEvent.click(
    screen.getByRole("option", { name: "Design · 2063927" }),
  );
  expect(button).toBeDisabled();
  await userEvent.click(screen.getByRole("checkbox"));
  await userEvent.click(button);
  await waitFor(() =>
    expect(calls.find((c) => c.path.endsWith("/complete"))?.body).toEqual({
      portal_id: "2063927",
    }),
  );
  expect(await screen.findByText("已连接")).toBeVisible();
});
it("personal flow requires an explicit identity confirmation with no portal or token request", async () => {
  const calls = renderSettings(
    "/integrations/oauth/complete?provider=zoho_bugtracker&organization=o1&session=session",
    (c) =>
      c.path === base + "/sessions/session"
        ? json(200, {
            ...session,
            purpose: "personal",
            portal_id: "2063927",
            portals: [],
          })
        : c.path.endsWith("/complete")
          ? json(200, connection)
          : undefined,
  );
  expect(await screen.findByText(/verified@example.test/)).toBeVisible();
  expect(calls.some((c) => c.method === "POST")).toBe(false);
  await userEvent.click(
    screen.getByRole("button", { name: "确认这是我的身份" }),
  );
  await waitFor(() =>
    expect(calls.find((c) => c.path.endsWith("/complete"))?.body).toEqual({}),
  );
});
it("does not trust successful callback query when persistent session failed", async () => {
  renderSettings(
    "/integrations/oauth/complete?provider=zoho_bugtracker&organization=o1&session=session&status=ok",
    (c) =>
      c.path === base + "/sessions/session"
        ? json(200, {
            ...session,
            status: "FAILED",
            error_code: "account_mismatch",
          })
        : apiError(404, "NOT_FOUND"),
  );
  expect(
    await screen.findByText("所选门户、本人身份或邮箱不匹配。"),
  ).toBeVisible();
  expect(screen.queryByRole("button", { name: "确认连接门户" })).toBeNull();
});

it.each([0, 7])(
  "personal start uses binding version %i, never the organization version",
  async (version) => {
    vi.spyOn(window, "open").mockReturnValue(null);
    const binding = version
      ? {
          connection_id: "personal",
          display_name: "Me",
          detail: "",
          avatar_url: "",
          created_at: ts,
          version,
        }
      : null;
    const calls = renderSettings("/o/o1/settings/account-binding", (c) =>
      c.path.endsWith("/me/account-bindings")
        ? json(200, {
            providers: [
              {
                ...provider,
                method: "oauth",
                organization_ready: true,
                binding,
                instances: [
                  {
                    connection_id: "c1",
                    host: "Design",
                    display_name: "Design",
                    binding,
                  },
                ],
              },
            ],
            jira_sites: [],
            email: "verified@example.test",
          })
        : c.path === base + "/start"
          ? json(200, {
              session_id: "session",
              authorize_url:
                "https://accounts.zoho.com/oauth/v2/auth?state=synthetic",
              expires_at: session.expires_at,
            })
          : c.path === base + "/sessions/session"
            ? json(200, { ...session, status: "PENDING", purpose: "personal" })
            : undefined,
    );
    const row = await screen.findByTestId("binding-zoho_bugtracker");
    await userEvent.click(within(row).getByRole("button"));
    if (version)
      await userEvent.click(
        await screen.findByRole("menuitem", { name: /重新验证/ }),
      );
    await waitFor(() =>
      expect(calls.find((c) => c.path === base + "/start")?.body).toEqual({
        purpose: "personal",
        connection_id: "c1",
        version,
      }),
    );
  },
);
