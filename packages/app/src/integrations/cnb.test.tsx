import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";
const base = "/v1/organizations/o1/integrations",
  conn = `${base}/connections/c1`;
const configuration = {
  version: 3,
  repositories: [],
  work: [],
  has_more: false,
};
function route(manage = true, more?: Route): Route {
  return (call) => {
    const custom = more?.(call);
    if (custom) return custom;
    if (call.path === `${base}/cnb`)
      return json(200, {
        provider: {
          id: "cnb",
          name: "CNB",
          category: "git",
          availability: "available",
          template: "B",
          capabilities: ["pr_link"],
          configured: true,
          bindable: true,
          summary: { connections: 1, status: "ok" },
        },
        can_manage: manage,
        connections: [
          {
            id: "c1",
            provider: "cnb",
            scope: "ORGANIZATION",
            external_id: "fixture",
            display_name: "fixture",
            status: "ACTIVE",
            version: 3,
            created_at: ts,
            updated_at: ts,
          },
        ],
        personal_connections: [],
        sync_links: [],
      });
    if (call.path === `${base}/cnb/settings`)
      return json(200, {
        provider: "cnb",
        can_manage: manage,
        version: 0,
        updated_at: null,
        settings: {
          link_pull_requests: true,
          link_commits: false,
          pr_backlink_private: true,
          pr_backlink_public: false,
          issue_backlink_private: true,
          issue_backlink_public: false,
          branch_format: "username/identifier-title",
        },
      });
    if (call.path === `${conn}/cnb/repositories`)
      return json(200, configuration);
    return undefined;
  };
}
describe("CNB explicit opt-in", () => {
  it("does not request secrets or repositories until opened, and member cannot open configuration", async () => {
    const calls = renderSettings(
      "/o/o1/settings/integrations/cnb",
      route(false),
      "MEMBER",
    );
    expect(
      await screen.findByRole("switch", {
        name: "关联 CNB Pull Request",
      }),
    ).toBeDisabled();
    expect(
      screen.queryByRole("button", { name: "查看事件桥接配置" }),
    ).not.toBeInTheDocument();
    expect(calls.some((c) => c.path.includes("/connections/"))).toBe(false);
  });
  it("CAS save conflict remains visible; capability denial does not claim token revocation", async () => {
    const user = userEvent.setup();
    let conflict = false;
    const calls = renderSettings(
      "/o/o1/settings/integrations/cnb",
      route(true, (call) => {
        if (call.path === `${conn}/repositories`)
          return json(200, {
            repositories: [
              {
                external_id: "repo-id",
                full_name: "team/repo",
                name: "repo",
                private: true,
                html_url: "https://cnb.cool/team/repo",
              },
            ],
            has_more: false,
          });
        if (call.path === `${conn}/cnb/repositories` && call.method === "PUT") {
          conflict = true;
          return apiError(409, "VERSION_CONFLICT");
        }
        if (call.path === `${conn}/cnb/repositories` && conflict)
          return json(200, { ...configuration, version: 4 });
        if (call.path === `${conn}/webhook`)
          return apiError(403, "CNB_CAPABILITY_DENIED");
        return undefined;
      }),
    );
    await screen.findByText(/已选择 0 个仓库/);
    expect(
      calls.some(
        (c) => c.path.endsWith("/webhook") || c.path === `${conn}/repositories`,
      ),
    ).toBe(false);
    await user.click(screen.getByRole("button", { name: "读取可访问仓库" }));
    await user.click(
      await screen.findByRole("checkbox", { name: "team/repo" }),
    );
    await user.click(screen.getByRole("button", { name: "保存仓库选择" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        version: 3,
        repositories: [{ id: "repo-id", path: "team/repo" }],
      }),
    );
    await screen.findAllByText("内容已被他人修改，已刷新");
    await user.click(screen.getByRole("button", { name: "查看事件桥接配置" }));
    await screen.findByText(/账号令牌未因此标记撤销/);
  });
});
